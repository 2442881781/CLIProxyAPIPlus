// Package browser drives an owned ephemeral Chromium session, never a user's
// browser. It does not retry submissions or translate DOM text into token usage.
package browser

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Config struct {
	Executable   string
	DataDir      string
	NoSandbox    bool
	PollInterval time.Duration
}
type Cookie struct {
	Name    string  `json:"name"`
	Value   string  `json:"value"`
	Expires float64 `json:"expires,omitempty"`
}
type Reference struct {
	Data []byte
	MIME string
}
type Input struct {
	AuthID     string
	ProxyURL   string
	Cookies    []Cookie
	Prompt     string
	Kind       string
	References []Reference
	ThreadURL  string
}
type Event struct {
	Text      string
	ThreadURL string
	Submitted bool
	Completed bool
}
type Media struct {
	Data   []byte
	MIME   string
	Width  int
	Height int
}
type Result struct {
	Text      string
	ThreadURL string
	Cookies   []Cookie
	Media     []Media
}
type Error struct {
	Code       string
	Message    string
	HTTPStatus int
	Submitted  bool
}

func (e *Error) Error() string { return e.Message }

// Tests in this package may bind a local fixture origin. Production never accepts
// an origin from a request, environment variable, or credential.
var museOrigin = "https://muse.ai"

type Engine struct {
	cfg    Config
	mu     sync.Mutex
	closed bool
	active map[*session]struct{}
}
type session struct {
	engine      *Engine
	cmd         *exec.Cmd
	processDone chan struct{}
	profile     string
	bridge      *proxyBridge
	browser     *cdp
	page        *cdp
	target      string
	cancel      context.CancelFunc
	once        sync.Once
	finished    chan struct{}
}

func New(cfg Config) (*Engine, error) {
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = 150 * time.Millisecond
	}
	if cfg.DataDir == "" {
		return nil, &Error{Code: "browser_config", Message: "browser data directory is required", HTTPStatus: 503}
	}
	if cfg.Executable == "" {
		for _, name := range []string{"chromium", "chromium-browser", "google-chrome", "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome", "/Applications/Chromium.app/Contents/MacOS/Chromium"} {
			if path, err := exec.LookPath(name); err == nil {
				cfg.Executable = path
				break
			}
		}
	}
	path, err := exec.LookPath(cfg.Executable)
	if err != nil {
		return nil, &Error{Code: "browser_unavailable", Message: "owned Chromium executable is unavailable", HTTPStatus: 503}
	}
	cfg.Executable = path
	if err = os.MkdirAll(cfg.DataDir, 0700); err != nil {
		return nil, &Error{Code: "browser_config", Message: "cannot create browser data directory", HTTPStatus: 503}
	}
	return &Engine{cfg: cfg, active: make(map[*session]struct{})}, nil
}
func (e *Engine) Status() any {
	e.mu.Lock()
	defer e.mu.Unlock()
	return map[string]any{"available": !e.closed, "closed": e.closed, "active_sessions": len(e.active), "owned": true}
}
func (e *Engine) Close() error {
	e.mu.Lock()
	e.closed = true
	sessions := make([]*session, 0, len(e.active))
	for s := range e.active {
		sessions = append(sessions, s)
	}
	e.mu.Unlock()
	for _, s := range sessions {
		s.cancel()
	}
	// Let the request's non-canceled cleanup read rotated cookies before it
	// tears down CDP. Run/Resume wake immediately through their local context.
	for _, s := range sessions {
		<-s.finished
	}
	return nil
}
func (s *session) close() {
	s.once.Do(func() {
		s.cancel()
		s.engine.mu.Lock()
		page, browser, cmd, done, bridge, profile := s.page, s.browser, s.cmd, s.processDone, s.bridge, s.profile
		s.engine.mu.Unlock()
		if page != nil {
			cleanup, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			var ignored bool
			_ = page.evaluate(cleanup, stopJS, &ignored)
			cancel()
		}
		if page != nil {
			page.close()
		}
		if browser != nil {
			browser.close()
		}
		if cmd != nil && cmd.Process != nil && done != nil {
			interruptProcess(cmd)
			select {
			case <-done:
			case <-time.After(2 * time.Second):
				killProcess(cmd)
				<-done
			}
			// Chromium children inherit our private process group, never a user's.
			killProcess(cmd)
		}
		if bridge != nil {
			bridge.close()
		}
		if profile != "" {
			_ = os.RemoveAll(profile)
		}
		s.engine.mu.Lock()
		delete(s.engine.active, s)
		s.engine.mu.Unlock()
		close(s.finished)
	})
}
func (e *Engine) open(ctx context.Context, in Input) (*session, context.Context, error) {
	ctx, cancel := context.WithCancel(ctx)
	s := &session{engine: e, cancel: cancel, finished: make(chan struct{})}
	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		cancel()
		return nil, ctx, errors.New("browser engine is closed")
	}
	e.active[s] = struct{}{}
	e.mu.Unlock()
	// Publish lifecycle fields under the same lock as Close. Startup itself is
	// canceled by Close, which waits for ownership initialization before cleanup.
	startup, stop := context.WithTimeout(ctx, 45*time.Second)
	defer stop()
	e.mu.Lock()
	if ctx.Err() != nil {
		e.mu.Unlock()
		s.close()
		return nil, ctx, ctx.Err()
	}
	var err error
	s.profile, err = os.MkdirTemp(e.cfg.DataDir, "muse-session-")
	if err == nil && in.ProxyURL != "" && in.ProxyURL != "direct" {
		s.bridge, err = newProxy(in.ProxyURL)
	}
	if err != nil {
		e.mu.Unlock()
		s.close()
		return nil, ctx, errors.New("cannot initialize owned browser transport")
	}
	args := []string{"--headless=new", "--disable-gpu", "--disable-dev-shm-usage", "--disable-background-networking", "--disable-quic", "--force-webrtc-ip-handling-policy=disable_non_proxied_udp", "--no-first-run", "--no-default-browser-check", "--autoplay-policy=no-user-gesture-required", "--window-size=1440,2400", "--remote-debugging-address=127.0.0.1", "--remote-debugging-port=0", "--user-data-dir=" + s.profile}
	if e.cfg.NoSandbox {
		args = append(args, "--no-sandbox")
	}
	if s.bridge != nil {
		args = append(args, "--proxy-server="+s.bridge.address(), "--proxy-bypass-list=<-loopback>")
	} else {
		args = append(args, "--no-proxy-server")
	}
	args = append(args, "about:blank")
	s.cmd = exec.Command(e.cfg.Executable, args...)
	s.cmd.Stdout = io.Discard
	s.cmd.Stderr = io.Discard
	configureProcess(s.cmd)
	err = s.cmd.Start()
	if err == nil {
		s.processDone = make(chan struct{})
		go func() { _ = s.cmd.Wait(); close(s.processDone) }()
	}
	e.mu.Unlock()
	if err != nil {
		s.close()
		return nil, ctx, errors.New("cannot launch owned Chromium")
	}
	endpoint := ""
	port := 0
	for endpoint == "" {
		data, readErr := os.ReadFile(filepath.Join(s.profile, "DevToolsActivePort"))
		if readErr == nil {
			lines := strings.Split(strings.TrimSpace(string(data)), "\n")
			if len(lines) == 2 {
				port, _ = strconv.Atoi(lines[0])
				if port > 0 && port <= 65535 && strings.HasPrefix(lines[1], "/devtools/browser/") {
					endpoint = fmt.Sprintf("ws://127.0.0.1:%d%s", port, lines[1])
				}
			}
		}
		if endpoint != "" {
			break
		}
		select {
		case <-startup.Done():
			s.close()
			return nil, ctx, errors.New("owned Chromium startup did not complete")
		case <-s.processDone:
			s.close()
			return nil, ctx, errors.New("owned Chromium exited during startup")
		case <-time.After(40 * time.Millisecond):
		}
	}
	browser, err := connectCDP(startup, endpoint)
	if err != nil {
		s.close()
		return nil, ctx, err
	}
	e.mu.Lock()
	if ctx.Err() != nil {
		e.mu.Unlock()
		browser.close()
		s.close()
		return nil, ctx, ctx.Err()
	}
	s.browser = browser
	e.mu.Unlock()
	var created struct {
		TargetID string `json:"targetId"`
	}
	if err = s.browser.call(startup, "Target.createTarget", map[string]any{"url": "about:blank"}, &created); err != nil {
		s.close()
		return nil, ctx, err
	}
	e.mu.Lock()
	s.target = created.TargetID
	e.mu.Unlock()
	client := &http.Client{Transport: &http.Transport{Proxy: nil}, Timeout: 5 * time.Second}
	defer client.CloseIdleConnections()
	pageEndpoint := ""
	for pageEndpoint == "" {
		req, _ := http.NewRequestWithContext(startup, http.MethodGet, fmt.Sprintf("http://127.0.0.1:%d/json/list", port), nil)
		response, requestErr := client.Do(req)
		if requestErr == nil {
			var targets []struct {
				ID       string `json:"id"`
				Type     string `json:"type"`
				Endpoint string `json:"webSocketDebuggerUrl"`
			}
			decodeErr := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&targets)
			_ = response.Body.Close()
			if decodeErr == nil {
				for _, t := range targets {
					if t.ID == created.TargetID && t.Type == "page" {
						pageEndpoint = t.Endpoint
						break
					}
				}
			}
		}
		if pageEndpoint != "" {
			break
		}
		if err = delay(startup, 40*time.Millisecond); err != nil {
			s.close()
			return nil, ctx, err
		}
	}
	page, err := connectCDP(startup, pageEndpoint)
	if err != nil {
		s.close()
		return nil, ctx, err
	}
	e.mu.Lock()
	if ctx.Err() != nil {
		e.mu.Unlock()
		page.close()
		s.close()
		return nil, ctx, ctx.Err()
	}
	s.page = page
	e.mu.Unlock()
	for _, method := range []string{"Network.enable", "Page.enable", "Runtime.enable"} {
		if err = page.call(startup, method, nil, nil); err != nil {
			s.close()
			return nil, ctx, err
		}
	}
	return s, ctx, nil
}
func delay(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
func threadURL(raw string) (string, error) {
	u, err := url.Parse(raw)
	origin, _ := url.Parse(museOrigin)
	if err != nil || u.Scheme != origin.Scheme || u.Host != origin.Host || u.User != nil || !strings.HasPrefix(u.Path, "/thread/") || u.Path == "/thread/new" || strings.TrimPrefix(u.Path, "/thread/") == "" || strings.Contains(strings.TrimPrefix(u.Path, "/thread/"), "/") {
		return "", errors.New("invalid Muse thread URL")
	}
	id := strings.TrimPrefix(u.Path, "/thread/")
	if id == "." || id == ".." || strings.ContainsAny(id, "\\\r\n\x00") {
		return "", errors.New("invalid Muse thread URL")
	}
	return (&url.URL{Scheme: origin.Scheme, Host: origin.Host, Path: u.Path}).String(), nil
}
func (s *session) readCookies(ctx context.Context) ([]Cookie, error) {
	var result struct {
		Cookies []Cookie `json:"cookies"`
	}
	err := s.page.call(ctx, "Network.getCookies", map[string]any{"urls": []string{museOrigin}}, &result)
	return result.Cookies, err
}
func (s *session) applyCookies(ctx context.Context, cookies []Cookie) error {
	origin, _ := url.Parse(museOrigin)
	for _, cookie := range cookies {
		if cookie.Name == "" || strings.ContainsAny(cookie.Name, ";\r\n\x00") || strings.ContainsAny(cookie.Value, "\r\n\x00") {
			return errors.New("invalid credential cookie")
		}
		params := map[string]any{"name": cookie.Name, "value": cookie.Value, "url": museOrigin + "/", "path": "/", "secure": origin.Scheme == "https"}
		if cookie.Expires > 0 {
			params["expires"] = cookie.Expires
		}
		var result struct {
			Success bool `json:"success"`
		}
		if err := s.page.call(ctx, "Network.setCookie", params, &result); err != nil {
			return err
		}
		if !result.Success {
			return errors.New("credential cookie was rejected")
		}
	}
	return nil
}
