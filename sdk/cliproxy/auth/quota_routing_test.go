package auth

import (
	"context"
	"strconv"
	"testing"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

func TestQuotaStateRemainingPercent(t *testing.T) {
	now := time.Now()
	tests := []struct {
		name     string
		provider string
		signals  map[string]string
		want     float64
		known    bool
	}{
		{
			name:     "claude uses most consumed window",
			provider: "claude",
			signals: map[string]string{
				"Anthropic-Ratelimit-Unified-5h-Utilization": "0.20",
				"Anthropic-Ratelimit-Unified-7d-Utilization": "0.75",
			},
			want:  25,
			known: true,
		},
		{
			name:     "codex uses most consumed base window",
			provider: "codex",
			signals: map[string]string{
				"X-Codex-Primary-Used-Percent":   "35",
				"X-Codex-Secondary-Used-Percent": "60",
			},
			want:  40,
			known: true,
		},
		{
			name:     "codex includes active additional limit",
			provider: "codex",
			signals: map[string]string{
				"X-Codex-Active-Limit":                                "gpt-5.3-codex-spark",
				"X-Codex-Primary-Used-Percent":                        "10",
				"X-Codex-Additional-GPT-5.3-Codex-Spark-Limit-Name":   "GPT-5.3-Codex-Spark",
				"X-Codex-Additional-GPT-5.3-Codex-Spark-Used-Percent": "92",
			},
			want:  8,
			known: true,
		},
		{
			name:     "codex maps active short name through limit name",
			provider: "codex",
			signals: map[string]string{
				"X-Codex-Active-Limit":                     "codex_bengalfox",
				"X-Codex-Primary-Used-Percent":             "10",
				"X-Codex-Bengalfox-Limit-Name":             "GPT-5.3-Codex-Spark",
				"X-Codex-Bengalfox-Primary-Used-Percent":   "62",
				"X-Codex-Bengalfox-Secondary-Used-Percent": "75",
			},
			want:  25,
			known: true,
		},
		{
			name:     "devin uses limiting window",
			provider: "devin",
			signals: map[string]string{
				"daily_quota_remaining_percent":  "80%",
				"weekly_quota_remaining_percent": "45%",
			},
			want:  45,
			known: true,
		},
		{
			name:     "unknown provider",
			provider: "gemini",
			signals:  map[string]string{"remaining": "90"},
			known:    false,
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			observation := quotaStateRemainingPercent(testCase.provider, QuotaState{
				ObservedAt: now,
				Signals:    testCase.signals,
			}, now)
			if observation.known != testCase.known {
				t.Fatalf("known = %v, want %v", observation.known, testCase.known)
			}
			if testCase.known && observation.percent != testCase.want {
				t.Fatalf("percent = %v, want %v", observation.percent, testCase.want)
			}
		})
	}
}

func TestAuthQuotaRemainingPercentUsesNewestMatchingModelState(t *testing.T) {
	now := time.Now()
	auth := quotaRoutingTestAuth("model-observation", "codex", 0, now.Add(-time.Minute), map[string]string{
		"X-Codex-Primary-Used-Percent": "50",
	})
	auth.ModelStates = map[string]*ModelState{
		"gpt-5(high)": {
			Quota: QuotaState{
				ObservedAt: now.Add(-2 * time.Minute),
				Signals:    map[string]string{"X-Codex-Primary-Used-Percent": "90"},
			},
		},
		"gpt-5": {
			Quota: QuotaState{
				ObservedAt: now,
				Signals:    map[string]string{"X-Codex-Primary-Used-Percent": "20"},
			},
		},
	}

	observation := authQuotaRemainingPercent(auth, "gpt-5(medium)", now)
	if !observation.known || observation.percent != 80 {
		t.Fatalf("observation = %+v, want freshest model observation at 80%% remaining", observation)
	}
}

func TestAuthQuotaRemainingPercentFallsBackWhenMatchingModelObservationStale(t *testing.T) {
	now := time.Now()
	auth := quotaRoutingTestAuth("model-fallback", "codex", 0, now, map[string]string{
		"X-Codex-Primary-Used-Percent": "30",
	})
	auth.ModelStates = map[string]*ModelState{
		"gpt-5": {
			Quota: QuotaState{
				ObservedAt: now.Add(-quotaRoutingObservationTTL - time.Second),
				Signals:    map[string]string{"X-Codex-Primary-Used-Percent": "99"},
			},
		},
	}

	observation := authQuotaRemainingPercent(auth, "gpt-5", now)
	if !observation.known || observation.percent != 70 {
		t.Fatalf("observation = %+v, want credential fallback at 70%% remaining", observation)
	}
}

func TestQuotaStateRemainingPercentRejectsStaleFutureAndMalformedSignals(t *testing.T) {
	now := time.Now()
	for _, testCase := range []QuotaState{
		{ObservedAt: now.Add(-quotaRoutingObservationTTL - time.Second), Signals: map[string]string{"X-Codex-Primary-Used-Percent": "20"}},
		{ObservedAt: now.Add(quotaObservationFutureTolerance + time.Second), Signals: map[string]string{"X-Codex-Primary-Used-Percent": "20"}},
		{ObservedAt: now, Signals: map[string]string{"X-Codex-Primary-Used-Percent": "not-a-number"}},
	} {
		if observation := quotaStateRemainingPercent("codex", testCase, now); observation.known {
			t.Fatalf("observation = %+v, want unknown", observation)
		}
	}
}

func TestQuotaAwareSelectorPrefersHighestRemainingWithinHighestPriority(t *testing.T) {
	now := time.Now()
	selector := NewQuotaAwareSelector(&RoundRobinSelector{})
	auths := []*Auth{
		quotaRoutingTestAuth("low-priority-full", "codex", 0, now, map[string]string{"X-Codex-Primary-Used-Percent": "0"}),
		quotaRoutingTestAuth("high-priority-low", "codex", 1, now, map[string]string{"X-Codex-Primary-Used-Percent": "70"}),
		quotaRoutingTestAuth("high-priority-high", "codex", 1, now, map[string]string{"X-Codex-Primary-Used-Percent": "20"}),
	}

	picked, errPick := selector.Pick(context.Background(), "codex", "gpt-5", cliproxyexecutor.Options{}, auths)
	if errPick != nil {
		t.Fatalf("Pick(): %v", errPick)
	}
	if picked.ID != "high-priority-high" {
		t.Fatalf("picked = %q, want high-priority-high", picked.ID)
	}
}

func TestQuotaAwareSelectorFallsBackWhenQuotaUnknownOrStale(t *testing.T) {
	now := time.Now()
	selector := NewQuotaAwareSelector(&RoundRobinSelector{})
	known := quotaRoutingTestAuth("a-known", "codex", 0, now, map[string]string{"X-Codex-Primary-Used-Percent": "5"})
	unknown := &Auth{ID: "b-unknown", Provider: "codex", Status: StatusActive}

	first, errFirst := selector.Pick(context.Background(), "codex", "gpt-5", cliproxyexecutor.Options{}, []*Auth{known, unknown})
	if errFirst != nil {
		t.Fatalf("first Pick(): %v", errFirst)
	}
	second, errSecond := selector.Pick(context.Background(), "codex", "gpt-5", cliproxyexecutor.Options{}, []*Auth{known, unknown})
	if errSecond != nil {
		t.Fatalf("second Pick(): %v", errSecond)
	}
	if first.ID != known.ID || second.ID != unknown.ID {
		t.Fatalf("fallback rotation = %q, %q; want %q, %q", first.ID, second.ID, known.ID, unknown.ID)
	}
}

func TestSessionAffinityQuotaAwareMigratesBelowThreshold(t *testing.T) {
	now := time.Now()
	selector := NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{
		Fallback:   &RoundRobinSelector{},
		TTL:        time.Hour,
		QuotaAware: true,
	})
	defer selector.Stop()

	low := quotaRoutingTestAuth("a-low", "codex", 0, now, map[string]string{"X-Codex-Primary-Used-Percent": "50"})
	high := quotaRoutingTestAuth("b-high", "codex", 0, now, map[string]string{"X-Codex-Primary-Used-Percent": "60"})
	auths := []*Auth{low, high}
	opts := cliproxyexecutor.Options{Metadata: map[string]any{
		cliproxyexecutor.DerivedSessionIDMetadataKey: "quota-session",
	}}

	first, errFirst := selector.Pick(context.Background(), "codex", "gpt-5", opts, auths)
	if errFirst != nil {
		t.Fatalf("first Pick(): %v", errFirst)
	}
	if first.ID != low.ID {
		t.Fatalf("first = %q, want %q", first.ID, low.ID)
	}

	low.Quota.ObservedAt = time.Now()
	low.Quota.Signals["X-Codex-Primary-Used-Percent"] = "91"
	high.Quota.ObservedAt = time.Now()
	high.Quota.Signals["X-Codex-Primary-Used-Percent"] = "60"
	second, errSecond := selector.Pick(context.Background(), "codex", "gpt-5", opts, auths)
	if errSecond != nil {
		t.Fatalf("second Pick(): %v", errSecond)
	}
	if second.ID != high.ID {
		t.Fatalf("second = %q, want migration to %q", second.ID, high.ID)
	}

	third, errThird := selector.Pick(context.Background(), "codex", "gpt-5", opts, auths)
	if errThird != nil {
		t.Fatalf("third Pick(): %v", errThird)
	}
	if third.ID != high.ID {
		t.Fatalf("third = %q, want sticky %q", third.ID, high.ID)
	}
}

func TestSessionAffinityQuotaAwareKeepsLowAuthWithoutViableAlternative(t *testing.T) {
	now := time.Now()
	selector := NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{
		Fallback:   &RoundRobinSelector{},
		TTL:        time.Hour,
		QuotaAware: true,
	})
	defer selector.Stop()

	bound := quotaRoutingTestAuth("a-bound", "codex", 0, now, map[string]string{"X-Codex-Primary-Used-Percent": "80"})
	other := quotaRoutingTestAuth("b-other", "codex", 0, now, map[string]string{"X-Codex-Primary-Used-Percent": "95"})
	auths := []*Auth{bound, other}
	opts := cliproxyexecutor.Options{Metadata: map[string]any{
		cliproxyexecutor.DerivedSessionIDMetadataKey: "no-alternative-session",
	}}

	first, errFirst := selector.Pick(context.Background(), "codex", "gpt-5", opts, auths)
	if errFirst != nil || first.ID != bound.ID {
		t.Fatalf("first = %#v, err=%v", first, errFirst)
	}
	bound.Quota.ObservedAt = time.Now()
	bound.Quota.Signals["X-Codex-Primary-Used-Percent"] = "92"
	second, errSecond := selector.Pick(context.Background(), "codex", "gpt-5", opts, auths)
	if errSecond != nil {
		t.Fatalf("second Pick(): %v", errSecond)
	}
	if second.ID != bound.ID {
		t.Fatalf("second = %q, want bound auth %q when no viable alternative", second.ID, bound.ID)
	}
}

func TestSessionAffinityQuotaAwareDoesNotMigrateToUnknownQuota(t *testing.T) {
	now := time.Now()
	selector := NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{
		Fallback:   &RoundRobinSelector{},
		TTL:        time.Hour,
		QuotaAware: true,
	})
	defer selector.Stop()

	bound := quotaRoutingTestAuth("a-bound", "codex", 0, now, map[string]string{"X-Codex-Primary-Used-Percent": "50"})
	unknown := &Auth{ID: "b-unknown", Provider: "codex", Status: StatusActive}
	auths := []*Auth{bound, unknown}
	opts := cliproxyexecutor.Options{Metadata: map[string]any{
		cliproxyexecutor.DerivedSessionIDMetadataKey: "unknown-migration-session",
	}}

	first, errFirst := selector.Pick(context.Background(), "codex", "gpt-5", opts, auths)
	if errFirst != nil || first.ID != bound.ID {
		t.Fatalf("first = %#v, err=%v", first, errFirst)
	}
	bound.Quota.ObservedAt = time.Now()
	bound.Quota.Signals["X-Codex-Primary-Used-Percent"] = "92"
	second, errSecond := selector.Pick(context.Background(), "codex", "gpt-5", opts, auths)
	if errSecond != nil {
		t.Fatalf("second Pick(): %v", errSecond)
	}
	if second.ID != bound.ID {
		t.Fatalf("second = %q, want bound auth %q when alternative quota is unknown", second.ID, bound.ID)
	}
}

func TestSessionAffinityQuotaAwareDoesNotPreemptHealthyBinding(t *testing.T) {
	now := time.Now()
	selector := NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{
		Fallback:   &RoundRobinSelector{},
		TTL:        time.Hour,
		QuotaAware: true,
	})
	defer selector.Stop()

	bound := quotaRoutingTestAuth("a-bound", "codex", 0, now, map[string]string{"X-Codex-Primary-Used-Percent": "40"})
	other := quotaRoutingTestAuth("b-other", "codex", 0, now, map[string]string{"X-Codex-Primary-Used-Percent": "60"})
	opts := cliproxyexecutor.Options{Metadata: map[string]any{
		cliproxyexecutor.DerivedSessionIDMetadataKey: "healthy-binding-session",
	}}
	first, errFirst := selector.Pick(context.Background(), "codex", "gpt-5", opts, []*Auth{bound, other})
	if errFirst != nil || first.ID != bound.ID {
		t.Fatalf("first = %#v, err=%v", first, errFirst)
	}
	other.Quota.Signals["X-Codex-Primary-Used-Percent"] = "0"
	second, errSecond := selector.Pick(context.Background(), "codex", "gpt-5", opts, []*Auth{bound, other})
	if errSecond != nil {
		t.Fatalf("second Pick(): %v", errSecond)
	}
	if second.ID != bound.ID {
		t.Fatalf("healthy binding was preempted: got %q want %q", second.ID, bound.ID)
	}
}

func quotaRoutingTestAuth(id, provider string, priority int, observedAt time.Time, signals map[string]string) *Auth {
	return &Auth{
		ID:       id,
		Provider: provider,
		Status:   StatusActive,
		Attributes: map[string]string{
			"priority": strconv.Itoa(priority),
		},
		Quota: QuotaState{
			ObservedAt: observedAt,
			Signals:    signals,
		},
	}
}

func TestManagerQuotaAwareMixedProviderSelectsHighestRemainingAuth(t *testing.T) {
	now := time.Now()
	manager := NewManager(nil, NewQuotaAwareSelector(&RoundRobinSelector{}), nil)
	for _, provider := range []string{"codex", "devin"} {
		manager.RegisterExecutor(schedulerTestExecutor{provider: provider})
	}
	auths := []*Auth{
		quotaRoutingTestAuth("codex-low", "codex", 0, now, map[string]string{"X-Codex-Primary-Used-Percent": "70"}),
		quotaRoutingTestAuth("devin-high", "devin", 0, now, map[string]string{"daily_quota_remaining_percent": "90%", "weekly_quota_remaining_percent": "85%"}),
	}
	for _, auth := range auths {
		if _, errRegister := manager.Register(WithSkipPersist(context.Background()), auth); errRegister != nil {
			t.Fatalf("Register(%s): %v", auth.ID, errRegister)
		}
	}

	selected, _, provider, errPick := manager.pickNextMixedLegacy(context.Background(), []string{"codex", "devin"}, "", cliproxyexecutor.Options{}, nil)
	if errPick != nil {
		t.Fatalf("pickNextMixedLegacy(): %v", errPick)
	}
	if selected == nil || selected.ID != "devin-high" || provider != "devin" {
		t.Fatalf("selected=%#v provider=%q, want devin-high via devin", selected, provider)
	}
}

func TestManagerQuotaAwareMixedProviderSessionAffinityMigratesAcrossProviders(t *testing.T) {
	now := time.Now()
	selector := NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{
		Fallback:   &RoundRobinSelector{},
		TTL:        time.Hour,
		QuotaAware: true,
	})
	defer selector.Stop()
	manager := NewManager(nil, selector, nil)
	for _, provider := range []string{"codex", "devin"} {
		manager.RegisterExecutor(schedulerTestExecutor{provider: provider})
	}
	codex := quotaRoutingTestAuth("a-codex", "codex", 0, now, map[string]string{"X-Codex-Primary-Used-Percent": "40"})
	devin := quotaRoutingTestAuth("b-devin", "devin", 0, now, map[string]string{"daily_quota_remaining_percent": "50%", "weekly_quota_remaining_percent": "50%"})
	for _, auth := range []*Auth{codex, devin} {
		if _, errRegister := manager.Register(WithSkipPersist(context.Background()), auth); errRegister != nil {
			t.Fatalf("Register(%s): %v", auth.ID, errRegister)
		}
	}
	opts := cliproxyexecutor.Options{Metadata: map[string]any{
		cliproxyexecutor.DerivedSessionIDMetadataKey: "mixed-quota-session",
	}}

	first, _, firstProvider, errFirst := manager.pickNextMixedLegacy(context.Background(), []string{"codex", "devin"}, "", opts, nil)
	if errFirst != nil {
		t.Fatalf("first pickNextMixedLegacy(): %v", errFirst)
	}
	if first == nil || first.ID != codex.ID || firstProvider != "codex" {
		t.Fatalf("first=%#v provider=%q, want codex", first, firstProvider)
	}

	manager.mu.Lock()
	manager.auths[codex.ID].Quota.ObservedAt = time.Now()
	manager.auths[codex.ID].Quota.Signals["X-Codex-Primary-Used-Percent"] = "92"
	manager.auths[devin.ID].Quota.ObservedAt = time.Now()
	manager.mu.Unlock()

	second, _, secondProvider, errSecond := manager.pickNextMixedLegacy(context.Background(), []string{"codex", "devin"}, "", opts, nil)
	if errSecond != nil {
		t.Fatalf("second pickNextMixedLegacy(): %v", errSecond)
	}
	if second == nil || second.ID != devin.ID || secondProvider != "devin" {
		t.Fatalf("second=%#v provider=%q, want devin migration", second, secondProvider)
	}
}

func TestPreferredQuotaMigrationAuthsStaysWithinProviderFirst(t *testing.T) {
	now := time.Now()
	current := quotaRoutingTestAuth("codex-current", "codex", 0, now, map[string]string{"X-Codex-Primary-Used-Percent": "95"})
	sameProvider := quotaRoutingTestAuth("codex-other", "codex", 0, now, map[string]string{"X-Codex-Primary-Used-Percent": "60"})
	otherProvider := quotaRoutingTestAuth("devin-other", "devin", 0, now, map[string]string{"daily_quota_remaining_percent": "95%"})

	preferred := preferredQuotaMigrationAuths([]*Auth{current, sameProvider, otherProvider}, current, "gpt-5", now)
	if len(preferred) != 1 || preferred[0].ID != sameProvider.ID {
		t.Fatalf("preferred = %#v, want same-provider auth %q", preferred, sameProvider.ID)
	}
}

func TestPreferredQuotaMigrationAuthsFallsBackAcrossProviders(t *testing.T) {
	now := time.Now()
	current := quotaRoutingTestAuth("codex-current", "codex", 0, now, map[string]string{"X-Codex-Primary-Used-Percent": "95"})
	sameProviderLow := quotaRoutingTestAuth("codex-other", "codex", 0, now, map[string]string{"X-Codex-Primary-Used-Percent": "96"})
	otherProvider := quotaRoutingTestAuth("devin-other", "devin", 0, now, map[string]string{"daily_quota_remaining_percent": "80%"})

	preferred := preferredQuotaMigrationAuths([]*Auth{current, sameProviderLow, otherProvider}, current, "gpt-5", now)
	if len(preferred) != 1 || preferred[0].ID != otherProvider.ID {
		t.Fatalf("preferred = %#v, want cross-provider auth %q", preferred, otherProvider.ID)
	}
}

func TestQuotaStateRemainingPercentUsesNormalizedPluginObservation(t *testing.T) {
	now := time.Now()
	observation := quotaStateRemainingPercent("opencode-go", QuotaState{
		ObservedAt: now,
		Signals:    map[string]string{"normalized_quota_remaining_percent": "37.5"},
	}, now)
	if !observation.known || observation.percent != 37.5 {
		t.Fatalf("observation = %+v", observation)
	}
}
