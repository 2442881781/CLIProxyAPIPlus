package test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/jb"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	runtimeexecutor "github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor"
	_ "github.com/router-for-me/CLIProxyAPI/v8/internal/translator"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	openaihandlers "github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers/openai"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// TestJBStreamRefusalRetryDeliversStreamFrames is the regression test for the
// stream refusal retry: the client must receive the retried answer as ordinary
// SSE delta frames. A previous implementation retried through the non-streaming
// executor and delivered one completion body with choices[].message, which
// strict clients (choices[].delta parsers) read as an empty response.
func TestJBStreamRefusalRetryDeliversStreamFrames(t *testing.T) {
	const model = "gpt-6-astra"
	var mu sync.Mutex
	var upstreamBodies []string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		upstreamBodies = append(upstreamBodies, string(body))
		call := len(upstreamBodies)
		mu.Unlock()

		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		if call == 1 {
			// First attempt: a soft refusal long enough for the detector.
			_, _ = fmt.Fprint(w, "data: {\"type\":\"response.output_text.delta\",\"output_index\":0,\"content_index\":0,\"item_id\":\"msg_1\",\"delta\":\"抱歉，我不能提供这个内容，因为它违反使用政策。这段需要超过二十个字符才会触发检测。\"}\n\n")
		} else {
			// Continuation attempt: real content.
			_, _ = fmt.Fprint(w, "data: {\"type\":\"response.output_text.delta\",\"output_index\":0,\"content_index\":0,\"item_id\":\"msg_2\",\"delta\":\"MOCK_FIXED_CONTENT\"}\n\n")
		}
		_, _ = fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_x\",\"status\":\"completed\",\"output\":[]}}\n\n")
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
	}))
	defer server.Close()

	cfg := &config.Config{}
	cfg.JB.Enabled = new(true)
	cfg.JB.Defaults = config.JBPrefs{
		JB:           new(true),
		NSFW:         new(true),
		RefusalRetry: new(true),
	}
	engine := jb.NewEngine(&cfg.SDKConfig, t.TempDir())
	if err := engine.Load(); err != nil {
		t.Fatalf("engine load: %v", err)
	}

	manager := cliproxyauth.NewManager(nil, &cliproxyauth.RoundRobinSelector{}, nil)
	manager.SetRetryConfig(0, 0, 0)
	manager.RegisterExecutor(runtimeexecutor.NewJBWiringExecutor(runtimeexecutor.NewCodexExecutor(cfg), engine))

	const authID = "jb-stream-retry-delivery"
	registry.GetGlobalRegistry().RegisterClient(authID, "codex", []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(authID) })

	if _, errRegister := manager.Register(context.Background(), &cliproxyauth.Auth{
		ID: authID, Provider: "codex", Status: cliproxyauth.StatusActive,
		Attributes: map[string]string{"base_url": server.URL, "api_key": "dummy"},
		Metadata:   map[string]any{"disable_cooling": true},
	}); errRegister != nil {
		t.Fatal(errRegister)
	}

	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(fmt.Sprintf(`{"model":%q,"stream":true,"messages":[{"role":"user","content":"hello"}]}`, model)))
	c.Request.Header.Set("Content-Type", "application/json")

	base := handlers.NewBaseAPIHandlers(&cfg.SDKConfig, manager)
	openaihandlers.NewOpenAIAPIHandler(base).ChatCompletions(c)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if got := recorder.Header().Get(jb.HeaderName); got != string(jb.StateRetryWrote) {
		t.Fatalf("X-JB = %q, want %q", got, jb.StateRetryWrote)
	}
	body := recorder.Body.String()
	if !strings.Contains(body, "MOCK_FIXED_CONTENT") {
		t.Fatalf("retried content missing from client stream: %s", body)
	}
	// The client must see chat delta frames; a completion body with
	// choices[].message reads as empty for streaming clients.
	if !strings.Contains(body, `"delta"`) {
		t.Fatalf("client stream must keep delta frames: %s", body)
	}
	if strings.Contains(body, "不能提供") {
		t.Fatalf("original refusal must not leak to the client: %s", body)
	}

	mu.Lock()
	calls := len(upstreamBodies)
	second := ""
	if calls > 1 {
		second = upstreamBodies[calls-1]
	}
	mu.Unlock()
	if calls != 2 {
		t.Fatalf("upstream calls = %d, want 2", calls)
	}
	if !strings.Contains(second, "续答规则") {
		t.Fatalf("continuation instruction missing from the retried upstream request: %s", second)
	}
}
