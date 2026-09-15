package plugin

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func TestRouteModelUsesHostProviderPath(t *testing.T) {
	cfg := parseConfig([]byte(`models:
  - alias: deepseek-flash
    name: deepseek/deepseek-v4.1-flash
`))
	resp, err := NewRouter(cfg).RouteModel(context.Background(), requestWithModel("commandcode/deepseek-flash"))
	if err != nil {
		t.Fatal(err)
	}
	if !resp.Handled || resp.TargetKind != pluginapi.ModelRouteTargetProvider || resp.Target != Provider || resp.TargetModel != "deepseek/deepseek-v4.1-flash" {
		t.Fatalf("route = %+v", resp)
	}
}

func TestExecutorUsesOnlyHostSelectedCredential(t *testing.T) {
	cfg := parseConfig([]byte("api_keys:\n  - key: user_config_a\n  - key: user_config_b\n"))
	client := &recordingHTTPClient{response: pluginapi.HTTPResponse{
		StatusCode: http.StatusOK,
		Body:       []byte(`{"choices":[]}`),
	}}
	executor := NewExecutor(cfg, nil)
	_, err := executor.Execute(context.Background(), pluginapi.ExecutorRequest{
		AuthProvider:   Provider,
		AuthAttributes: map[string]string{"api_key": "user_selected"},
		Model:          "deepseek/deepseek-v4.1-flash",
		Payload:        []byte(`{"model":"deepseek/deepseek-v4.1-flash","messages":[]}`),
		HTTPClient:     client,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(client.requests) != 1 || client.requests[0].Headers.Get("Authorization") != "Bearer user_selected" {
		t.Fatalf("requests = %+v", client.requests)
	}
}

func TestExecutorRejectsMissingHostCredential(t *testing.T) {
	executor := NewExecutor(parseConfig([]byte("api_key: user_config\n")), nil)
	_, err := executor.Execute(context.Background(), pluginapi.ExecutorRequest{
		AuthProvider: Provider,
		Payload:      []byte(`{"model":"deepseek-flash"}`),
		HTTPClient:   &recordingHTTPClient{},
	})
	if err == nil {
		t.Fatal("expected missing selected credential error")
	}
}

type recordingHTTPClient struct {
	requests []pluginapi.HTTPRequest
	response pluginapi.HTTPResponse
}

func (c *recordingHTTPClient) Do(_ context.Context, req pluginapi.HTTPRequest) (pluginapi.HTTPResponse, error) {
	copyReq := req
	copyReq.Body = append([]byte(nil), req.Body...)
	copyReq.Headers = req.Headers.Clone()
	c.requests = append(c.requests, copyReq)
	return c.response, nil
}

func (c *recordingHTTPClient) DoStream(_ context.Context, req pluginapi.HTTPRequest) (pluginapi.HTTPStreamResponse, error) {
	copyReq := req
	copyReq.Body = append([]byte(nil), req.Body...)
	copyReq.Headers = req.Headers.Clone()
	c.requests = append(c.requests, copyReq)
	chunks := make(chan pluginapi.HTTPStreamChunk)
	close(chunks)
	return pluginapi.HTTPStreamResponse{StatusCode: http.StatusOK, Chunks: chunks}, nil
}

func TestModelProviderAdvertisesClientAlias(t *testing.T) {
	cfg := parseConfig([]byte(`models:
  - alias: deepseek-flash
    name: deepseek/deepseek-v4.1-flash
`))
	resp, err := NewModelProvider(cfg).ModelsForAuth(context.Background(), pluginapi.AuthModelRequest{})
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(resp)
	if len(resp.Models) != 1 || resp.Models[0].ID != "deepseek-flash" {
		t.Fatalf("models = %s", encoded)
	}
}
