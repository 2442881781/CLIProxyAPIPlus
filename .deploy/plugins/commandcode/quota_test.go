package plugin

import (
	"context"
	"net/http"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func TestCommandCodeQuotaProviderNormalizesWindows(t *testing.T) {
	client := &recordingHTTPClient{response: pluginapi.HTTPResponse{
		StatusCode: http.StatusOK,
		Body:       []byte(`{"credits":{"monthlyCredits":69.99},"windowLimits":{"fiveHour":{"used":2,"cap":10,"resetAt":1800000000000},"weekly":{"used":25,"cap":100}}}`),
	}}
	resp, err := (quotaProvider{}).FetchQuota(context.Background(), pluginapi.QuotaFetchRequest{
		Attributes: map[string]string{"api_key": "user_selected"}, HTTPClient: client,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(client.requests) != 1 || client.requests[0].Headers.Get("Authorization") != "Bearer user_selected" {
		t.Fatalf("requests = %+v", client.requests)
	}
	if len(resp.Groups) != 1 || len(resp.Groups[0].Buckets) != 3 {
		t.Fatalf("quota = %+v", resp)
	}
	if got := resp.Groups[0].Buckets[0].RemainingFraction; got != 0.8 {
		t.Fatalf("five-hour remaining = %v", got)
	}
	if got := resp.Groups[0].Buckets[1].RemainingFraction; got != 0.75 {
		t.Fatalf("weekly remaining = %v", got)
	}
	monthly := resp.Groups[0].Buckets[2]
	if monthly.Window != "monthly-credits" {
		t.Fatalf("monthly bucket window = %q", monthly.Window)
	}
	if got := monthly.RemainingFraction; got <= 0.999 || got > 1 {
		t.Fatalf("monthly remaining = %v, want balance/70 clamped into (0.999,1]", got)
	}
}

func TestCommandCodeQuotaProviderDepletedBalanceReportsZero(t *testing.T) {
	client := &recordingHTTPClient{response: pluginapi.HTTPResponse{
		StatusCode: http.StatusOK,
		Body:       []byte(`{"credits":{"monthlyCredits":0.106,"purchasedCredits":0,"freeCredits":0},"windowLimits":{"fiveHour":{"used":5.36,"cap":14},"weekly":{"used":20.9,"cap":35}}}`),
	}}
	resp, err := (quotaProvider{}).FetchQuota(context.Background(), pluginapi.QuotaFetchRequest{
		Attributes: map[string]string{"api_key": "user_selected"}, HTTPClient: client,
	})
	if err != nil {
		t.Fatal(err)
	}
	var monthly *pluginapi.QuotaBucket
	for i := range resp.Groups[0].Buckets {
		if resp.Groups[0].Buckets[i].Window == "monthly-credits" {
			monthly = &resp.Groups[0].Buckets[i]
		}
	}
	if monthly == nil {
		t.Fatalf("monthly-credits bucket missing: %+v", resp)
	}
	if got := monthly.RemainingFraction; got >= 0.01 {
		t.Fatalf("depleted balance remaining = %v, want ~0", got)
	}
	if monthly.Description != "Monthly credits exhausted" {
		t.Fatalf("depleted description = %q", monthly.Description)
	}
}
