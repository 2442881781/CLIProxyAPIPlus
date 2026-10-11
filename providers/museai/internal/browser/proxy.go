package browser

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
)

// Chromium sees only a loopback HTTP proxy. Upstream credentials never enter
// argv, log output, CDP endpoints, or browser profile preferences.
type proxyBridge struct {
	upstream *url.URL
	listener net.Listener
	server   *http.Server
	ctx      context.Context
	cancel   context.CancelFunc
	mu       sync.Mutex
	conns    map[net.Conn]struct{}
}

func newProxy(raw string) (*proxyBridge, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" || u.Path != "" && u.Path != "/" || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("invalid browser proxy")
	}
	switch u.Scheme {
	case "http", "https", "socks5", "socks5h":
	default:
		return nil, errors.New("unsupported browser proxy scheme")
	}
	if u.Port() != "" {
		n, e := strconv.Atoi(u.Port())
		if e != nil || n < 1 || n > 65535 {
			return nil, errors.New("invalid browser proxy port")
		}
	}
	if u.User != nil && (len(u.User.Username()) > 255 || func() bool { p, _ := u.User.Password(); return len(p) > 255 }()) && strings.HasPrefix(u.Scheme, "socks") {
		return nil, errors.New("invalid SOCKS credentials")
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, errors.New("cannot start browser proxy bridge")
	}
	ctx, cancel := context.WithCancel(context.Background())
	p := &proxyBridge{upstream: u, listener: ln, ctx: ctx, cancel: cancel, conns: make(map[net.Conn]struct{})}
	p.server = &http.Server{Handler: p, ReadHeaderTimeout: 0}
	go func() { _ = p.server.Serve(ln) }()
	return p, nil
}
func (p *proxyBridge) address() string { return "http://" + p.listener.Addr().String() }
func (p *proxyBridge) track(c net.Conn) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.ctx.Err() != nil {
		_ = c.Close()
		return false
	}
	p.conns[c] = struct{}{}
	return true
}
func (p *proxyBridge) drop(c net.Conn) { p.mu.Lock(); delete(p.conns, c); p.mu.Unlock(); _ = c.Close() }
func (p *proxyBridge) close() {
	p.cancel()
	_ = p.server.Close()
	p.mu.Lock()
	for c := range p.conns {
		_ = c.Close()
	}
	p.mu.Unlock()
}
func (p *proxyBridge) dial(ctx context.Context, address string) (net.Conn, error) {
	port := p.upstream.Port()
	if port == "" {
		switch p.upstream.Scheme {
		case "https":
			port = "443"
		case "http":
			port = "80"
		default:
			port = "1080"
		}
	}
	conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", net.JoinHostPort(p.upstream.Hostname(), port))
	if err != nil {
		return nil, errors.New("browser upstream proxy unavailable")
	}
	if !p.track(conn) {
		return nil, errors.New("browser proxy closed")
	}
	rawConn := conn
	ok := false
	defer func() {
		if !ok {
			p.drop(rawConn)
		}
	}()
	// Interrupt the handshake when this request or the owned bridge closes.
	stop := context.AfterFunc(ctx, func() { _ = rawConn.Close() })
	defer stop()
	if p.upstream.Scheme == "https" {
		secure := tls.Client(conn, &tls.Config{ServerName: p.upstream.Hostname(), MinVersion: tls.VersionTLS12})
		if secure.HandshakeContext(ctx) != nil {
			return nil, errors.New("browser proxy TLS failed")
		}
		conn = secure
	}
	if p.upstream.Scheme == "http" || p.upstream.Scheme == "https" {
		req := &http.Request{Method: http.MethodConnect, URL: &url.URL{Opaque: address}, Host: address, Header: make(http.Header)}
		if p.upstream.User != nil {
			pw, _ := p.upstream.User.Password()
			req.Header.Set("Proxy-Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(p.upstream.User.Username()+":"+pw)))
		}
		if req.Write(conn) != nil {
			return nil, errors.New("browser proxy handshake failed")
		}
		reader := bufio.NewReader(conn)
		response, e := http.ReadResponse(reader, req)
		if e != nil || response.StatusCode != http.StatusOK {
			return nil, errors.New("browser proxy CONNECT refused")
		}
		if reader.Buffered() > 0 {
			conn = &bufferedConn{Conn: conn, reader: reader}
		}
	} else if err = socksHandshake(ctx, conn, p.upstream, address); err != nil {
		return nil, err
	}
	ok = true
	return &bridgeConn{Conn: conn, bridge: p, tracked: rawConn}, nil
}

type bufferedConn struct {
	net.Conn
	reader *bufio.Reader
}

func (c *bufferedConn) Read(b []byte) (int, error) { return c.reader.Read(b) }

type bridgeConn struct {
	net.Conn
	bridge  *proxyBridge
	tracked net.Conn
	once    sync.Once
}

func (c *bridgeConn) Close() error {
	var err error
	c.once.Do(func() {
		err = c.Conn.Close()
		c.bridge.mu.Lock()
		delete(c.bridge.conns, c.tracked)
		c.bridge.mu.Unlock()
	})
	return err
}
func socksHandshake(ctx context.Context, c net.Conn, u *url.URL, address string) error {
	fail := func() error { return errors.New("browser SOCKS handshake failed") }
	method := byte(0)
	if u.User != nil {
		method = 2
	}
	if _, err := c.Write([]byte{5, 1, method}); err != nil {
		return fail()
	}
	var pair [2]byte
	if _, err := io.ReadFull(c, pair[:]); err != nil || pair[0] != 5 || pair[1] != method {
		return fail()
	}
	if method == 2 {
		user := u.User.Username()
		pw, _ := u.User.Password()
		msg := append([]byte{1, byte(len(user))}, []byte(user)...)
		msg = append(msg, byte(len(pw)))
		msg = append(msg, []byte(pw)...)
		if _, err := c.Write(msg); err != nil {
			return fail()
		}
		if _, err := io.ReadFull(c, pair[:]); err != nil || pair[0] != 1 || pair[1] != 0 {
			return fail()
		}
	}
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return fail()
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 {
		return fail()
	}
	ip := net.ParseIP(host)
	if ip == nil && u.Scheme == "socks5" {
		ips, e := net.DefaultResolver.LookupIPAddr(ctx, host)
		if e != nil || len(ips) == 0 {
			return fail()
		}
		ip = ips[0].IP
	}
	msg := []byte{5, 1, 0}
	if v := ip.To4(); v != nil {
		msg = append(msg, 1)
		msg = append(msg, v...)
	} else if ip != nil {
		msg = append(msg, 4)
		msg = append(msg, ip.To16()...)
	} else {
		if len(host) > 255 {
			return fail()
		}
		msg = append(msg, 3, byte(len(host)))
		msg = append(msg, []byte(host)...)
	}
	msg = binary.BigEndian.AppendUint16(msg, uint16(n))
	if _, err = c.Write(msg); err != nil {
		return fail()
	}
	var head [4]byte
	if _, err = io.ReadFull(c, head[:]); err != nil || head[0] != 5 || head[1] != 0 {
		return fail()
	}
	size := 0
	switch head[3] {
	case 1:
		size = 4
	case 4:
		size = 16
	case 3:
		var b [1]byte
		if _, err = io.ReadFull(c, b[:]); err != nil {
			return fail()
		}
		size = int(b[0])
	default:
		return fail()
	}
	_, err = io.CopyN(io.Discard, c, int64(size+2))
	if err != nil {
		return fail()
	}
	return nil
}
func (p *proxyBridge) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodConnect {
		remote, err := p.dial(p.ctx, r.Host)
		if err != nil {
			http.Error(w, "proxy connection failed", http.StatusBadGateway)
			return
		}
		defer remote.Close()
		client, buffer, err := w.(http.Hijacker).Hijack()
		if err != nil {
			return
		}
		if !p.track(client) {
			return
		}
		defer p.drop(client)
		if _, err = buffer.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
			return
		}
		if buffer.Flush() != nil {
			return
		}
		done := make(chan struct{})
		go func() { _, _ = io.Copy(remote, buffer); _ = remote.Close(); close(done) }()
		_, _ = io.Copy(client, remote)
		_ = client.Close()
		<-done
		return
	}
	if r.URL.Scheme != "http" || r.URL.Host == "" {
		http.Error(w, "invalid proxy request", http.StatusBadRequest)
		return
	}
	address := r.URL.Host
	if r.URL.Port() == "" {
		address = net.JoinHostPort(r.URL.Hostname(), "80")
	}
	remote, err := p.dial(r.Context(), address)
	if err != nil {
		http.Error(w, "proxy connection failed", http.StatusBadGateway)
		return
	}
	defer remote.Close()
	request := r.Clone(r.Context())
	request.RequestURI = ""
	request.Header.Del("Proxy-Authorization")
	request.Header.Del("Proxy-Connection")
	request.Close = true
	if request.Write(remote) != nil {
		http.Error(w, "proxy request failed", http.StatusBadGateway)
		return
	}
	response, err := http.ReadResponse(bufio.NewReader(remote), request)
	if err != nil {
		http.Error(w, "proxy response failed", http.StatusBadGateway)
		return
	}
	defer response.Body.Close()
	for key, values := range response.Header {
		if strings.EqualFold(key, "Connection") || strings.EqualFold(key, "Proxy-Authenticate") {
			continue
		}
		for _, value := range values {
			w.Header().Add(key, value)
		}
	}
	w.WriteHeader(response.StatusCode)
	_, _ = io.Copy(w, response.Body)
}
