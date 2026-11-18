package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	proxyconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

// chatBody builds a chat-completions payload whose counted input tokens exceed
// the given threshold for a GPT tokenizer. Each repeated message tokenizes to
// well over one token, so the loop overshoots limit comfortably.
func chatBody(model string, approxTokens int) []byte {
	msg := strings.Repeat("lorem ipsum dolor sit amet ", (approxTokens/4)+64)
	body, _ := json.Marshal(map[string]any{
		"model":    model,
		"messages": []map[string]any{{"role": "user", "content": msg}},
	})
	return body
}

func newContextLimitEngine(t *testing.T, cfg *proxyconfig.Config) (*httptest.Server, *int) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	s := &Server{cfg: cfg, engine: gin.New()}
	hits := 0
	s.engine.POST("/v1/chat/completions", s.contextLimitMiddleware(), func(c *gin.Context) {
		hits++
		c.Status(http.StatusOK)
	})
	return httptest.NewServer(s.engine), &hits
}

func postChat(t *testing.T, url string, body []byte) (*http.Response, map[string]any) {
	t.Helper()
	resp, errPost := http.Post(url+"/v1/chat/completions", "application/json", bytes.NewReader(body))
	if errPost != nil {
		t.Fatalf("POST error: %v", errPost)
	}
	defer resp.Body.Close()
	var parsed map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&parsed)
	return resp, parsed
}

func TestContextLimitRejectsOversizedGPTRequest(t *testing.T) {
	cfg := &proxyconfig.Config{}
	cfg.ContextLimit.Default = 100 // tiny cap so the test stays cheap
	srv, hits := newContextLimitEngine(t, cfg)
	defer srv.Close()

	resp, parsed := postChat(t, srv.URL, chatBody("gpt-6.2-sol", 2000))
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%v", resp.StatusCode, parsed)
	}
	if *hits != 0 {
		t.Fatalf("downstream handler reached; request should have been blocked")
	}
	errObj, _ := parsed["error"].(map[string]any)
	if errObj["code"] != "context_length_exceeded" {
		t.Fatalf("error.code = %v, want context_length_exceeded", errObj["code"])
	}
}

func TestContextLimitPassesUnderLimit(t *testing.T) {
	cfg := &proxyconfig.Config{}
	cfg.ContextLimit.Default = 272000
	srv, hits := newContextLimitEngine(t, cfg)
	defer srv.Close()

	resp, _ := postChat(t, srv.URL, chatBody("gpt-6-sol", 200))
	if resp.StatusCode != http.StatusOK || *hits != 1 {
		t.Fatalf("status = %d hits = %d, want 200/1", resp.StatusCode, *hits)
	}
}

func TestContextLimitIgnoresNonGPT(t *testing.T) {
	cfg := &proxyconfig.Config{}
	cfg.ContextLimit.Default = 100
	srv, hits := newContextLimitEngine(t, cfg)
	defer srv.Close()

	resp, _ := postChat(t, srv.URL, chatBody("claude-opus-4", 5000))
	if resp.StatusCode != http.StatusOK || *hits != 1 {
		t.Fatalf("non-GPT model should pass; status = %d hits = %d", resp.StatusCode, *hits)
	}
}

func TestContextLimitDisabled(t *testing.T) {
	cfg := &proxyconfig.Config{}
	cfg.ContextLimit.Enabled = new(false)
	cfg.ContextLimit.Default = 100
	srv, hits := newContextLimitEngine(t, cfg)
	defer srv.Close()

	resp, _ := postChat(t, srv.URL, chatBody("gpt-6.2-sol", 5000))
	if resp.StatusCode != http.StatusOK || *hits != 1 {
		t.Fatalf("disabled guard should pass; status = %d hits = %d", resp.StatusCode, *hits)
	}
}
func TestResolveContextTokenLimit(t *testing.T) {
	tests := []struct {
		name  string
		cfg   proxyconfig.ContextLimitConfig
		model string
		want  int
	}{
		{name: "sol fallback", model: "gpt-9-sol", want: 272000},
		{name: "sol override", cfg: proxyconfig.ContextLimitConfig{SolDefault: 500000}, model: "gpt-9-sol", want: 500000},
		{name: "non-sol default", cfg: proxyconfig.ContextLimitConfig{Default: 100}, model: "gpt-9-astra", want: 100},
		{name: "explicit rule by name", cfg: proxyconfig.ContextLimitConfig{Models: []proxyconfig.ContextLimitRule{{Name: "gpt-9-x", Limit: 42}}}, model: "gpt-9-x", want: 42},
		{name: "explicit rule by alias", cfg: proxyconfig.ContextLimitConfig{Models: []proxyconfig.ContextLimitRule{{Name: "upstream", Alias: "gpt-9-x", Limit: 42}}}, model: "gpt-9-x", want: 42},
		{name: "non-gpt passes", model: "claude-opus-4", want: 0},
		{name: "prefixed gpt still gpt", model: "teamA/gpt-9-sol", want: 272000},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := resolveContextTokenLimit(tt.cfg, tt.model); got != tt.want {
				t.Fatalf("resolveContextTokenLimit(%q) = %d, want %d", tt.model, got, tt.want)
			}
		})
	}
}
