package plugin

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"

	"opencode-go-cliproxyapi/internal/config"
)

func TestStandardQuotaProviderFetchesSelectedCredential(t *testing.T) {
	client := &quotaTestHTTPClient{response: pluginapi.HTTPResponse{
		StatusCode: http.StatusOK,
		Body:       []byte(`{"usage":{"rolling":{"status":"ok","percent":9,"resetsAt":"2026-09-05T12:20:04Z"},"weekly":{"status":"ok","percent":12},"monthly":{"status":"ok","percent":6}}}`),
	}}
	manager := &Manager{cfg: config.Config{BaseURL: "https://opencode.example/v1"}}
	resp, err := (standardQuotaProvider{manager: manager}).FetchQuota(context.Background(), pluginapi.QuotaFetchRequest{
		Attributes: map[string]string{"api_key": "selected-key"}, HTTPClient: client,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(client.requests) != 1 || client.requests[0].URL != "https://opencode.example/v1/usage" || client.requests[0].Headers.Get("Authorization") != "Bearer selected-key" {
		t.Fatalf("requests = %+v", client.requests)
	}
	if len(resp.Groups) != 1 || len(resp.Groups[0].Buckets) != 3 {
		t.Fatalf("quota = %+v", resp)
	}
	want := []float64{0.91, 0.88, 0.94}
	for index, bucket := range resp.Groups[0].Buckets {
		if bucket.RemainingFraction != want[index] {
			t.Fatalf("bucket %d = %+v", index, bucket)
		}
	}
}

type quotaTestHTTPClient struct {
	requests []pluginapi.HTTPRequest
	response pluginapi.HTTPResponse
}

func (c *quotaTestHTTPClient) Do(_ context.Context, req pluginapi.HTTPRequest) (pluginapi.HTTPResponse, error) {
	req.Headers = req.Headers.Clone()
	c.requests = append(c.requests, req)
	return c.response, nil
}

func (*quotaTestHTTPClient) DoStream(context.Context, pluginapi.HTTPRequest) (pluginapi.HTTPStreamResponse, error) {
	return pluginapi.HTTPStreamResponse{}, nil
}

func TestQuotaDispatchCarriesHostCallbackScope(t *testing.T) {
	var callbackID string
	bridge := NewHostBridge(func(method string, payload []byte) ([]byte, error) {
		if method != pluginabi.MethodHostHTTPDo {
			return hostOK(map[string]any{}), nil
		}
		var req hostHTTPReq
		if err := json.Unmarshal(payload, &req); err != nil {
			t.Fatal(err)
		}
		callbackID = req.HostCallbackID
		return hostOK(pluginapi.HTTPResponse{StatusCode: http.StatusOK, Body: []byte(`{"usage":{"rolling":{"percent":1},"weekly":{"percent":2},"monthly":{"percent":3}}}`)}), nil
	})
	manager := NewManager(bridge)
	manager.cfg = config.Config{BaseURL: "https://opencode.example/v1"}
	raw, err := manager.HandleCall(pluginabi.MethodQuotaFetch, mustJSON(struct {
		pluginapi.QuotaFetchRequest
		HostCallbackID string `json:"host_callback_id"`
	}{QuotaFetchRequest: pluginapi.QuotaFetchRequest{Attributes: map[string]string{"api_key": "selected"}}, HostCallbackID: "quota-scope"}))
	if err != nil || !decodeEnv(t, raw).OK {
		t.Fatalf("quota dispatch = %s, err=%v", raw, err)
	}
	if callbackID != "quota-scope" {
		t.Fatalf("callback ID = %q, want quota-scope", callbackID)
	}
}
