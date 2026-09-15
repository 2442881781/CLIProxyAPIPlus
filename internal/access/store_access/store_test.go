package storeaccess

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	dir := t.TempDir()
	store := &Store{path: dir + "/access-keys.json"}
	if err := store.loadLocked(); err != nil {
		t.Fatalf("load: %v", err)
	}
	return store
}

func TestStoreCreateLookupDelete(t *testing.T) {
	s := newTestStore(t)
	entry, err := s.Create("sk-cpa-testkey123", AccessKey{Name: "alice"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if entry.KeyHash == "" || strings.Contains(entry.KeyHash, "testkey123") {
		t.Fatal("key material must be stored hashed")
	}
	if got := s.Lookup("sk-cpa-testkey123"); got == nil || got.ID != entry.ID {
		t.Fatal("lookup failed")
	}
	if got := s.Lookup("sk-cpa-wrong"); got != nil {
		t.Fatal("wrong key must not match")
	}
	if err := s.Delete(entry.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if got := s.Lookup("sk-cpa-testkey123"); got != nil {
		t.Fatal("deleted key must not match")
	}
}

func TestStorePersistence(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/access-keys.json"
	s := &Store{path: path}
	if err := s.loadLocked(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Create("sk-cpa-persist", AccessKey{Name: "bob"}); err != nil {
		t.Fatal(err)
	}
	s2 := &Store{path: path}
	if err := s2.loadLocked(); err != nil {
		t.Fatal(err)
	}
	if got := s2.Lookup("sk-cpa-persist"); got == nil || got.Name != "bob" {
		t.Fatal("reloaded store lost the key")
	}
}

func TestModelAllowed(t *testing.T) {
	k := &AccessKey{}
	if !k.ModelAllowed("anything") {
		t.Fatal("empty allowlist must allow all")
	}
	k.AllowedModels = []string{"gpt-6-astra", "zhipu/*"}
	if !k.ModelAllowed("gpt-6-astra") || !k.ModelAllowed("zhipu/glm-5.3") {
		t.Fatal("allowed models rejected")
	}
	if k.ModelAllowed("devin/swe-2") || k.ModelAllowed("gpt-6-other") {
		t.Fatal("disallowed model accepted")
	}
}

func TestExpired(t *testing.T) {
	k := &AccessKey{ExpiresAt: time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)}
	if !k.Expired(time.Now()) {
		t.Fatal("past expiry must be expired")
	}
	k.ExpiresAt = time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	if k.Expired(time.Now()) {
		t.Fatal("future expiry must be valid")
	}
	k.ExpiresAt = "garbage"
	if k.Expired(time.Now()) {
		t.Fatal("unparsable expiry must not block")
	}
}

func TestQuotaAndUsage(t *testing.T) {
	s := newTestStore(t)
	entry, _ := s.Create("sk-cpa-quota", AccessKey{Quota: Quota{TokenLimit: 100, Period: "monthly"}})
	if entry.QuotaExceeded(time.Now()) {
		t.Fatal("fresh key must not be over quota")
	}
	if !s.RecordUsage("sk-cpa-quota", 60, false) {
		t.Fatal("record usage failed")
	}
	got := s.Lookup("sk-cpa-quota")
	if got.Usage.TotalTokens != 60 || got.Usage.PeriodTokens != 60 || got.Usage.Requests != 1 {
		t.Fatalf("usage not counted: %+v", got.Usage)
	}
	if got.QuotaExceeded(time.Now()) {
		t.Fatal("under limit must not be over quota")
	}
	s.RecordUsage("sk-cpa-quota", 50, false)
	if !s.Lookup("sk-cpa-quota").QuotaExceeded(time.Now()) {
		t.Fatal("over limit must be over quota")
	}
	// period roll resets the window counter
	s.mu.Lock()
	s.keys[entry.ID].Usage.PeriodKey = "2000-01"
	s.mu.Unlock()
	if !s.RecordUsage("sk-cpa-quota", 10, false) {
		t.Fatal("record after roll failed")
	}
	got = s.Lookup("sk-cpa-quota")
	if got.Usage.PeriodTokens != 10 || got.Usage.TotalTokens != 120 {
		t.Fatalf("period roll wrong: %+v", got.Usage)
	}
	if got.QuotaExceeded(time.Now()) {
		t.Fatal("rolled period must reset quota")
	}
	if s.RecordUsage("sk-cpa-unknown", 10, false) {
		t.Fatal("unknown key must not count")
	}
}

func TestQuotaEnforcementInProvider(t *testing.T) {
	s := newTestStore(t)
	s.Create("sk-cpa-limited", AccessKey{Quota: Quota{TokenLimit: 10}})
	s.RecordUsage("sk-cpa-limited", 20, false)
	p := &provider{store: s}
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"x"}`))
	req.Header.Set("Authorization", "Bearer sk-cpa-limited")
	_, authErr := p.Authenticate(req.Context(), req)
	if authErr == nil || authErr.HTTPStatusCode() != 429 {
		t.Fatalf("over-quota key should 429, got %v", authErr)
	}
}

func TestProviderAuthenticate(t *testing.T) {
	s := newTestStore(t)
	entry, _ := s.Create("sk-cpa-good", AccessKey{Name: "carol", AllowedModels: []string{"gpt-6-astra"}})
	disabled, _ := s.Create("sk-cpa-off", AccessKey{Disabled: true})
	_ = disabled
	p := &provider{store: s}

	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"gpt-6-astra"}`))
	req.Header.Set("Authorization", "Bearer sk-cpa-good")
	res, authErr := p.Authenticate(req.Context(), req)
	if authErr != nil || res == nil || res.Metadata["key_id"] != entry.ID {
		t.Fatalf("valid key rejected: %v", authErr)
	}

	req = httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"devin/swe-2"}`))
	req.Header.Set("Authorization", "Bearer sk-cpa-good")
	_, authErr = p.Authenticate(req.Context(), req)
	if authErr == nil || authErr.HTTPStatusCode() != 403 {
		t.Fatalf("model restriction should 403, got %v", authErr)
	}

	req = httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"gpt-6-astra"}`))
	req.Header.Set("Authorization", "Bearer sk-cpa-off")
	_, authErr = p.Authenticate(req.Context(), req)
	if authErr == nil || authErr.HTTPStatusCode() != 403 {
		t.Fatalf("disabled key should 403, got %v", authErr)
	}

	req = httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"gpt-6-astra"}`))
	req.Header.Set("Authorization", "Bearer unknown")
	_, authErr = p.Authenticate(req.Context(), req)
	if authErr == nil || authErr.Code != "not_handled" {
		t.Fatalf("unknown key should be not handled, got %v", authErr)
	}
}

func TestGroupCRUDAndMigration(t *testing.T) {
	dir := t.TempDir()
	// Legacy bare-array file must migrate.
	legacy := `[{"id":"key-1","key_hash":"` + HashKey("sk-cpa-a") + `","key_prefix":"sk-cpa-a","created_at":"2026-01-01T00:00:00Z"}]`
	path := dir + "/access-keys.json"
	if err := os.WriteFile(path, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	s := &Store{path: path}
	if err := s.loadLocked(); err != nil {
		t.Fatalf("legacy load: %v", err)
	}
	if len(s.keys) != 1 {
		t.Fatalf("legacy keys not loaded: %d", len(s.keys))
	}
	// Persist must now write the object schema.
	if err := s.persistLocked(); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	var file storeFile
	if err := json.Unmarshal(data, &file); err != nil || len(file.Keys) != 1 {
		t.Fatalf("persisted schema wrong: %v", err)
	}

	if _, err := s.UpsertGroup(Group{Name: "infra", AllowedAuths: []string{"auth-1"}, AllowedModels: []string{"zhipu/*"}, MaxConcurrency: 2, RateLimitRPM: 3}); err != nil {
		t.Fatal(err)
	}
	g := s.GetGroup("infra")
	if g == nil || g.MaxConcurrency != 2 || !g.AuthAllowed("auth-1", "", "") || g.AuthAllowed("auth-2", "", "") {
		t.Fatalf("group wrong: %+v", g)
	}
	if !g.ModelAllowed("zhipu/glm-5.3") || g.ModelAllowed("gpt-6") {
		t.Fatal("group model check wrong")
	}
	// reload sees groups
	s2 := &Store{path: path}
	if err := s2.loadLocked(); err != nil || s2.GetGroup("infra") == nil {
		t.Fatalf("group not persisted: %v", err)
	}
	if err := s.DeleteGroup("infra"); err != nil || s.GetGroup("infra") != nil {
		t.Fatal("delete group failed")
	}
}

func TestGroupAcquireRateAndConcurrency(t *testing.T) {
	s := &Store{}
	s.keys = map[string]*AccessKey{}
	s.groups = map[string]*Group{}
	if _, err := s.UpsertGroup(Group{Name: "g", MaxConcurrency: 1, RateLimitRPM: 2}); err == nil {
		t.Fatal("unloaded store should error")
	}
	s.loaded = true
	s.path = t.TempDir() + "/access-keys.json"
	if _, err := s.UpsertGroup(Group{Name: "g", MaxConcurrency: 1, RateLimitRPM: 2}); err != nil {
		t.Fatal(err)
	}
	rel, _, err := s.AcquireGroup("g")
	if err != nil || rel == nil {
		t.Fatalf("first acquire: %v", err)
	}
	if _, _, err := s.AcquireGroup("g"); err == nil {
		t.Fatal("concurrency should reject second")
	}
	rel()
	if _, _, err := s.AcquireGroup("g"); err != nil {
		t.Fatalf("after release: %v", err)
	}
	if _, _, err := s.AcquireGroup("g"); err == nil {
		t.Fatal("rate limit should reject third")
	}
	if _, _, err := s.AcquireGroup("missing"); err != nil {
		t.Fatal("missing group should pass through")
	}
}

func TestGroupUsageAggregation(t *testing.T) {
	s := &Store{}
	s.keys = map[string]*AccessKey{}
	s.groups = map[string]*Group{}
	s.byHash = map[string]*AccessKey{}
	s.loaded = true
	s.path = t.TempDir() + "/access-keys.json"
	if _, err := s.UpsertGroup(Group{Name: "g"}); err != nil {
		t.Fatal(err)
	}
	entry, _ := s.Create("sk-cpa-g1", AccessKey{Name: "a", Group: "g"})
	if !s.RecordUsage("sk-cpa-g1", 100, false) {
		t.Fatal("record failed")
	}
	g := s.GetGroup("g")
	if g.Usage.TotalTokens != 100 || g.Usage.Requests != 1 {
		t.Fatalf("group usage: %+v", g.Usage)
	}
	_ = entry
}

func TestProviderGroupModelCheck(t *testing.T) {
	s := &Store{}
	s.keys = map[string]*AccessKey{}
	s.byHash = map[string]*AccessKey{}
	s.groups = map[string]*Group{"g": {Name: "g", AllowedModels: []string{"zhipu/*"}}}
	s.loaded = true
	s.path = t.TempDir() + "/access-keys.json"
	if _, err := s.Create("sk-cpa-g", AccessKey{Group: "g"}); err != nil {
		t.Fatal(err)
	}
	p := &provider{store: s}
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"zhipu/glm-5.3"}`))
	req.Header.Set("Authorization", "Bearer sk-cpa-g")
	if _, err := p.Authenticate(req.Context(), req); err != nil {
		t.Fatalf("group-allowed model rejected: %v", err)
	}
	req = httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"gpt-6"}`))
	req.Header.Set("Authorization", "Bearer sk-cpa-g")
	if _, err := p.Authenticate(req.Context(), req); err == nil || err.HTTPStatusCode() != 403 {
		t.Fatalf("group-disallowed model should 403, got %v", err)
	}
}
