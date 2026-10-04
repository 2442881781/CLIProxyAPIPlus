package plugin

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

const commandCodeQuotaURL = "https://api.commandcode.ai/alpha/billing/credits"

type quotaProvider struct{}

var _ pluginapi.QuotaProvider = quotaProvider{}

func (quotaProvider) Identifier() string { return Provider }

func (quotaProvider) DescribeQuota(context.Context, pluginapi.QuotaDescribeRequest) (pluginapi.QuotaDescribeResponse, error) {
	return pluginapi.QuotaDescribeResponse{
		SupportedProviders: []string{Provider}, DisplayName: "CommandCode quota", SupportsReset: false,
	}, nil
}

func (quotaProvider) FetchQuota(ctx context.Context, req pluginapi.QuotaFetchRequest) (pluginapi.QuotaFetchResponse, error) {
	key := strings.TrimSpace(req.Attributes["api_key"])
	if key == "" && req.Metadata != nil {
		if value, ok := req.Metadata["api_key"].(string); ok {
			key = strings.TrimSpace(value)
		}
	}
	if key == "" {
		return pluginapi.QuotaFetchResponse{}, fmt.Errorf("commandcode quota: credential has no api key")
	}
	if req.HTTPClient == nil {
		return pluginapi.QuotaFetchResponse{}, fmt.Errorf("commandcode quota: host HTTP client is required")
	}
	resp, err := req.HTTPClient.Do(ctx, pluginapi.HTTPRequest{
		Method: http.MethodGet, URL: commandCodeQuotaURL,
		Headers: http.Header{"Authorization": []string{"Bearer " + key}, "Accept": []string{"application/json"}},
	})
	if err != nil || resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return pluginapi.QuotaFetchResponse{}, fmt.Errorf("commandcode quota request failed")
	}
	var payload map[string]any
	if err := json.Unmarshal(resp.Body, &payload); err != nil {
		return pluginapi.QuotaFetchResponse{}, fmt.Errorf("commandcode quota response invalid")
	}
	return normalizeCommandCodeQuota(payload)
}

func (quotaProvider) ResetQuota(context.Context, pluginapi.QuotaResetRequest) (pluginapi.QuotaResetResponse, error) {
	return pluginapi.QuotaResetResponse{Success: false, Message: "CommandCode does not support quota reset"}, nil
}

func normalizeCommandCodeQuota(payload map[string]any) (pluginapi.QuotaFetchResponse, error) {
	data := objectValue(payload["data"])
	if data == nil {
		data = payload
	}
	windowsRoot := objectValue(data["windowLimits"])
	if windowsRoot == nil {
		windowsRoot = objectValue(data["window_limits"])
	}
	if windowsRoot == nil {
		windowsRoot = data
	}
	buckets := make([]pluginapi.QuotaBucket, 0, 2)
	appendWindow := func(window, description string, names ...string) {
		var raw map[string]any
		for _, name := range names {
			if value := objectValue(windowsRoot[name]); value != nil {
				raw = value
				break
			}
		}
		if raw == nil {
			return
		}
		used, hasUsed := numberValue(firstValue(raw, "used", "current", "usage"))
		limit, hasLimit := numberValue(firstValue(raw, "limit", "total", "cap"))
		percent, hasPercent := numberValue(firstValue(raw, "percent", "percentage", "used_percent"))
		if !hasPercent && hasUsed && hasLimit && limit > 0 {
			percent, hasPercent = used/limit*100, true
		}
		if !hasPercent || math.IsNaN(percent) || math.IsInf(percent, 0) {
			return
		}
		bucket := pluginapi.QuotaBucket{Window: window, RemainingFraction: clampFraction(1 - percent/100), Description: description}
		if reset, ok := stringValue(firstValue(raw, "resetAt", "reset_at")); ok {
			bucket.ResetTime = reset
		} else if resetNumber, ok := numberValue(firstValue(raw, "resetAt", "reset_at")); ok {
			bucket.ResetTime = fmt.Sprintf("%.0f", resetNumber)
		}
		buckets = append(buckets, bucket)
	}
	appendWindow("five-hour", "Five-hour usage", "fiveHour", "five_hour", "five_hour_limit")
	appendWindow("weekly", "Weekly usage", "weekly", "weekly_limit")
	credits := objectValue(data["credits"])
	if credits == nil {
		credits = data
	}
	if balance, ok := numberValue(firstValue(credits, "monthlyCredits", "monthly_credits", "remainingCredits", "remaining_credits")); ok {
		buckets = append(buckets, pluginapi.QuotaBucket{Window: "monthly-credits", RemainingFraction: 1, Description: fmt.Sprintf("Monthly credits remaining: %.2f USD", balance)})
	}
	if len(buckets) == 0 {
		return pluginapi.QuotaFetchResponse{}, fmt.Errorf("commandcode quota response has no quota data")
	}
	return pluginapi.QuotaFetchResponse{Groups: []pluginapi.QuotaGroup{{DisplayName: "CommandCode", Buckets: buckets}}}, nil
}

func objectValue(value any) map[string]any {
	object, _ := value.(map[string]any)
	return object
}

func firstValue(object map[string]any, keys ...string) any {
	for _, key := range keys {
		if value, ok := object[key]; ok {
			return value
		}
	}
	return nil
}

func numberValue(value any) (float64, bool) {
	switch typed := value.(type) {
	case float64:
		return typed, true
	case float32:
		return float64(typed), true
	case int:
		return float64(typed), true
	case int64:
		return float64(typed), true
	case json.Number:
		parsed, err := typed.Float64()
		return parsed, err == nil
	case string:
		var parsed json.Number = json.Number(strings.TrimSpace(typed))
		value, err := parsed.Float64()
		return value, err == nil
	default:
		return 0, false
	}
}

func stringValue(value any) (string, bool) {
	text, ok := value.(string)
	text = strings.TrimSpace(text)
	return text, ok && text != ""
}

func clampFraction(value float64) float64 {
	if value < 0 {
		return 0
	}
	if value > 1 {
		return 1
	}
	return value
}
