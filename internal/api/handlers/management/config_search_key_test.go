package management

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/searchproxy"
)

// Feature: Management API for search-api-key
//   Routes under /v0/management/search-api-key, guarded by the management key.
//   Entry fields: provider, api-key, label, base-url, proxy-url, disabled.

func newSearchKeyHandler(t *testing.T, keys ...config.SearchKey) *Handler {
	t.Helper()
	pool := searchproxy.NewPool(nil)
	pool.Update(keys)
	h := &Handler{
		cfg:            &config.Config{SearchKey: append([]config.SearchKey(nil), keys...)},
		configFilePath: writeTestConfigFile(t),
		searchPool:     pool,
	}
	return h
}

func serveSearchKey(h *Handler, handler func(*gin.Context), method, target, body string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(method, target, strings.NewReader(body))
	ctx.Request.Header.Set("Content-Type", "application/json")
	handler(ctx)
	return rec
}

// Scenario: List keys
//
//	When GET /search-api-key
//	Then all entries are returned with provider, label, base-url, proxy-url, disabled and the api-key
func TestGetSearchKeys(t *testing.T) {
	keys := []config.SearchKey{
		{Provider: "tavily", APIKey: "tvly-a", Label: "acc1", ProxyURL: "socks5://p:1"},
		{Provider: "exa", APIKey: "exa-a", BaseURL: "https://exa.example.com", Disabled: true},
	}
	h := newSearchKeyHandler(t, keys...)

	rec := serveSearchKey(h, h.GetSearchKeys, http.MethodGet, "/v0/management/search-api-key", "")

	var payload struct {
		Items []config.SearchKey `json:"search-api-key"`
	}
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &payload) != nil {
		t.Fatalf("response = %d %s", rec.Code, rec.Body.String())
	}
	if !reflect.DeepEqual(payload.Items, keys) {
		t.Fatalf("items = %#v, want %#v", payload.Items, keys)
	}
}

// Scenario: Replace the whole list
//
//	When PUT /search-api-key with an array (or {"items":[...]})
//	Then config is replaced, sanitized, persisted and a reload is triggered
func TestPutSearchKeysReplacesAndPersists(t *testing.T) {
	for _, body := range []string{
		`[{"provider":" Tavily ","api-key":" tvly-new "},{"provider":"firecrawl","api-key":"fc-1","label":"main"}]`,
		`{"items":[{"provider":" Tavily ","api-key":" tvly-new "},{"provider":"firecrawl","api-key":"fc-1","label":"main"}]}`,
	} {
		h := newSearchKeyHandler(t, config.SearchKey{Provider: "exa", APIKey: "old"})
		reloaded := make(chan *config.Config, 1)
		h.SetConfigReloadHook(func(_ context.Context, cfg *config.Config) { reloaded <- cfg })

		rec := serveSearchKey(h, h.PutSearchKeys, http.MethodPut, "/v0/management/search-api-key", body)

		want := []config.SearchKey{{Provider: "tavily", APIKey: "tvly-new"}, {Provider: "firecrawl", APIKey: "fc-1", Label: "main"}}
		if rec.Code != http.StatusOK || !reflect.DeepEqual(h.cfg.SearchKey, want) {
			t.Fatalf("response = %d %s, keys = %#v", rec.Code, rec.Body.String(), h.cfg.SearchKey)
		}
		saved, err := os.ReadFile(h.configFilePath)
		if err != nil || !strings.Contains(string(saved), "search-api-key:") || !strings.Contains(string(saved), "fc-1") || strings.Contains(string(saved), "old") {
			t.Fatalf("persisted config = %s err=%v", saved, err)
		}
		select {
		case cfg := <-reloaded:
			if len(cfg.SearchKey) != 2 {
				t.Fatalf("reload hook got %d keys, want 2", len(cfg.SearchKey))
			}
		case <-time.After(5 * time.Second):
			t.Fatal("reload hook not called")
		}
	}
}

// Scenario: Invalid entries are rejected
//
//	When PUT or PATCH contains an unknown provider or an empty api-key
//	Then the response is 400 and config is unchanged
func TestPutSearchKeysRejectsInvalidEntries(t *testing.T) {
	original := config.SearchKey{Provider: "exa", APIKey: "keep"}
	h := newSearchKeyHandler(t, original)

	for _, body := range []string{
		`[{"provider":"bing","api-key":"k"}]`,
		`[{"provider":"tavily","api-key":"  "}]`,
		`not json`,
	} {
		rec := serveSearchKey(h, h.PutSearchKeys, http.MethodPut, "/v0/management/search-api-key", body)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("PUT %s = %d, want 400", body, rec.Code)
		}
	}
	for _, body := range []string{
		`{"index":0,"value":{"provider":"bing"}}`,
		`{"index":0,"value":{"api-key":""}}`,
	} {
		rec := serveSearchKey(h, h.PatchSearchKey, http.MethodPatch, "/v0/management/search-api-key", body)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("PATCH %s = %d, want 400", body, rec.Code)
		}
	}
	if !reflect.DeepEqual(h.cfg.SearchKey, []config.SearchKey{original}) {
		t.Fatalf("config changed: %#v", h.cfg.SearchKey)
	}
}

// Scenario: Patch one entry
//
//	When PATCH /search-api-key with {"index":0,"value":{"disabled":true,"label":"acc2"}}
//	Then only the given fields change on that entry
func TestPatchSearchKeyUpdatesFields(t *testing.T) {
	h := newSearchKeyHandler(t,
		config.SearchKey{Provider: "tavily", APIKey: "tvly-a", Label: "acc1", ProxyURL: "socks5://p:1"},
		config.SearchKey{Provider: "exa", APIKey: "exa-a"},
	)

	rec := serveSearchKey(h, h.PatchSearchKey, http.MethodPatch, "/v0/management/search-api-key", `{"index":0,"value":{"disabled":true,"label":"acc2"}}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("response = %d %s", rec.Code, rec.Body.String())
	}
	want := config.SearchKey{Provider: "tavily", APIKey: "tvly-a", Label: "acc2", ProxyURL: "socks5://p:1", Disabled: true}
	if h.cfg.SearchKey[0] != want || h.cfg.SearchKey[1] != (config.SearchKey{Provider: "exa", APIKey: "exa-a"}) {
		t.Fatalf("keys = %#v", h.cfg.SearchKey)
	}

	rec = serveSearchKey(h, h.PatchSearchKey, http.MethodPatch, "/v0/management/search-api-key", `{"match":"exa-a","value":{"base-url":"https://exa.example.com/"}}`)
	if rec.Code != http.StatusOK || h.cfg.SearchKey[1].BaseURL != "https://exa.example.com" {
		t.Fatalf("match patch = %d, keys = %#v", rec.Code, h.cfg.SearchKey)
	}

	rec = serveSearchKey(h, h.PatchSearchKey, http.MethodPatch, "/v0/management/search-api-key", `{"index":9,"value":{"label":"x"}}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("missing index = %d, want 404", rec.Code)
	}
}

// Scenario: Delete by index or by api-key
//
//	When DELETE /search-api-key?index=0 or ?api-key=tvly-x
//	Then the matching entry is removed; a miss returns 404
func TestDeleteSearchKey(t *testing.T) {
	h := newSearchKeyHandler(t,
		config.SearchKey{Provider: "tavily", APIKey: "tvly-a"},
		config.SearchKey{Provider: "exa", APIKey: "exa-a"},
		config.SearchKey{Provider: "firecrawl", APIKey: "fc-a"},
	)

	if rec := serveSearchKey(h, h.DeleteSearchKey, http.MethodDelete, "/v0/management/search-api-key?index=0", ""); rec.Code != http.StatusOK {
		t.Fatalf("delete index = %d", rec.Code)
	}
	if rec := serveSearchKey(h, h.DeleteSearchKey, http.MethodDelete, "/v0/management/search-api-key?api-key=fc-a", ""); rec.Code != http.StatusOK {
		t.Fatalf("delete api-key = %d", rec.Code)
	}
	if !reflect.DeepEqual(h.cfg.SearchKey, []config.SearchKey{{Provider: "exa", APIKey: "exa-a"}}) {
		t.Fatalf("keys = %#v", h.cfg.SearchKey)
	}
	for _, target := range []string{"?api-key=missing", "?index=5", ""} {
		if rec := serveSearchKey(h, h.DeleteSearchKey, http.MethodDelete, "/v0/management/search-api-key"+target, ""); rec.Code != http.StatusNotFound && rec.Code != http.StatusBadRequest {
			t.Fatalf("delete %q = %d, want 404/400", target, rec.Code)
		}
	}
	if rec := serveSearchKey(h, h.DeleteSearchKey, http.MethodDelete, "/v0/management/search-api-key?api-key=missing", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("delete missing = %d, want 404", rec.Code)
	}
}

// Scenario: Runtime status
//
//	When GET /search-api-key/status
//	Then each key's config index, masked key, provider, label, cooldown-until, last status and counts are returned
func TestGetSearchKeyStatus(t *testing.T) {
	const secret = "tvly-dev-1234567890abcdef"
	h := newSearchKeyHandler(t,
		config.SearchKey{Provider: "exa", APIKey: "exa-first"},
		config.SearchKey{Provider: "tavily", APIKey: secret, Label: "acc1"},
	)
	id := searchproxy.KeyID("tavily", secret)
	h.searchPool.Report(id, 432, true)
	h.searchPool.Cooldown(id, time.Hour, 432)

	rec := serveSearchKey(h, h.GetSearchKeyStatus, http.MethodGet, "/v0/management/search-api-key/status", "")

	var payload struct {
		Keys []struct {
			searchproxy.KeyStatus
			Index int `json:"index"`
		} `json:"keys"`
	}
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &payload) != nil || len(payload.Keys) != 2 {
		t.Fatalf("response = %d %s", rec.Code, rec.Body.String())
	}
	st := payload.Keys[0]
	if payload.Keys[1].Provider != "exa" || payload.Keys[1].Index != 0 {
		t.Fatalf("exa status = %#v, want index 0", payload.Keys[1])
	}
	if st.Index != 1 || st.Provider != "tavily" || st.Label != "acc1" || st.LastStatus != 432 || st.Failures != 1 || st.CooldownUntil.IsZero() {
		t.Fatalf("status = %#v", st)
	}
	if strings.Contains(rec.Body.String(), secret) {
		t.Fatalf("status leaks key: %s", rec.Body.String())
	}
}

// Scenario: Reset cooldown
//
//	When POST /search-api-key/reset-cooldown with {"provider":"tavily","api-key":"tvly-x"} (or {"all":true})
//	Then the matching key(s) become eligible immediately
func TestResetSearchKeyCooldown(t *testing.T) {
	h := newSearchKeyHandler(t,
		config.SearchKey{Provider: "tavily", APIKey: "tvly-x"},
		config.SearchKey{Provider: "exa", APIKey: "exa-x"},
	)
	h.searchPool.Cooldown(searchproxy.KeyID("tavily", "tvly-x"), time.Hour, 432)
	h.searchPool.Cooldown(searchproxy.KeyID("exa", "exa-x"), time.Hour, 402)

	rec := serveSearchKey(h, h.ResetSearchKeyCooldown, http.MethodPost, "/v0/management/search-api-key/reset-cooldown", `{"provider":"tavily","api-key":"tvly-x"}`)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"reset":1`) {
		t.Fatalf("reset one = %d %s", rec.Code, rec.Body.String())
	}
	if _, err := h.searchPool.Pick("tavily", nil); err != nil {
		t.Fatalf("tavily still cooling: %v", err)
	}
	if _, err := h.searchPool.Pick("exa", nil); err == nil {
		t.Fatal("exa should still be cooling")
	}

	rec = serveSearchKey(h, h.ResetSearchKeyCooldown, http.MethodPost, "/v0/management/search-api-key/reset-cooldown", `{"all":true}`)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"reset":1`) {
		t.Fatalf("reset all = %d %s", rec.Code, rec.Body.String())
	}
	if _, err := h.searchPool.Pick("exa", nil); err != nil {
		t.Fatalf("exa still cooling: %v", err)
	}

	rec = serveSearchKey(h, h.ResetSearchKeyCooldown, http.MethodPost, "/v0/management/search-api-key/reset-cooldown", `{}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("empty reset = %d, want 400", rec.Code)
	}
}
