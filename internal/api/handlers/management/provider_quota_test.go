package management

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func TestListAndRefreshProviderQuotas(t *testing.T) {
	gin.SetMode(gin.TestMode)
	manager := coreauth.NewManager(nil, nil, nil)
	now := time.Now().UTC()
	if errObserve := manager.ObserveProviderQuota(context.Background(), coreauth.ProviderQuotaSnapshot{
		Provider: "commandcode", RemainingPercent: 55, ObservedAt: now,
	}); errObserve != nil {
		t.Fatalf("ObserveProviderQuota(): %v", errObserve)
	}
	handler := NewHandlerWithoutConfigFilePath(nil, manager)

	listRecorder := httptest.NewRecorder()
	listContext, _ := gin.CreateTestContext(listRecorder)
	listContext.Request = httptest.NewRequest(http.MethodGet, "/v0/management/provider-quotas", nil)
	handler.ListProviderQuotas(listContext)
	if listRecorder.Code != http.StatusOK || !containsBody(listRecorder.Body.String(), `"provider":"commandcode"`) {
		t.Fatalf("list status=%d body=%s", listRecorder.Code, listRecorder.Body.String())
	}

	handler.SetProviderQuotaRefreshHook(func(context.Context, string) error {
		return errors.New("upstream unavailable")
	})
	refreshRecorder := httptest.NewRecorder()
	refreshContext, _ := gin.CreateTestContext(refreshRecorder)
	refreshContext.Params = gin.Params{{Key: "provider", Value: "commandcode"}}
	refreshContext.Request = httptest.NewRequest(http.MethodPost, "/v0/management/provider-quotas/commandcode/refresh", nil)
	handler.RefreshProviderQuota(refreshContext)
	if refreshRecorder.Code != http.StatusBadGateway || !containsBody(refreshRecorder.Body.String(), `"remaining_percent":55`) {
		t.Fatalf("refresh status=%d body=%s", refreshRecorder.Code, refreshRecorder.Body.String())
	}
}

func containsBody(body, expected string) bool {
	for i := 0; i+len(expected) <= len(body); i++ {
		if body[i:i+len(expected)] == expected {
			return true
		}
	}
	return false
}
