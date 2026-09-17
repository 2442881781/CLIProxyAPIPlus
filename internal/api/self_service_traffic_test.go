package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	storeaccess "github.com/router-for-me/CLIProxyAPI/v7/internal/access/store_access"
)

// Feature: self-service usage never reveals traffic (bandwidth) accounting.
func TestSelfServiceUsageHidesTraffic(t *testing.T) {
	server := newTestServer(t)

	store, err := storeaccess.Configure(t.TempDir())
	if err != nil {
		t.Fatalf("configure store: %v", err)
	}
	t.Cleanup(func() {
		// Leave an empty store configured so the shared store provider is
		// unregistered for the remaining tests in this package.
		if _, errCleanup := storeaccess.Configure(t.TempDir()); errCleanup != nil {
			t.Errorf("cleanup configure: %v", errCleanup)
		}
	})

	const key = "sk-cpa-selfservice-guard"
	entry, err := store.Create(key, storeaccess.AccessKey{
		Name:  "self",
		Quota: storeaccess.Quota{TokenLimit: 1000, ByteLimit: 1 << 30, Period: "monthly"},
	})
	if err != nil {
		t.Fatalf("create key: %v", err)
	}
	store.RecordTraffic(entry.ID, storeaccess.TrafficEvent{InBytes: 111, OutBytes: 222, Model: "gpt-self"})
	store.RecordUsage(key, storeaccess.UsageEvent{
		Tokens: 5, Model: "gpt-self", AuthID: "auth-self",
		UpstreamInBytes: 333, UpstreamOutBytes: 444,
	})

	request := httptest.NewRequest(http.MethodGet, "/v0/usage/me", nil)
	request.Header.Set("Authorization", "Bearer "+key)
	recorder := httptest.NewRecorder()
	server.engine.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	body := recorder.Body.String()
	for _, forbidden := range []string{"bytes", "byte_limit"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("self-service response leaks %q: %s", forbidden, body)
		}
	}
	var payload map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	usage, _ := payload["usage"].(map[string]any)
	if usage == nil || usage["total_tokens"].(float64) != 5 {
		t.Fatalf("token usage must survive: %v", payload["usage"])
	}
	if usage["models"] == nil {
		t.Fatal("model dimension rows must survive")
	}
	quota, _ := payload["quota"].(map[string]any)
	if quota == nil || quota["token_limit"].(float64) != 1000 {
		t.Fatalf("token quota must survive: %v", payload["quota"])
	}
}
