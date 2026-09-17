package auth

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

func TestRecordRoutingDiagnosticCapturesSafeProviderDecision(t *testing.T) {
	now := time.Date(2026, 2, 27, 12, 0, 0, 0, time.UTC)
	manager := NewManager(nil, nil, nil)
	candidates := []*Auth{
		routingDiagnosticAuth("plugin-static:commandcode", "commandcode", "secret-commandcode", 5.8, now.Add(-time.Minute)),
		routingDiagnosticAuth("devin-auth", "devin", "secret-devin", 89, now.Add(-2*time.Minute)),
		routingDiagnosticAuth("opencode-auth", "opencode-go", "secret-opencode", 100, now.Add(-30*time.Second)),
	}

	manager.recordRoutingDiagnostic("deepseek-v4.1-flash", candidates[2], candidates, cliproxyexecutor.Options{}, now)
	diagnostics := manager.RecentRoutingDiagnostics(1)
	if len(diagnostics) != 1 {
		t.Fatalf("diagnostics len = %d, want 1", len(diagnostics))
	}
	diagnostic := diagnostics[0]
	if diagnostic.SelectedProvider != "opencode-go" {
		t.Fatalf("selected provider = %q, want opencode-go", diagnostic.SelectedProvider)
	}
	if diagnostic.SelectionReason != "highest_remaining_quota" {
		t.Fatalf("selection reason = %q, want highest_remaining_quota", diagnostic.SelectionReason)
	}
	if diagnostic.AffinityBound {
		t.Fatal("affinity bound = true, want false")
	}
	if len(diagnostic.Candidates) != 3 {
		t.Fatalf("candidate providers = %d, want 3", len(diagnostic.Candidates))
	}

	payload, errMarshal := json.Marshal(diagnostic)
	if errMarshal != nil {
		t.Fatal(errMarshal)
	}
	serialized := string(payload)
	for _, secret := range []string{"secret-commandcode", "secret-devin", "secret-opencode", "plugin-static:commandcode", "devin-auth", "opencode-auth"} {
		if strings.Contains(serialized, secret) {
			t.Fatalf("diagnostic leaked %q: %s", secret, serialized)
		}
	}
}

func TestRecordRoutingDiagnosticReportsAffinityAndUnknownQuota(t *testing.T) {
	now := time.Date(2026, 2, 27, 12, 0, 0, 0, time.UTC)
	manager := NewManager(nil, nil, nil)
	selected := routingDiagnosticAuth("devin-auth", "devin", "", 89, now.Add(-time.Minute))
	unknown := &Auth{ID: "unknown-auth", Provider: "opencode-go"}
	opts := cliproxyexecutor.Options{Metadata: map[string]any{
		cliproxyexecutor.CanonicalSessionIDMetadataKey: "private-session-id",
	}}

	manager.recordRoutingDiagnostic("deepseek-v4.1-flash", selected, []*Auth{selected, unknown}, opts, now)
	diagnostic := manager.RecentRoutingDiagnostics(1)[0]
	if !diagnostic.AffinityBound {
		t.Fatal("affinity bound = false, want true")
	}
	if diagnostic.SelectionReason != "configured_strategy_unknown_quota" {
		t.Fatalf("selection reason = %q, want configured_strategy_unknown_quota", diagnostic.SelectionReason)
	}
	payload, _ := json.Marshal(diagnostic)
	if strings.Contains(string(payload), "private-session-id") {
		t.Fatalf("diagnostic leaked session identity: %s", payload)
	}
}

func TestRoutingDiagnosticsBufferIsBoundedAndNewestFirst(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	now := time.Date(2026, 2, 27, 12, 0, 0, 0, time.UTC)
	auth := routingDiagnosticAuth("auth", "devin", "", 50, now)
	for index := 0; index < routingDiagnosticsLimit+5; index++ {
		manager.recordRoutingDiagnostic("model-"+string(rune('a'+index%26)), auth, []*Auth{auth}, cliproxyexecutor.Options{}, now.Add(time.Duration(index)*time.Second))
	}
	diagnostics := manager.RecentRoutingDiagnostics(0)
	if len(diagnostics) != routingDiagnosticsLimit {
		t.Fatalf("diagnostics len = %d, want %d", len(diagnostics), routingDiagnosticsLimit)
	}
	if !diagnostics[0].ObservedAt.After(diagnostics[len(diagnostics)-1].ObservedAt) {
		t.Fatal("diagnostics are not newest first")
	}
}

func routingDiagnosticAuth(id, provider, apiKey string, remaining float64, observedAt time.Time) *Auth {
	auth := &Auth{
		ID:       id,
		Provider: provider,
		Quota: QuotaState{
			ObservedAt: observedAt,
			Signals: map[string]string{
				"normalized_quota_remaining_percent": formatNormalizedQuotaPercent(remaining),
			},
		},
	}
	if apiKey != "" {
		auth.Attributes = map[string]string{"api_key": apiKey}
	}
	return auth
}
