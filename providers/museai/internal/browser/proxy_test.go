package browser

import (
	"bufio"
	"context"
	"encoding/base64"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestProxyBridgeHidesAuthenticationAndTunnels(t *testing.T) {
	echo, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer echo.Close()
	go func() {
		conn, e := echo.Accept()
		if e != nil {
			return
		}
		defer conn.Close()
		_, _ = io.Copy(conn, conn)
	}()
	var authorized atomic.Bool
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		expected := "Basic " + base64.StdEncoding.EncodeToString([]byte("user:password"))
		if r.Method != "CONNECT" || r.Header.Get("Proxy-Authorization") != expected {
			http.Error(w, "denied", 407)
			return
		}
		authorized.Store(true)
		remote, e := net.Dial("tcp", r.Host)
		if e != nil {
			http.Error(w, "unavailable", 502)
			return
		}
		defer remote.Close()
		conn, buffer, e := w.(http.Hijacker).Hijack()
		if e != nil {
			return
		}
		defer conn.Close()
		_, _ = buffer.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n")
		_ = buffer.Flush()
		done := make(chan struct{})
		go func() { _, _ = io.Copy(remote, buffer); _ = remote.Close(); close(done) }()
		_, _ = io.Copy(conn, remote)
		_ = conn.Close()
		<-done
	}))
	defer upstream.Close()
	bridge, err := newProxy(strings.Replace(upstream.URL, "http://", "http://user:password@", 1))
	if err != nil {
		t.Fatal(err)
	}
	defer bridge.close()
	if strings.Contains(bridge.address(), "user") || strings.Contains(bridge.address(), "password") {
		t.Fatal("proxy address leaks credentials")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", bridge.listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
	_, _ = io.WriteString(conn, "CONNECT "+echo.Addr().String()+" HTTP/1.1\r\nHost: "+echo.Addr().String()+"\r\n\r\n")
	reader := bufio.NewReader(conn)
	response, err := http.ReadResponse(reader, &http.Request{Method: "CONNECT"})
	if err != nil || response.StatusCode != 200 {
		t.Fatal("bridge CONNECT failed")
	}
	_, _ = io.WriteString(conn, "echo")
	var got [4]byte
	if _, err = io.ReadFull(reader, got[:]); err != nil || string(got[:]) != "echo" || !authorized.Load() {
		t.Fatal("authenticated tunnel did not deliver bytes")
	}
}
func TestProxySchemesFailClosed(t *testing.T) {
	for _, raw := range []string{"ftp://proxy.example", "http://proxy.example/path", "socks4://proxy.example", "http://proxy.example:0", "not-a-proxy"} {
		if p, err := newProxy(raw); err == nil {
			p.close()
			t.Fatal("invalid proxy accepted")
		}
	}
}
