package management

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	storeaccess "github.com/router-for-me/CLIProxyAPI/v7/internal/access/store_access"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func setupAccessKeyRouter(t *testing.T) *gin.Engine {
	t.Helper()
	dir := t.TempDir()
	if _, err := storeaccess.Configure(dir); err != nil {
		t.Fatalf("configure store: %v", err)
	}
	t.Cleanup(func() {
		// Reset the shared store so other tests are unaffected.
		// Configure with a fresh temp dir is enough for isolation here.
	})
	gin.SetMode(gin.TestMode)
	r := gin.New()
	h := &Handler{}
	r.GET("/access-keys", h.ListAccessKeys)
	r.POST("/access-keys", h.CreateAccessKey)
	r.PATCH("/access-keys", h.PatchAccessKey)
	r.DELETE("/access-keys", h.DeleteAccessKey)
	r.POST("/access-keys/rotate", h.RotateAccessKey)
	return r
}

func doReq(t *testing.T, r *gin.Engine, method, path, body string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	var reader *strings.Reader
	if body != "" {
		reader = strings.NewReader(body)
	} else {
		reader = strings.NewReader("")
	}
	req := httptest.NewRequest(method, path, reader)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec, out
}

func TestAccessKeysCRUD(t *testing.T) {
	r := setupAccessKeyRouter(t)

	// create
	rec, body := doReq(t, r, "POST", "/access-keys", `{"name":"alice","quota":{"token_limit":1000,"period":"monthly"},"allowed_models":["gpt-*"]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	key, _ := body["key"].(string)
	id, _ := body["id"].(string)
	if !strings.HasPrefix(key, "sk-cpa-") || id == "" {
		t.Fatalf("bad create response: %v", body)
	}
	if body["key_hash"] != nil || body["keyHash"] != nil {
		t.Fatal("response must not leak key hash")
	}

	// list
	rec, body = doReq(t, r, "GET", "/access-keys", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("list: %d", rec.Code)
	}
	list, _ := body["access-keys"].([]any)
	if len(list) != 1 {
		t.Fatalf("expected 1 key, got %v", body)
	}
	entry, _ := list[0].(map[string]any)
	if entry["key"] != nil {
		t.Fatal("list must not return plaintext key")
	}
	if entry["key_prefix"] == "" {
		t.Fatal("list should return key_prefix")
	}

	// patch disable + reset
	rec, _ = doReq(t, r, "PATCH", "/access-keys?id="+id, `{"disabled":true}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("patch: %d %s", rec.Code, rec.Body.String())
	}

	// rotate
	rec, body = doReq(t, r, "POST", "/access-keys/rotate?id="+id, `{}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("rotate: %d %s", rec.Code, rec.Body.String())
	}
	newKey, _ := body["key"].(string)
	if newKey == "" || newKey == key {
		t.Fatal("rotate must return a fresh key")
	}

	// delete
	rec, _ = doReq(t, r, "DELETE", "/access-keys?id="+id, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("delete: %d", rec.Code)
	}
	_, body = doReq(t, r, "GET", "/access-keys", "")
	list, _ = body["access-keys"].([]any)
	if len(list) != 0 {
		t.Fatal("delete did not remove key")
	}
}

func TestAccessKeyValidation(t *testing.T) {
	r := setupAccessKeyRouter(t)
	rec, _ := doReq(t, r, "POST", "/access-keys", `{"expires_at":"not-a-date"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("bad expiry should 400, got %d", rec.Code)
	}
	rec, _ = doReq(t, r, "POST", "/access-keys", `{"quota":{"token_limit":1,"period":"weekly"}}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("bad period should 400, got %d", rec.Code)
	}
	rec, _ = doReq(t, r, "PATCH", "/access-keys", `{"disabled":true}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("missing id should 400, got %d", rec.Code)
	}
}

func TestRatesEndpoint(t *testing.T) {
	dir := t.TempDir()
	store, err := storeaccess.Configure(dir)
	if err != nil {
		t.Fatalf("configure store: %v", err)
	}
	gin.SetMode(gin.TestMode)
	r := gin.New()
	h := &Handler{}
	r.GET("/rates", h.GetRates)

	if _, err := store.Create("sk-cpa-rate-1", storeaccess.AccessKey{Name: "live"}); err != nil {
		t.Fatalf("create: %v", err)
	}
	store.RecordUsage("sk-cpa-rate-1", storeaccess.UsageEvent{Tokens: 120, InputTokens: 80, OutputTokens: 40})

	rec, body := doReq(t, r, "GET", "/rates", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("rates: %d %s", rec.Code, rec.Body.String())
	}
	if body["window_seconds"].(float64) != 60 {
		t.Fatalf("window_seconds: %v", body["window_seconds"])
	}
	keys, _ := body["keys"].([]any)
	if len(keys) != 1 {
		t.Fatalf("keys: %v", body["keys"])
	}
	row := keys[0].(map[string]any)
	if row["name"] != "live" || row["output_tokens_per_second"].(float64) != 40.0/60.0 {
		t.Fatalf("key rate row: %v", row)
	}
	if _, ok := body["providers"].([]any); !ok {
		t.Fatalf("providers: %v", body["providers"])
	}
	if _, ok := body["auths"].([]any); !ok {
		t.Fatalf("auths: %v", body["auths"])
	}
}

func TestAccessKeyUsageEndpoints(t *testing.T) {
	dir := t.TempDir()
	store, err := storeaccess.Configure(dir)
	if err != nil {
		t.Fatalf("configure store: %v", err)
	}
	gin.SetMode(gin.TestMode)
	r := gin.New()
	h := &Handler{}
	r.GET("/access-keys/usage", h.GetAccessKeyUsage)
	r.GET("/access-keys/usage-top", h.GetAccessKeyUsageTop)
	r.GET("/access-groups/usage", h.GetAccessGroupUsage)

	if _, err := store.UpsertGroup(storeaccess.Group{Name: "plan-a"}); err != nil {
		t.Fatalf("upsert group: %v", err)
	}
	entry, err := store.Create("sk-cpa-e2e-user", storeaccess.AccessKey{Name: "u", Group: "plan-a"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	store.RecordUsage("sk-cpa-e2e-user", storeaccess.UsageEvent{Tokens: 100, Model: "claude-x", AuthID: "auth-1", Latency: 80 * time.Millisecond})
	store.RecordUsage("sk-cpa-e2e-user", storeaccess.UsageEvent{Tokens: 60, Model: "gpt-y", AuthID: "auth-2", Failed: true})

	// per-key detail
	rec, body := doReq(t, r, "GET", "/access-keys/usage?id="+entry.ID, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("usage: %d %s", rec.Code, rec.Body.String())
	}
	usage, _ := body["usage"].(map[string]any)
	if usage["total_tokens"].(float64) != 160 || usage["failed"].(float64) != 1 {
		t.Fatalf("usage summary: %v", usage)
	}
	models, _ := body["models"].([]any)
	if len(models) != 2 || models[0].(map[string]any)["model"] != "claude-x" {
		t.Fatalf("models: %v", models)
	}
	auths, _ := body["auths"].([]any)
	if len(auths) != 2 {
		t.Fatalf("auths: %v", auths)
	}
	daily, _ := body["daily"].([]any)
	if len(daily) != 1 {
		t.Fatalf("daily: %v", daily)
	}

	// missing/unknown id
	if rec, _ := doReq(t, r, "GET", "/access-keys/usage", ""); rec.Code != http.StatusBadRequest {
		t.Fatalf("missing id should 400, got %d", rec.Code)
	}
	if rec, _ := doReq(t, r, "GET", "/access-keys/usage?id=nope", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown id should 404, got %d", rec.Code)
	}

	// leaderboard
	rec, body = doReq(t, r, "GET", "/access-keys/usage-top?by=tokens&period=all&limit=5", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("top: %d", rec.Code)
	}
	keys, _ := body["keys"].([]any)
	if len(keys) != 1 || keys[0].(map[string]any)["id"] != entry.ID || keys[0].(map[string]any)["tokens"].(float64) != 160 {
		t.Fatalf("top: %v", keys)
	}
	if rec, _ := doReq(t, r, "GET", "/access-keys/usage-top?by=bogus", ""); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad by should 400, got %d", rec.Code)
	}

	// group aggregate
	rec, body = doReq(t, r, "GET", "/access-groups/usage?name=plan-a", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("group usage: %d", rec.Code)
	}
	totals, _ := body["totals"].(map[string]any)
	if totals["tokens"].(float64) != 160 || totals["requests"].(float64) != 2 {
		t.Fatalf("group totals: %v", totals)
	}
	if rec, _ := doReq(t, r, "GET", "/access-groups/usage?name=nope", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown group should 404, got %d", rec.Code)
	}
}

func TestAccessKeyStoreRecoversFromConfiguredAuthDir(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, storeaccess.StoreFileName), []byte(`{"keys":[],"groups":[{"name":"deepseek"}]}`), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	h := &Handler{cfg: &config.Config{AuthDir: dir}}
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	store := h.accessKeyStore(ctx)
	if store == nil || store.GetGroup("deepseek") == nil {
		t.Fatalf("accessKeyStore() = %#v, want configured store with deepseek group", store)
	}
}
