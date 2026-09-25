package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

func basispointsTestConfig(baseURL string, nativeFallback bool) *config.Config {
	return &config.Config{Codex: config.CodexConfig{Basispoints: config.CodexBasispointsConfig{
		Enabled:        true,
		BaseURL:        baseURL,
		Models:         []string{"gpt-5.6-sol"},
		NativeFallback: nativeFallback,
	}}}
}

func basispointsTestAuth() *cliproxyauth.Auth {
	return &cliproxyauth.Auth{
		ID:       "codex-bps-auth",
		Provider: "codex",
		Attributes: map[string]string{
			cliproxyauth.AttributeAuthKind:         cliproxyauth.AuthKindOAuth,
			cliproxyauth.AttributeCodexBasispoints: "true",
		},
		Metadata: map[string]any{
			"type":         "codex",
			"access_token": "oauth-token",
			"account_id":   "account-1",
			"basispoints":  true,
		},
	}
}

func basispointsCompletedSSE(model, text string) string {
	response := map[string]any{
		"id":     "resp_bps",
		"object": "response",
		"status": "completed",
		"model":  model,
		"output": []any{map[string]any{
			"id": "msg_bps", "type": "message", "role": "assistant", "status": "completed",
			"content": []any{map[string]any{"type": "output_text", "text": text, "annotations": []any{}}},
		}},
		"usage": map[string]any{"input_tokens": 4, "output_tokens": 2, "total_tokens": 6},
	}
	payload, _ := json.Marshal(map[string]any{"type": "response.completed", "response": response})
	return "event: response.completed\ndata: " + string(payload) + "\n\n"
}

func TestCodexBasispointsEnabledRequiresGlobalCredentialOAuthAndAllowedModel(t *testing.T) {
	executor := NewCodexExecutor(basispointsTestConfig("https://example.invalid", true))
	auth := basispointsTestAuth()
	for _, tc := range []struct {
		name   string
		mutate func(*config.Config, *cliproxyauth.Auth)
		model  string
		want   bool
	}{
		{name: "enabled", model: "gpt-5.6-sol", want: true},
		{name: "dated snapshot", model: "gpt-5.6-sol-2026-09-18", want: true},
		{name: "global disabled", model: "gpt-5.6-sol", mutate: func(cfg *config.Config, _ *cliproxyauth.Auth) { cfg.Codex.Basispoints.Enabled = false }},
		{name: "credential disabled", model: "gpt-5.6-sol", mutate: func(_ *config.Config, auth *cliproxyauth.Auth) {
			auth.Attributes[cliproxyauth.AttributeCodexBasispoints] = "false"
		}},
		{name: "api key credential", model: "gpt-5.6-sol", mutate: func(_ *config.Config, auth *cliproxyauth.Auth) {
			auth.Attributes[cliproxyauth.AttributeAuthKind] = cliproxyauth.AuthKindAPIKey
		}},
		{name: "unlisted model", model: "gpt-6-astra"},
		{name: "oauth with runtime token attribute", model: "gpt-5.6-sol", want: true, mutate: func(_ *config.Config, auth *cliproxyauth.Auth) {
			auth.Attributes[cliproxyauth.AttributeAPIKey] = "runtime-oauth-token"
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfgCopy := *executor.cfg
			cfgCopy.Codex = executor.cfg.Codex
			authCopy := auth.Clone()
			if tc.mutate != nil {
				tc.mutate(&cfgCopy, authCopy)
			}
			if got := NewCodexExecutor(&cfgCopy).basispointsEnabled(authCopy, tc.model); got != tc.want {
				t.Fatalf("basispointsEnabled() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestCodexExecutorBasispointsNonStreamUsesOAuthHeadersAndBridgesResponse(t *testing.T) {
	var seenBody []byte
	var seenHeaders http.Header
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenHeaders = r.Header.Clone()
		seenBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, basispointsCompletedSSE("gpt-5.6-sol", "21"))
	}))
	defer server.Close()

	executor := NewCodexExecutor(basispointsTestConfig(server.URL+"/basispoints/api/responses", true))
	response, err := executor.Execute(context.Background(), basispointsTestAuth(), cliproxyexecutor.Request{
		Model:   "gpt-5.6-sol",
		Payload: []byte(`{"model":"gpt-5.6-sol","input":"6*7/2","reasoning":{"effort":"max"}}`),
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse, ResponseFormat: sdktranslator.FormatOpenAIResponse})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if got := seenHeaders.Get("Authorization"); got != "Bearer oauth-token" {
		t.Fatalf("Authorization = %q", got)
	}
	if got := seenHeaders.Get("Chatgpt-Account-Id"); got != "account-1" {
		t.Fatalf("Chatgpt-Account-Id = %q", got)
	}
	if got := seenHeaders.Get("X-Basispoints-Auth-Mode"); got != "chatgpt" {
		t.Fatalf("X-Basispoints-Auth-Mode = %q", got)
	}
	if got := gjson.GetBytes(seenBody, "reasoning_effort").String(); got != "xhigh" {
		t.Fatalf("upstream reasoning_effort = %q; body=%s", got, seenBody)
	}
	if !gjson.GetBytes(seenBody, "stream").Bool() || gjson.GetBytes(seenBody, "store").Bool() {
		t.Fatalf("upstream stream/store contract changed: %s", seenBody)
	}
	if got := gjson.GetBytes(response.Payload, "output.0.content.0.text").String(); got != "21" {
		t.Fatalf("response text = %q; payload=%s", got, response.Payload)
	}
	if got := gjson.GetBytes(response.Payload, "reasoning.effort").String(); got != "xhigh" {
		t.Fatalf("response reasoning effort = %q; payload=%s", got, response.Payload)
	}
}

func TestCodexExecutorBasispointsStreamEmitsTranslatedTerminal(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"output_index\":0,\"content_index\":0,\"item_id\":\"msg_bps\",\"delta\":\"he\"}\n\n")
		_, _ = io.WriteString(w, basispointsCompletedSSE("gpt-5.6-sol", "hello"))
	}))
	defer server.Close()

	result, err := NewCodexExecutor(basispointsTestConfig(server.URL, true)).ExecuteStream(context.Background(), basispointsTestAuth(), cliproxyexecutor.Request{
		Model:   "gpt-5.6-sol",
		Payload: []byte(`{"model":"gpt-5.6-sol","input":"hello","stream":true}`),
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse, ResponseFormat: sdktranslator.FormatOpenAIResponse, Stream: true})
	if err != nil {
		t.Fatalf("ExecuteStream() error = %v", err)
	}
	var joined bytes.Buffer
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("stream chunk error = %v", chunk.Err)
		}
		joined.Write(chunk.Payload)
	}
	text := joined.String()
	if !strings.Contains(text, "response.output_text.delta") || !strings.Contains(text, "response.completed") || !strings.Contains(text, "hello") {
		t.Fatalf("translated stream missing events: %s", text)
	}
}

func TestCodexExecutorBasispointsCompactUsesResponsesEndpoint(t *testing.T) {
	var seenBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, basispointsCompletedSSE("gpt-5.6-sol", "summary"))
	}))
	defer server.Close()

	response, err := NewCodexExecutor(basispointsTestConfig(server.URL, true)).Execute(context.Background(), basispointsTestAuth(), cliproxyexecutor.Request{
		Model:   "gpt-5.6-sol",
		Payload: []byte(`{"model":"gpt-5.6-sol","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"old history"}]},{"type":"compaction_trigger"}]}`),
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse, ResponseFormat: sdktranslator.FormatOpenAIResponse, Alt: "responses/compact"})
	if err != nil {
		t.Fatalf("compact Execute() error = %v", err)
	}
	input := gjson.GetBytes(seenBody, "input").Array()
	if len(input) == 0 || input[len(input)-1].Get("type").String() != "compaction_trigger" {
		t.Fatalf("upstream compact trigger missing: %s", seenBody)
	}
	if got := gjson.GetBytes(response.Payload, "output.0.content.0.text").String(); got != "summary" {
		t.Fatalf("compact response text = %q; payload=%s", got, response.Payload)
	}
}

func TestCodexAutoExecutorNativeFallbackKeepsWebsocketRoute(t *testing.T) {
	executor := NewCodexAutoExecutor(basispointsTestConfig("https://example.invalid", true))
	auth := basispointsTestAuth()
	body := []byte(`{"model":"gpt-5.6-sol","input":"latest news","tools":[{"type":"web_search","external_web_access":true}]}`)
	if reason := executor.httpExec.basispointsNativeFallbackReason(body); reason == "" {
		t.Fatal("expected live web search to require native Codex")
	}
	if !executor.httpExec.basispointsEnabled(auth, "gpt-5.6-sol") {
		t.Fatal("test credential should otherwise be Basispoints eligible")
	}
}

func TestPrepareBasispointsRequestRejectsMissingAccountID(t *testing.T) {
	auth := basispointsTestAuth()
	delete(auth.Metadata, "account_id")
	_, _, _, err := NewCodexExecutor(basispointsTestConfig("https://example.invalid", true)).prepareBasispointsRequest(context.Background(), auth, []byte(`{"model":"gpt-5.6-sol","input":"hello"}`))
	if err == nil || !strings.Contains(err.Error(), "basispoints_account_id_missing") {
		t.Fatalf("error = %v, want basispoints_account_id_missing", err)
	}
}
