package auth

import (
	"context"
	"net/http"
	"testing"
	"time"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
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
}
