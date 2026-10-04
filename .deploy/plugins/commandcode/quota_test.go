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
}
