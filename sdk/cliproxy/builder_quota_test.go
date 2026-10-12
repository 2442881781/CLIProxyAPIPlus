package cliproxy

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/pluginhost"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/providerquota"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

type startupCredentialQuotaProvider struct {
	url string
}

func (p startupCredentialQuotaProvider) Identifier() string { return "startup-quota" }

func (p startupCredentialQuotaProvider) DescribeQuota(context.Context, pluginapi.QuotaDescribeRequest) (pluginapi.QuotaDescribeResponse, error) {
	return pluginapi.QuotaDescribeResponse{SupportedProviders: []string{p.Identifier()}}, nil
}

func (p startupCredentialQuotaProvider) FetchQuota(ctx context.Context, req pluginapi.QuotaFetchRequest) (pluginapi.QuotaFetchResponse, error) {
	var credential struct {
		Token string `json:"token"`
	}
	if errDecode := json.Unmarshal(req.StorageJSON, &credential); errDecode != nil {
		return pluginapi.QuotaFetchResponse{}, fmt.Errorf("decode quota credential: %w", errDecode)
	}
	request, errRequest := http.NewRequestWithContext(ctx, http.MethodGet, p.url, nil)
	if errRequest != nil {
		return pluginapi.QuotaFetchResponse{}, errRequest
	}
	request.Header.Set("Authorization", "Bearer "+credential.Token)
	response, errRequest := http.DefaultClient.Do(request)
	if errRequest != nil {
		return pluginapi.QuotaFetchResponse{}, errRequest
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return pluginapi.QuotaFetchResponse{}, fmt.Errorf("quota status %d", response.StatusCode)
	}
	var quota pluginapi.QuotaFetchResponse
	errDecode := json.NewDecoder(response.Body).Decode(&quota)
	return quota, errDecode
}

func (startupCredentialQuotaProvider) ResetQuota(context.Context, pluginapi.QuotaResetRequest) (pluginapi.QuotaResetResponse, error) {
	return pluginapi.QuotaResetResponse{}, fmt.Errorf("unsupported quota reset")
}

func TestBuilderStartupQuotaUsesPersistedCredentialBeforeAPIServer(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer account-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"groups":[{"buckets":[{"remainingFraction":0.64}]}]}`))
	}))
	defer upstream.Close()

	authDir := t.TempDir()
	path := filepath.Join(authDir, "account.json")
	if errWrite := os.WriteFile(path, []byte(`{"type":"startup-quota","token":"account-token"}`), 0600); errWrite != nil {
		t.Fatal(errWrite)
	}
	manager := coreauth.NewManager(nil, nil, nil)
	if _, errRegister := manager.Register(context.Background(), &coreauth.Auth{
		ID: "account.json", Provider: "startup-quota", FileName: "account.json", Status: coreauth.StatusActive,
		Attributes: map[string]string{coreauth.AttributePath: path},
	}); errRegister != nil {
		t.Fatal(errRegister)
	}
	host := pluginhost.New()
	service, errBuild := NewBuilder().
		WithConfig(&config.Config{AuthDir: authDir}).
		WithConfigPath(filepath.Join(authDir, "config.yaml")).
		WithCoreAuthManager(manager).
		WithPluginHost(host).
		Build()
	if errBuild != nil {
		t.Fatal(errBuild)
	}
	host.RegisterPluginForTest("startup-quota", pluginapi.Plugin{
		Capabilities: pluginapi.Capabilities{QuotaProvider: startupCredentialQuotaProvider{url: upstream.URL}},
	})
	failures := service.coreManager.RefreshCredentialProviderQuotas(context.Background(), host, providerquota.RemainingPercent, coreauth.ProviderQuotaRefreshOptions{MaxConcurrent: 1})
	if len(failures) != 0 {
		t.Fatalf("startup quota failed before API server creation: %v", failures)
	}
	auth, _ := manager.GetByID("account.json")
	if auth.Quota.Signals["normalized_quota_remaining_percent"] != "64" {
		t.Fatalf("startup quota observation = %+v, want 64%% remaining", auth.Quota)
	}
}
