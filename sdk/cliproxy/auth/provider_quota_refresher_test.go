package auth

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

type testProviderQuotaFetcher struct {
	responses map[string]pluginapi.QuotaFetchResponse
	failures  map[string]error
}

func (f testProviderQuotaFetcher) FetchCredentialQuota(_ context.Context, auth *Auth) (pluginapi.QuotaFetchResponse, bool, error) {
	if errFetch := f.failures[auth.ID]; errFetch != nil {
		return pluginapi.QuotaFetchResponse{}, true, errFetch
	}
	quota, ok := f.responses[auth.ID]
	return quota, ok, nil
}

func TestRefreshCredentialProviderQuotasKeepsLastSuccessOnFailure(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	for _, auth := range []*Auth{
		{ID: "success", Provider: "devin", Status: StatusActive},
		{ID: "failure", Provider: "codex", Status: StatusActive},
	} {
		if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
			t.Fatalf("Register(%s): %v", auth.ID, errRegister)
		}
	}
	previous, _ := manager.GetByID("failure")
	previous.Quota.Signals = map[string]string{"normalized_quota_remaining_percent": "70"}
	previous.Quota.ObservedAt = previous.UpdatedAt
	if _, errUpdate := manager.Update(context.Background(), previous); errUpdate != nil {
		t.Fatalf("Update(): %v", errUpdate)
	}

	failures := manager.RefreshCredentialProviderQuotas(context.Background(), testProviderQuotaFetcher{
		responses: map[string]pluginapi.QuotaFetchResponse{
			"success": {Groups: []pluginapi.QuotaGroup{{Buckets: []pluginapi.QuotaBucket{{RemainingFraction: 0.45}}}}},
		},
		failures: map[string]error{"failure": errors.New("temporary quota endpoint failure")},
	}, func(quota pluginapi.QuotaFetchResponse) (float64, bool) {
		if len(quota.Groups) == 0 || len(quota.Groups[0].Buckets) == 0 {
			return 0, false
		}
		return quota.Groups[0].Buckets[0].RemainingFraction * 100, true
	}, ProviderQuotaRefreshOptions{MaxConcurrent: 2})
	if failures["failure"] == nil {
		t.Fatalf("failures = %#v, want failure auth error", failures)
	}
	success, _ := manager.GetByID("success")
	if success.Quota.Signals["normalized_quota_remaining_percent"] != "45" {
		t.Fatalf("success quota = %#v", success.Quota)
	}
	failure, _ := manager.GetByID("failure")
	if failure.Quota.Signals["normalized_quota_remaining_percent"] != "70" {
		t.Fatalf("failed refresh replaced last success: %#v", failure.Quota)
	}
}

type concurrencyQuotaFetcher struct {
	mu     sync.Mutex
	active int
	max    int
}

func (f *concurrencyQuotaFetcher) FetchCredentialQuota(ctx context.Context, _ *Auth) (pluginapi.QuotaFetchResponse, bool, error) {
	f.mu.Lock()
	f.active++
	if f.active > f.max {
		f.max = f.active
	}
	f.mu.Unlock()
	select {
	case <-time.After(10 * time.Millisecond):
	case <-ctx.Done():
	}
	f.mu.Lock()
	f.active--
	f.mu.Unlock()
	return pluginapi.QuotaFetchResponse{Groups: []pluginapi.QuotaGroup{{Buckets: []pluginapi.QuotaBucket{{RemainingFraction: 0.5}}}}}, true, nil
}

func TestRefreshCredentialProviderQuotasBoundsConcurrency(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	for i := range 8 {
		if _, errRegister := manager.Register(context.Background(), &Auth{ID: fmt.Sprintf("auth-%d", i), Provider: "devin", Status: StatusActive}); errRegister != nil {
			t.Fatalf("Register(): %v", errRegister)
		}
	}
	fetcher := &concurrencyQuotaFetcher{}
	manager.RefreshCredentialProviderQuotas(context.Background(), fetcher, func(pluginapi.QuotaFetchResponse) (float64, bool) { return 50, true }, ProviderQuotaRefreshOptions{Timeout: time.Second, MaxConcurrent: 3})
	if fetcher.max > 3 {
		t.Fatalf("max concurrency = %d, want <= 3", fetcher.max)
	}
}
