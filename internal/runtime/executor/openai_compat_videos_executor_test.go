package executor

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	auth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	core "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestOpenAICompatVideoPayloadBarrierAndTaskRequests(t *testing.T) {
	var requests int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.Header.Get("Authorization") != "Bearer video-key" {
			t.Error("missing upstream authentication")
		}
		body, _ := io.ReadAll(r.Body)
		switch r.Method + " " + r.URL.Path {
		case "POST /v1/videos":
			if gjson.GetBytes(body, "model").String() != "configured-video" || gjson.GetBytes(body, "images.0").String() != "second" || gjson.GetBytes(body, "images.1").Exists() || gjson.GetBytes(body, "prompt").Exists() {
				t.Errorf("payload barrier violated: %s", body)
			}
			if gjson.GetBytes(body, "duration").Int() != 4 || gjson.GetBytes(body, "image_url").String() != "data:image/png;base64,AA==" {
				t.Errorf("video fields changed: %s", body)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"task-1","status":"queued"}`))
		case "GET /v1/videos/task-1":
			if len(body) != 0 {
				t.Errorf("GET sent internal task payload: %s", body)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"task-1","status":"completed","video":{"url":"https://example.com/movie.mp4"}}`))
		case "GET /v1/videos/task-1/content":
			w.Header().Set("Content-Type", "video/mp4")
			_, _ = w.Write([]byte("video-content"))
		default:
			t.Errorf("unexpected upstream request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	cfg := &config.Config{Payload: config.PayloadConfig{
		Override: []config.PayloadRule{{Models: []config.PayloadModelRule{{Name: "*"}}, Params: map[string]any{"model": "configured-video"}}},
		Filter:   []config.PayloadFilterRule{{Models: []config.PayloadModelRule{{Name: "*"}}, Params: []string{"prompt", "images.0"}}},
	}}
	ex := NewOpenAICompatExecutor("compat-video", cfg)
	credential := &auth.Auth{ID: t.Name(), Provider: "compat-video", Attributes: map[string]string{"base_url": server.URL + "/v1", "api_key": "video-key"}}
	options := func(path string) core.Options {
		return core.Options{SourceFormat: translator.FromString("openai-video"), Metadata: map[string]any{core.RequestPathMetadataKey: path}}
	}
	created, err := ex.Execute(context.Background(), credential, core.Request{Model: "upstream-video", Payload: []byte(`{"model":"alias","prompt":"remove","images":["first","second"],"duration":4,"image_url":"data:image/png;base64,AA=="}`)}, options("/v1/videos/generations"))
	if err != nil || gjson.GetBytes(created.Payload, "status").String() != "queued" {
		t.Fatalf("create: %s %v", created.Payload, err)
	}
	retrieved, err := ex.Execute(context.Background(), credential, core.Request{Model: "upstream-video", Payload: []byte(`{"operation":"retrieve","id":"task-1"}`)}, options("/v1/videos/cpa-video.task"))
	if err != nil || gjson.GetBytes(retrieved.Payload, "status").String() != "completed" {
		t.Fatalf("retrieve: %s %v", retrieved.Payload, err)
	}
	content, err := ex.Execute(context.Background(), credential, core.Request{Model: "upstream-video", Payload: []byte(`{"operation":"content","id":"task-1"}`)}, options("/v1/videos/cpa-video.task/content"))
	if err != nil || string(content.Payload) != "video-content" || content.Headers.Get("Content-Type") != "video/mp4" {
		t.Fatalf("content: %+v %v", content, err)
	}
	if requests != 3 {
		t.Fatalf("requests=%d", requests)
	}
	for _, body := range []string{`{"operation":"retrieve","id":"../admin"}`, `{"operation":"retrieve","id":".."}`, `{"operation":"delete","id":"task-1"}`} {
		_, err = ex.Execute(context.Background(), credential, core.Request{Model: "upstream-video", Payload: []byte(body)}, options("/v1/videos/cpa-video.task"))
		if err == nil {
			t.Fatalf("unsafe task accepted: %s", body)
		}
	}
	if requests != 3 {
		t.Fatal("unsafe task reached upstream")
	}
}

func TestOpenAICompatVideoPreservesUpstreamError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte(`{"detail":"invalid video duration"}`))
	}))
	defer server.Close()
	ex := NewOpenAICompatExecutor("compat-video", &config.Config{})
	_, err := ex.Execute(context.Background(), &auth.Auth{Attributes: map[string]string{"base_url": server.URL}}, core.Request{Model: "video", Payload: []byte(`{"prompt":"bird"}`)}, core.Options{SourceFormat: translator.FromString("openai-video")})
	if err == nil {
		t.Fatal("upstream failure hidden")
	}
	status, ok := err.(interface{ StatusCode() int })
	if !ok || status.StatusCode() != 422 || err.Error() != `{"detail":"invalid video duration"}` {
		t.Fatalf("lost upstream error: %v", err)
	}
}
