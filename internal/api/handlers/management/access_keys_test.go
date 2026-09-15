package management

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	storeaccess "github.com/router-for-me/CLIProxyAPI/v7/internal/access/store_access"
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
