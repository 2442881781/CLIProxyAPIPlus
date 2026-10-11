package auth

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"testing"
	"time"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

type staticQuotaTestExecutor struct{ provider string }

func (e staticQuotaTestExecutor) Identifier() string { return e.provider }
func (staticQuotaTestExecutor) Execute(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}
func (staticQuotaTestExecutor) ExecuteStream(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	return nil, nil
}
func (staticQuotaTestExecutor) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}
func (staticQuotaTestExecutor) Refresh(_ context.Context, auth *Auth) (*Auth, error) {
	return auth, nil
}
func (staticQuotaTestExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, nil
}

func TestProviderQuotaRanksStaticPluginCandidateAgainstOrdinaryAuth(t *testing.T) {
	const model = "deepseek-v4.1-flash"
	manager := NewManager(nil, NewQuotaAwareSelector(&RoundRobinSelector{}), nil)
	manager.SetConfig(&internalconfig.Config{Routing: internalconfig.RoutingConfig{QuotaAware: true}})
	manager.RegisterExecutor(staticQuotaTestExecutor{provider: "commandcode"})
	manager.RegisterExecutor(staticQuotaTestExecutor{provider: "devin"})

	staticCandidate := &Auth{ID: "plugin-static:commandcode", Provider: "commandcode", Status: StatusActive, Attributes: map[string]string{}}
	MarkPluginStaticAuth(staticCandidate)
	if _, errRegister := manager.Register(WithSkipPersist(context.Background()), staticCandidate); errRegister != nil {
		t.Fatalf("Register(static): %v", errRegister)
	}
	ordinary := &Auth{ID: "devin-auth", Provider: "devin", Status: StatusActive}
	ordinary.Quota = QuotaState{ObservedAt: time.Now(), Signals: map[string]string{"normalized_quota_remaining_percent": "25"}}
	if _, errRegister := manager.Register(WithSkipPersist(context.Background()), ordinary); errRegister != nil {
		t.Fatalf("Register(devin): %v", errRegister)
	}
	if errObserve := manager.ObserveProviderQuota(context.Background(), ProviderQuotaSnapshot{Provider: "commandcode", RemainingPercent: 90, ObservedAt: time.Now()}); errObserve != nil {
		t.Fatalf("ObserveProviderQuota(): %v", errObserve)
	}
	modelRegistry := registry.GetGlobalRegistry()
	modelRegistry.RegisterClient(staticCandidate.ID, "commandcode", []*registry.ModelInfo{{ID: model}})
	modelRegistry.RegisterClient(ordinary.ID, "devin", []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() {
		modelRegistry.UnregisterClient(staticCandidate.ID)
		modelRegistry.UnregisterClient(ordinary.ID)
	})

	selected, _, provider, errPick := manager.pickNextMixedLegacy(context.Background(), []string{"commandcode", "devin"}, model, cliproxyexecutor.Options{}, nil)
	if errPick != nil {
		t.Fatalf("pickNextMixedLegacy(): %v", errPick)
	}
	if selected == nil || selected.ID != staticCandidate.ID || provider != "commandcode" {
		t.Fatalf("selection = %#v, %q; want static CommandCode candidate", selected, provider)
	}
	diagnostics := manager.RecentRoutingDiagnostics(1)
	if len(diagnostics) != 1 {
		t.Fatalf("routing diagnostics len = %d, want 1", len(diagnostics))
	}
	if diagnostics[0].SelectedProvider != "commandcode" || diagnostics[0].SelectionReason != "highest_remaining_quota" {
		t.Fatalf("routing diagnostic = %+v, want CommandCode highest quota", diagnostics[0])
	}
}

func TestProviderQuotaExhaustionFiltersBeforePriorityAndAffinity(t *testing.T) {
	for _, sticky := range []bool{false, true} {
		t.Run(strconv.FormatBool(sticky), func(t *testing.T) {
			var selector Selector = &RoundRobinSelector{}
			if sticky {
				affinity := NewSessionAffinitySelector(selector)
				defer affinity.Stop()
				selector = affinity
			}
			manager := NewManager(nil, selector, nil)
			for _, provider := range []string{"commandcode", "opencode-go"} {
				manager.RegisterExecutor(staticQuotaTestExecutor{provider: provider})
				candidate := &Auth{ID: "plugin-static:" + provider, Provider: provider, Status: StatusActive, Attributes: map[string]string{"priority": "0"}}
				if provider == "commandcode" {
					candidate.Attributes["priority"] = "1"
				}
				MarkPluginStaticAuth(candidate)
				if _, errRegister := manager.Register(WithSkipPersist(context.Background()), candidate); errRegister != nil {
					t.Fatal(errRegister)
				}
			}
			opts := cliproxyexecutor.Options{Headers: http.Header{"X-Session-Id": {"provider-zero-session"}}}
			pick := func(wantProvider string) {
				selected, _, provider, errPick := manager.pickNextMixed(context.Background(), []string{"commandcode", "opencode-go"}, "", opts, nil)
				if errPick != nil || selected == nil || provider != wantProvider {
					t.Fatalf("selection = %v, %q, %v; want %s", selected != nil, provider, errPick, wantProvider)
				}
			}
			pick("commandcode")
			if errObserve := manager.ObserveProviderQuota(context.Background(), ProviderQuotaSnapshot{Provider: "commandcode", RemainingPercent: 0, ObservedAt: time.Now()}); errObserve != nil {
				t.Fatal(errObserve)
			}
			pick("opencode-go") // Unknown quota is eligible even below the exhausted tier.
			if errObserve := manager.ObserveProviderQuota(context.Background(), ProviderQuotaSnapshot{Provider: "opencode-go", RemainingPercent: 0, ObservedAt: time.Now()}); errObserve != nil {
				t.Fatal(errObserve)
			}
			selected, _, _, errPick := manager.pickNextMixed(context.Background(), []string{"commandcode", "opencode-go"}, "", opts, nil)
			var unavailable *Error
			if selected != nil || !errors.As(errPick, &unavailable) || unavailable.Code != "auth_unavailable" {
				t.Fatalf("all exhausted = %v, %v; want local auth_unavailable", selected != nil, errPick)
			}
		})
	}
}
