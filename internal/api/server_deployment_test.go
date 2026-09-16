package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

func TestDeploymentReadinessAndDrain(t *testing.T) {
	server := newTestServerWithOptions(t)
	server.deploymentControlKey = "deploy-secret"
	server.deploymentReady.Store(true)

	recorder := httptest.NewRecorder()
	server.engine.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("readyz status = %d, want %d", recorder.Code, http.StatusOK)
	}

	recorder = httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v0/deployment/drain", nil)
	req.RemoteAddr = "127.0.0.1:12345"
	req.Header.Set(deploymentControlHeader, "deploy-secret")
	server.engine.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusOK {
		t.Fatalf("drain status = %d, want %d", recorder.Code, http.StatusOK)
	}

	recorder = httptest.NewRecorder()
	server.engine.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("draining readyz status = %d, want %d", recorder.Code, http.StatusServiceUnavailable)
	}

	recorder = httptest.NewRecorder()
	server.engine.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/v1/models", nil))
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("new request while draining status = %d, want %d", recorder.Code, http.StatusServiceUnavailable)
	}
}

func TestDeploymentControlRequiresLoopbackAndToken(t *testing.T) {
	server := newTestServerWithOptions(t)
	server.deploymentControlKey = "deploy-secret"
	server.deploymentReady.Store(true)

	tests := []struct {
		name       string
		remoteAddr string
		token      string
	}{
		{name: "missing token", remoteAddr: "127.0.0.1:12345"},
		{name: "wrong token", remoteAddr: "127.0.0.1:12345", token: "wrong"},
		{name: "non-loopback", remoteAddr: "192.0.2.10:12345", token: "deploy-secret"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/v0/deployment/status", nil)
			req.RemoteAddr = tt.remoteAddr
			req.Header.Set(deploymentControlHeader, tt.token)
			server.engine.ServeHTTP(recorder, req)
			if recorder.Code != http.StatusNotFound {
				t.Fatalf("status = %d, want %d", recorder.Code, http.StatusNotFound)
			}
		})
	}
}

func TestDeploymentMiddlewareTracksActiveRequests(t *testing.T) {
	server := &Server{}
	entered := make(chan struct{})
	release := make(chan struct{})
	engine := gin.New()
	engine.Use(server.deploymentLifecycleMiddleware())
	engine.GET("/stream", func(c *gin.Context) {
		close(entered)
		<-release
		c.Status(http.StatusNoContent)
	})

	done := make(chan struct{})
	go func() {
		defer close(done)
		engine.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/stream", nil))
	}()
	<-entered
	if got := server.activeRequests.Load(); got != 1 {
		t.Fatalf("active requests = %d, want 1", got)
	}
	close(release)
	<-done
	if got := server.activeRequests.Load(); got != 0 {
		t.Fatalf("active requests after completion = %d, want 0", got)
	}
}

func TestServerStartMarksReady(t *testing.T) {
	server := newTestServerWithOptions(t)
	server.server.Addr = "127.0.0.1:0"
	errCh := make(chan error, 1)
	go func() { errCh <- server.Start() }()

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) && !server.deploymentReady.Load() {
		time.Sleep(time.Millisecond)
	}
	if !server.deploymentReady.Load() {
		t.Fatal("server did not become ready")
	}
	if errStop := server.Stop(context.Background()); errStop != nil {
		t.Fatalf("Stop() error = %v", errStop)
	}
	if errStart := <-errCh; errStart != nil {
		t.Fatalf("Start() error = %v", errStart)
	}
}

func TestDeploymentControlTokenFile(t *testing.T) {
	path := t.TempDir() + "/token"
	if errWrite := os.WriteFile(path, []byte("file-secret\n"), 0o600); errWrite != nil {
		t.Fatal(errWrite)
	}
	t.Setenv("CLIPROXY_DEPLOY_CONTROL_TOKEN", "")
	t.Setenv("CLIPROXY_DEPLOY_CONTROL_TOKEN_FILE", path)
	if got := deploymentControlToken(); got != "file-secret" {
		t.Fatalf("deploymentControlToken() = %q, want %q", got, "file-secret")
	}
}
