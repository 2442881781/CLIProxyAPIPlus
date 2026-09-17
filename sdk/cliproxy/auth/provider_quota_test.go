package auth

import (
	"context"
	"testing"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

func TestProviderQuotaFallbackRanksMixedProviders(t *testing.T) {
	now := time.Now().UTC()
	manager := NewManager(nil, NewQuotaAwareSelector(&RoundRobinSelector{}), nil)
	manager.providerQuotas["commandcode"] = ProviderQuotaSnapshot{
		Provider: "commandcode", RemainingPercent: 8, ObservedAt: now,
	}
	manager.providerQuotas["opencode-go"] = ProviderQuotaSnapshot{
		Provider: "opencode-go", RemainingPercent: 78, ObservedAt: now,
	}
	auths := []*Auth{
		{ID: "commandcode-static", Provider: "commandcode", Status: StatusActive},
		{ID: "opencode-static", Provider: "opencode-go", Status: StatusActive},
	}
	manager.applyProviderQuotaFallback(auths, "deepseek-v4.1-flash", now)

	picked, errPick := manager.selector.Pick(context.Background(), "", "deepseek-v4.1-flash", cliproxyexecutor.Options{}, auths)
	if errPick != nil {
		t.Fatalf("Pick(): %v", errPick)
	}
	if picked.ID != "opencode-static" {
		t.Fatalf("picked = %q, want opencode-static", picked.ID)
	}
}

func TestProviderQuotaFallbackDoesNotOverrideAuthObservation(t *testing.T) {
	now := time.Now().UTC()
	manager := NewManager(nil, nil, nil)
	manager.providerQuotas["commandcode"] = ProviderQuotaSnapshot{
		Provider: "commandcode", RemainingPercent: 90, ObservedAt: now,
	}
	auth := &Auth{
		ID:       "credential-level",
		Provider: "commandcode",
		Status:   StatusActive,
		Quota: QuotaState{
			ObservedAt: now,
			Signals: map[string]string{
				"normalized_quota_remaining_percent": "12",
			},
		},
	}
	manager.applyProviderQuotaFallback([]*Auth{auth}, "model", now)
	observation := authQuotaRemainingPercent(auth, "model", now)
	if !observation.known || observation.percent != 12 {
		t.Fatalf("observation = %+v, want credential-level 12%%", observation)
	}
}

func TestProviderQuotaFallbackExpiresThroughExistingTTL(t *testing.T) {
	now := time.Now().UTC()
	manager := NewManager(nil, nil, nil)
	manager.providerQuotas["commandcode"] = ProviderQuotaSnapshot{
		Provider:         "commandcode",
		RemainingPercent: 90,
		ObservedAt:       now.Add(-quotaRoutingObservationTTL - time.Second),
	}
	auth := &Auth{ID: "static", Provider: "commandcode", Status: StatusActive}
	manager.applyProviderQuotaFallback([]*Auth{auth}, "model", now)
	if observation := authQuotaRemainingPercent(auth, "model", now); observation.known {
		t.Fatalf("observation = %+v, want stale provider snapshot to remain unknown", observation)
	}
}

func TestRecordProviderQuotaFailureRetainsLastSuccess(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	observedAt := time.Now().UTC().Add(-time.Minute)
	if errObserve := manager.ObserveProviderQuota(context.Background(), ProviderQuotaSnapshot{
		Provider: "commandcode", RemainingPercent: 55, ObservedAt: observedAt,
	}); errObserve != nil {
		t.Fatalf("ObserveProviderQuota(): %v", errObserve)
	}
	attemptedAt := time.Now().UTC()
	if errFailure := manager.RecordProviderQuotaFailure(context.Background(), "commandcode", attemptedAt, "temporary failure"); errFailure != nil {
		t.Fatalf("RecordProviderQuotaFailure(): %v", errFailure)
	}
	snapshot, ok := manager.ProviderQuota("commandcode")
	if !ok {
		t.Fatal("ProviderQuota() missing snapshot")
	}
	if snapshot.RemainingPercent != 55 || !snapshot.ObservedAt.Equal(observedAt) {
		t.Fatalf("snapshot = %+v, want retained successful observation", snapshot)
	}
	if snapshot.LastError != "temporary failure" || !snapshot.LastAttemptAt.Equal(attemptedAt) {
		t.Fatalf("snapshot failure metadata = %+v", snapshot)
	}
}

func TestProviderQuotaFallbackUsesCredentialSourceWhenAvailable(t *testing.T) {
	now := time.Now().UTC()
	manager := NewManager(nil, NewQuotaAwareSelector(&RoundRobinSelector{}), nil)
	manager.providerQuotas["opencode-go"] = ProviderQuotaSnapshot{
		Provider:         "opencode-go",
		RemainingPercent: 90,
		ObservedAt:       now,
		Sources: []ProviderQuotaSourceSnapshot{
			{ID: ProviderQuotaSourceID("opencode-go", "key-low"), RemainingPercent: 15, ObservedAt: now},
			{ID: ProviderQuotaSourceID("opencode-go", "key-high"), RemainingPercent: 85, ObservedAt: now},
		},
	}
	auths := []*Auth{
		{ID: "low", Provider: "opencode-go", Status: StatusActive, Attributes: map[string]string{"api_key": "key-low"}},
		{ID: "high", Provider: "opencode-go", Status: StatusActive, Attributes: map[string]string{"api_key": "key-high"}},
	}
	manager.applyProviderQuotaFallback(auths, "model", now)
	picked, errPick := manager.selector.Pick(context.Background(), "opencode-go", "model", cliproxyexecutor.Options{}, auths)
	if errPick != nil {
		t.Fatalf("Pick(): %v", errPick)
	}
	if picked.ID != "high" {
		t.Fatalf("picked = %q, want high source-specific quota", picked.ID)
	}
}

func TestObserveProviderCredentialQuotaPersistsSharedSnapshot(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	auth := &Auth{
		ID: "credential", Provider: "devin", Status: StatusActive,
		Attributes: map[string]string{"api_key": "secret"},
	}
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("Register(): %v", errRegister)
	}
	now := time.Now().UTC()
	if _, errObserve := manager.ObserveProviderCredentialQuota(context.Background(), auth.ID, now, map[string]string{
		"normalized_quota_remaining_percent": "42",
	}); errObserve != nil {
		t.Fatalf("ObserveProviderCredentialQuota(): %v", errObserve)
	}
	snapshot, ok := manager.ProviderQuota("devin")
	if !ok || snapshot.RemainingPercent != 42 || len(snapshot.Sources) != 1 {
		t.Fatalf("ProviderQuota() = %+v, %v", snapshot, ok)
	}
	if snapshot.Sources[0].ID == "secret" || snapshot.Sources[0].RemainingPercent != 42 {
		t.Fatalf("source snapshot leaked or lost quota: %+v", snapshot.Sources[0])
	}
}

func TestProviderQuotaFallbackKeepsUnmatchedCredentialUnknown(t *testing.T) {
	now := time.Now().UTC()
	manager := NewManager(nil, nil, nil)
	manager.providerQuotas["opencode-go"] = ProviderQuotaSnapshot{
		Provider: "opencode-go", RemainingPercent: 85, ObservedAt: now,
		Sources: []ProviderQuotaSourceSnapshot{{
			ID: ProviderQuotaSourceID("opencode-go", "known-key"), RemainingPercent: 85, ObservedAt: now,
		}},
	}
	auth := &Auth{ID: "new-key", Provider: "opencode-go", Status: StatusActive, Attributes: map[string]string{"api_key": "different-key"}}
	manager.applyProviderQuotaFallback([]*Auth{auth}, "model", now)
	if observation := authQuotaRemainingPercent(auth, "model", now); observation.known {
		t.Fatalf("observation = %+v, want unmatched credential to stay unknown", observation)
	}
}

func TestProviderQuotaFallbackSessionAffinityMigratesBelowThreshold(t *testing.T) {
	now := time.Now().UTC()
	selector := NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{
		Fallback: &RoundRobinSelector{}, TTL: time.Hour, QuotaAware: true,
	})
	defer selector.Stop()
	manager := NewManager(nil, selector, nil)
	auths := []*Auth{
		{ID: "commandcode", Provider: "commandcode", Status: StatusActive},
		{ID: "opencode", Provider: "opencode-go", Status: StatusActive},
	}
	manager.providerQuotas["commandcode"] = ProviderQuotaSnapshot{Provider: "commandcode", RemainingPercent: 50, ObservedAt: now}
	manager.providerQuotas["opencode-go"] = ProviderQuotaSnapshot{Provider: "opencode-go", RemainingPercent: 40, ObservedAt: now}
	manager.applyProviderQuotaFallback(auths, "model", now)
	opts := cliproxyexecutor.Options{Metadata: map[string]any{cliproxyexecutor.DerivedSessionIDMetadataKey: "provider-quota-session"}}
	first, errFirst := selector.Pick(context.Background(), "mixed", "model", opts, auths)
	if errFirst != nil || first.ID != "commandcode" {
		t.Fatalf("first = %#v, err=%v", first, errFirst)
	}
	manager.providerQuotas["commandcode"] = ProviderQuotaSnapshot{Provider: "commandcode", RemainingPercent: 5, ObservedAt: time.Now().UTC()}
	manager.providerQuotas["opencode-go"] = ProviderQuotaSnapshot{Provider: "opencode-go", RemainingPercent: 60, ObservedAt: time.Now().UTC()}
	auths = []*Auth{
		{ID: "commandcode", Provider: "commandcode", Status: StatusActive},
		{ID: "opencode", Provider: "opencode-go", Status: StatusActive},
	}
	manager.applyProviderQuotaFallback(auths, "model", time.Now().UTC())
	second, errSecond := selector.Pick(context.Background(), "mixed", "model", opts, auths)
	if errSecond != nil || second.ID != "opencode" {
		t.Fatalf("second = %#v, err=%v, want migration to opencode", second, errSecond)
	}
}
