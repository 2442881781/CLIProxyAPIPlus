package cliproxy

import (
	"context"
	"testing"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func TestJitterProviderQuotaIntervalStaysBelowRoutingTTL(t *testing.T) {
	for range 1000 {
		interval := jitterProviderQuotaInterval()
		if interval < providerQuotaRefreshInterval-providerQuotaRefreshJitter || interval > providerQuotaRefreshInterval+providerQuotaRefreshJitter {
			t.Fatalf("interval = %s, outside configured jitter range", interval)
		}
		if interval >= 15*time.Minute {
			t.Fatalf("interval = %s, must remain below routing observation TTL", interval)
		}
	}
}

func TestMergeProviderQuotaSnapshotPreservesOtherFreshCredentialSources(t *testing.T) {
	now := time.Now().UTC()
	manager := coreauth.NewManager(nil, nil, nil)
	if errObserve := manager.ObserveProviderQuota(context.Background(), coreauth.ProviderQuotaSnapshot{
		Provider: "opencode-go", RemainingPercent: 80, ObservedAt: now,
		Sources: []coreauth.ProviderQuotaSourceSnapshot{
			{ID: "config-source", RemainingPercent: 20, ObservedAt: now},
			{ID: "auth-source", RemainingPercent: 80, ObservedAt: now},
		},
	}); errObserve != nil {
		t.Fatalf("ObserveProviderQuota(): %v", errObserve)
	}
	service := &Service{coreManager: manager}
	merged := service.mergeProviderQuotaSnapshot(coreauth.ProviderQuotaSnapshot{
		Provider: "opencode-go", RemainingPercent: 20, ObservedAt: now,
		Sources: []coreauth.ProviderQuotaSourceSnapshot{{ID: "config-source", RemainingPercent: 20, ObservedAt: now}},
	})
	if merged.RemainingPercent != 80 || len(merged.Sources) != 2 {
		t.Fatalf("merged = %+v, want auth source retained at 80%%", merged)
	}
}
