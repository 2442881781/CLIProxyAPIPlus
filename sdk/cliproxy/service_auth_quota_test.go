package cliproxy

import (
	"context"
	"testing"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

func TestCredentialQuotaSurvivesUnversionedAuthReload(t *testing.T) {
	ctx := coreauth.WithSkipPersist(context.Background())
	observedAt := time.Now().UTC()
	for _, test := range []struct {
		name       string
		incoming   coreauth.QuotaState
		wantTime   time.Time
		wantSignal string
	}{
		{name: "missing observation", wantTime: observedAt, wantSignal: "0"},
		{name: "older observation", incoming: coreauth.QuotaState{ObservedAt: observedAt.Add(-time.Minute), Signals: map[string]string{"normalized_quota_remaining_percent": "80"}}, wantTime: observedAt, wantSignal: "0"},
		{name: "newer observation", incoming: coreauth.QuotaState{ObservedAt: observedAt.Add(time.Minute), Signals: map[string]string{"normalized_quota_remaining_percent": "50"}}, wantTime: observedAt.Add(time.Minute), wantSignal: "50"},
	} {
		t.Run(test.name, func(t *testing.T) {
			manager := coreauth.NewManager(nil, nil, nil)
			auth := &coreauth.Auth{ID: "quota-reload", Provider: "museai", Status: coreauth.StatusActive}
			if _, errRegister := manager.Register(ctx, auth); errRegister != nil {
				t.Fatal(errRegister)
			}
			if _, errObserve := manager.ObserveQuota(ctx, auth.ID, observedAt, map[string]string{"normalized_quota_remaining_percent": "0"}); errObserve != nil {
				t.Fatal(errObserve)
			}
			service := &Service{cfg: &config.Config{}, coreManager: manager}
			service.prepareCoreAuthForModelRegistration(ctx, &coreauth.Auth{ID: auth.ID, Provider: auth.Provider, Status: coreauth.StatusActive, Quota: test.incoming})
			current, _ := manager.GetByID(auth.ID)
			if !current.Quota.ObservedAt.Equal(test.wantTime) || current.Quota.Signals["normalized_quota_remaining_percent"] != test.wantSignal {
				t.Fatalf("reload quota = %+v, want observation %s at %s", current.Quota, test.wantSignal, test.wantTime)
			}
		})
	}
}
