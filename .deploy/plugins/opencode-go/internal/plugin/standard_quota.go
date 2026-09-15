package plugin

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

type standardQuotaProvider struct {
	manager *Manager
}

var _ pluginapi.QuotaProvider = standardQuotaProvider{}

func (standardQuotaProvider) Identifier() string { return ProviderID }

func (standardQuotaProvider) DescribeQuota(context.Context, pluginapi.QuotaDescribeRequest) (pluginapi.QuotaDescribeResponse, error) {
	return pluginapi.QuotaDescribeResponse{
		SupportedProviders: []string{ProviderID}, DisplayName: "OpenCode Go quota", SupportsReset: false,
	}, nil
}

func (p standardQuotaProvider) FetchQuota(ctx context.Context, req pluginapi.QuotaFetchRequest) (pluginapi.QuotaFetchResponse, error) {
	key := strings.TrimSpace(req.Attributes["api_key"])
	if key == "" && req.Metadata != nil {
		if value, ok := req.Metadata["api_key"].(string); ok {
			key = strings.TrimSpace(value)
		}
	}
	if key == "" {
		return pluginapi.QuotaFetchResponse{}, fmt.Errorf("opencode-go quota: credential has no api key")
	}
	baseURL := ""
	if p.manager != nil {
		p.manager.mu.RLock()
		baseURL = p.manager.cfg.BaseURL
		p.manager.mu.RUnlock()
	}
	if baseURL == "" {
		return pluginapi.QuotaFetchResponse{}, fmt.Errorf("opencode-go quota: configuration is unavailable")
	}
	if req.HTTPClient == nil {
		return pluginapi.QuotaFetchResponse{}, fmt.Errorf("opencode-go quota: host HTTP client is required")
	}
	resp, err := req.HTTPClient.Do(ctx, pluginapi.HTTPRequest{
		Method: http.MethodGet, URL: strings.TrimRight(baseURL, "/") + "/usage",
		Headers: http.Header{"Authorization": []string{"Bearer " + key}, "Accept": []string{"application/json"}},
	})
	if err != nil || resp.StatusCode != http.StatusOK {
		return pluginapi.QuotaFetchResponse{}, fmt.Errorf("opencode-go quota request failed")
	}
	usage, err := decodeQuotaUsage(resp.Body)
	if err != nil {
		return pluginapi.QuotaFetchResponse{}, err
	}
	buckets := make([]pluginapi.QuotaBucket, 0, 3)
	for _, candidate := range []struct {
		name   string
		window quotaWindow
	}{{"rolling", usage.Rolling}, {"weekly", usage.Weekly}, {"monthly", usage.Monthly}} {
		if candidate.window.Status == "" && candidate.window.Percent == 0 && candidate.window.ResetsAt == "" {
			continue
		}
		buckets = append(buckets, quotaBucket(candidate.name, candidate.window))
	}
	if len(buckets) == 0 {
		return pluginapi.QuotaFetchResponse{}, fmt.Errorf("opencode-go quota response has no quota data")
	}
	return pluginapi.QuotaFetchResponse{Groups: []pluginapi.QuotaGroup{{DisplayName: "OpenCode Go", Buckets: buckets}}}, nil
}

func (standardQuotaProvider) ResetQuota(context.Context, pluginapi.QuotaResetRequest) (pluginapi.QuotaResetResponse, error) {
	return pluginapi.QuotaResetResponse{Success: false, Message: "OpenCode Go does not support quota reset"}, nil
}

func decodeQuotaUsage(body []byte) (quotaUsage, error) {
	var decoded usageResponse
	if err := json.Unmarshal(body, &decoded); err != nil {
		return quotaUsage{}, fmt.Errorf("quota response invalid")
	}
	return quotaUsage{
		Rolling: quotaWindow{Status: decoded.Usage.Rolling.Status, Percent: decoded.Usage.Rolling.Percent, ResetsAt: decoded.Usage.Rolling.ResetsAt},
		Weekly:  quotaWindow{Status: decoded.Usage.Weekly.Status, Percent: decoded.Usage.Weekly.Percent, ResetsAt: decoded.Usage.Weekly.ResetsAt},
		Monthly: quotaWindow{Status: decoded.Usage.Monthly.Status, Percent: decoded.Usage.Monthly.Percent, ResetsAt: decoded.Usage.Monthly.ResetsAt},
	}, nil
}

func quotaBucket(window string, value quotaWindow) pluginapi.QuotaBucket {
	remaining := 1 - float64(value.Percent)/100
	if remaining < 0 {
		remaining = 0
	}
	if remaining > 1 {
		remaining = 1
	}
	return pluginapi.QuotaBucket{Window: window, RemainingFraction: remaining, ResetTime: value.ResetsAt, Description: value.Status}
}

type quotaBridgeClient struct {
	bridge     *HostBridge
	callbackID string
}

func (c quotaBridgeClient) Do(ctx context.Context, req pluginapi.HTTPRequest) (pluginapi.HTTPResponse, error) {
	if c.bridge == nil {
		return pluginapi.HTTPResponse{}, fmt.Errorf("host bridge unavailable")
	}
	return c.bridge.DoWithCallbackID(ctx, c.callbackID, req)
}

func (c quotaBridgeClient) DoStream(context.Context, pluginapi.HTTPRequest) (pluginapi.HTTPStreamResponse, error) {
	return pluginapi.HTTPStreamResponse{}, fmt.Errorf("streaming quota request unsupported")
}
