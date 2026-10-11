package browser

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
)

// cdp owns a single loopback connection. Responses are routed independently of
// events so an awaited page promise cannot block cancellation or target cleanup.
type cdp struct {
	conn             *websocket.Conn
	writeMu          sync.Mutex
	mu               sync.Mutex
	pending          map[uint64]chan cdpResponse
	next             atomic.Uint64
	done             chan struct{}
	once             sync.Once
	navigationStatus atomic.Int64
	origin           *url.URL
	mainFrame        atomic.Value
}
type cdpResponse struct {
	ID     uint64          `json:"id"`
	Result json.RawMessage `json:"result"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func connectCDP(ctx context.Context, endpoint string) (*cdp, error) {
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme != "ws" || u.Hostname() != "127.0.0.1" || u.User != nil {
		return nil, errors.New("invalid owned CDP endpoint")
	}
	d := websocket.Dialer{Proxy: nil}
	conn, _, err := d.DialContext(ctx, endpoint, nil)
	if err != nil {
		return nil, errors.New("cannot connect to owned Chromium")
	}
	conn.SetReadLimit(256 << 20)
	origin, _ := url.Parse(museOrigin)
	c := &cdp{conn: conn, pending: make(map[uint64]chan cdpResponse), done: make(chan struct{}), origin: origin}
	c.mainFrame.Store("")
	go c.readLoop()
	return c, nil
}
func (c *cdp) readLoop() {
	defer c.close()
	for {
		var r cdpResponse
		if c.conn.ReadJSON(&r) != nil {
			return
		}
		if r.ID == 0 {
			if r.Method == "Network.responseReceived" {
				var event struct {
					Type     string `json:"type"`
					FrameID  string `json:"frameId"`
					Response struct {
						URL    string  `json:"url"`
						Status float64 `json:"status"`
					} `json:"response"`
				}
				if json.Unmarshal(r.Params, &event) == nil && event.Type == "Document" && event.FrameID == c.mainFrame.Load().(string) {
					observed, _ := url.Parse(event.Response.URL)
					if observed != nil && c.origin != nil && observed.Host == c.origin.Host && observed.Scheme == c.origin.Scheme {
						c.navigationStatus.Store(int64(event.Response.Status))
					}
				}
			}
			continue
		}
		c.mu.Lock()
		ch := c.pending[r.ID]
		delete(c.pending, r.ID)
		c.mu.Unlock()
		if ch != nil {
			ch <- r
		}
	}
}
func (c *cdp) close() {
	c.once.Do(func() { close(c.done); _ = c.conn.Close() })
}
func (c *cdp) call(ctx context.Context, method string, params any, out any) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	id := c.next.Add(1)
	ch := make(chan cdpResponse, 1)
	c.mu.Lock()
	c.pending[id] = ch
	c.mu.Unlock()
	defer func() { c.mu.Lock(); delete(c.pending, id); c.mu.Unlock() }()
	c.writeMu.Lock()
	// Cancellation interrupts a stalled local socket write without closing the
	// connection needed by non-canceled stop/cookie cleanup. Only the writer
	// goroutine changes websocket deadlines (gorilla's concurrency contract).
	stopWrite := make(chan struct{})
	writeDone := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = c.conn.UnderlyingConn().SetWriteDeadline(time.Now())
		case <-stopWrite:
		}
		close(writeDone)
	}()
	err := c.conn.WriteJSON(struct {
		ID     uint64 `json:"id"`
		Method string `json:"method"`
		Params any    `json:"params,omitempty"`
	}{id, method, params})
	close(stopWrite)
	<-writeDone
	_ = c.conn.UnderlyingConn().SetWriteDeadline(time.Time{})
	c.writeMu.Unlock()
	if err != nil {
		return errors.New("Chromium connection closed")
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-c.done:
		return errors.New("Chromium connection closed")
	case r := <-ch:
		// CDP errors may contain page data, cookies, or credential-bearing URLs.
		if r.Error != nil {
			return errors.New("Chromium command failed: " + method)
		}
		if out != nil {
			return json.Unmarshal(r.Result, out)
		}
		return nil
	}
}
func (c *cdp) evaluate(ctx context.Context, expression string, out any) error {
	var r struct {
		Result struct {
			Value json.RawMessage `json:"value"`
			Type  string          `json:"type"`
		} `json:"result"`
		Exception json.RawMessage `json:"exceptionDetails"`
	}
	if err := c.call(ctx, "Runtime.evaluate", map[string]any{"expression": expression, "returnByValue": true, "awaitPromise": true}, &r); err != nil {
		return err
	}
	if len(r.Exception) > 0 || len(r.Result.Value) == 0 {
		return errors.New("page operation failed")
	}
	if out != nil {
		return json.Unmarshal(r.Result.Value, out)
	}
	return nil
}
func jsArg(v any) string { b, _ := json.Marshal(v); return string(b) }
