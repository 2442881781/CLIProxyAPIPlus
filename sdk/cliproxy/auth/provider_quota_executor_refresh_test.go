package auth

import (
	"context"
	"net/http"
	"testing"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

type quotaRefreshExecutor struct{}

func (quotaRefreshExecutor) Identifier() string { return "devin" }
func (quotaRefreshExecutor) Execute(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}
func (quotaRefreshExecutor) ExecuteStream(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	return nil, nil
}
func (quotaRefreshExecutor) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}
func (quotaRefreshExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, nil
}
func (quotaRefreshExecutor) Refresh(_ context.Context, auth *Auth) (*Auth, error) {
	updated := auth.Clone()
	updated.Quota.ObservedAt = time.Now().UTC()
	updated.Quota.Signals = map[string]string{
		"daily_quota_remaining_percent":  "75%",
		"weekly_quota_remaining_percent": "40%",
	}
	return updated, nil
}

func TestRefreshProviderCredentialQuotasUsesExecutorRefresh(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	manager.RegisterExecutor(quotaRefreshExecutor{})
	if _, errRegister := manager.Register(context.Background(), &Auth{ID: "devin-auth", Provider: "devin", Status: StatusActive}); errRegister != nil {
		t.Fatalf("Register(): %v", errRegister)
	}
	failures := manager.RefreshProviderCredentialQuotas(context.Background(), []string{"devin"}, time.Second, 2)
	if len(failures) != 0 {
		t.Fatalf("failures = %#v", failures)
	}
	auth, _ := manager.GetByID("devin-auth")
	observation := authQuotaRemainingPercent(auth, "", time.Now())
	if !observation.known || observation.percent != 40 {
		t.Fatalf("observation = %+v, want Devin limiting quota at 40%%", observation)
	}
}
