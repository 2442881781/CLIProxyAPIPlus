package storeaccess

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

// Feature: per-key downstream rate limiting for monthly-plan sales
//
//   The operator sells monthly subscription plans. Each plan maps to a group;
//   each customer holds one access key. Limits must be enforceable per key so
//   one abusive customer cannot exhaust a shared group budget, while group
//   defaults keep plan provisioning to a single configuration step.
//
//   Dimensions per key: RPM (token bucket), TPM (token bucket, charged
//   post-request from recorded usage), RPD (persisted daily counter, UTC),
//   MaxConcurrency (in-flight counter, released when the request finishes).
//
//   Merge rule: key field > 0 overrides the group's per-key default; key field
//   0/unset inherits the group default; key field -1 forces unlimited. Group
//   per-key fields <= 0 mean "no default".

type fakeClock struct{ t time.Time }

func (c *fakeClock) now() time.Time { return c.t }

func newRateLimitTestStore(t *testing.T) (*Store, *fakeClock) {
	t.Helper()
	s := newTestStore(t)
	clk := &fakeClock{t: time.Now()}
	s.now = clk.now
	return s, clk
}

func mustAcquire(t *testing.T, s *Store, id string) func() {
	t.Helper()
	release, _, err := s.AcquireKey(id)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	return release
}

func TestKeyRateLimit_UnsetIsUnlimited(t *testing.T) {
	// Given a key with no rate_limit and no group defaults
	// When many requests are admitted in the same instant
	// Then all are admitted
	s, _ := newRateLimitTestStore(t)
	entry, err := s.Create("sk-cpa-unlimited", AccessKey{Name: "u"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	for i := 0; i < 100; i++ {
		mustAcquire(t, s, entry.ID)()
	}
}

func TestKeyRateLimit_RPMBucketBurstThenReject(t *testing.T) {
	// Given a key with rate_limit.rpm = N
	// When N requests arrive in the same instant
	// Then all are admitted (full bucket)
	// And the (N+1)th request is rejected with a non-zero retry-after
	s, _ := newRateLimitTestStore(t)
	entry, err := s.Create("sk-cpa-rpm", AccessKey{RateLimit: RateLimit{RPM: 3}})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	for i := 0; i < 3; i++ {
		mustAcquire(t, s, entry.ID)()
	}
	release, retryAfter, err := s.AcquireKey(entry.ID)
	if err == nil {
		release()
		t.Fatal("expected rpm rejection")
	}
	if retryAfter <= 0 {
		t.Fatalf("retry-after must be positive, got %v", retryAfter)
	}
}

func TestKeyRateLimit_RPMBucketRefills(t *testing.T) {
	// Given an exhausted RPM bucket
	// When the clock advances by 60/rpm seconds
	// Then exactly one more request is admitted
	s, clk := newRateLimitTestStore(t)
	entry, err := s.Create("sk-cpa-rpm2", AccessKey{RateLimit: RateLimit{RPM: 2}})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	mustAcquire(t, s, entry.ID)()
	mustAcquire(t, s, entry.ID)()
	if release, _, err := s.AcquireKey(entry.ID); err == nil {
		release()
		t.Fatal("bucket should be empty")
	}
	clk.t = clk.t.Add(30 * time.Second) // refill 2/60 * 30s = 1 token
	mustAcquire(t, s, entry.ID)()
	if release, _, err := s.AcquireKey(entry.ID); err == nil {
		release()
		t.Fatal("only one token should have refilled")
	}
}

func TestKeyRateLimit_RPDDailyWindow(t *testing.T) {
	// Given a key with rate_limit.rpd = N
	// When N requests are admitted on day D
	// Then the (N+1)th is rejected until the UTC day rolls over
	s, clk := newRateLimitTestStore(t)
	entry, err := s.Create("sk-cpa-rpd", AccessKey{RateLimit: RateLimit{RPD: 2}})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	mustAcquire(t, s, entry.ID)()
	mustAcquire(t, s, entry.ID)()
	if release, retryAfter, err := s.AcquireKey(entry.ID); err == nil {
		release()
		t.Fatal("expected rpd rejection")
	} else if retryAfter <= 0 {
		t.Fatal("rpd rejection must carry retry-after")
	}
	clk.t = clk.t.Add(24 * time.Hour)
	mustAcquire(t, s, entry.ID)()
}

func TestKeyRateLimit_RPDPersistedAcrossReload(t *testing.T) {
	// Given a key that consumed its RPD and a flushed store
	// When the store is reloaded from disk
	// Then the key is still rejected on the same UTC day
	dir := t.TempDir()
	clk := &fakeClock{t: time.Now()}
	s := &Store{path: dir + "/access-keys.json", now: clk.now}
	if err := s.loadLocked(); err != nil {
		t.Fatal(err)
	}
	entry, err := s.Create("sk-cpa-rpd2", AccessKey{RateLimit: RateLimit{RPD: 1}})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	mustAcquire(t, s, entry.ID)()
	if err := s.Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}
	s2 := &Store{path: s.path, now: clk.now}
	if err := s2.loadLocked(); err != nil {
		t.Fatal(err)
	}
	if release, _, err := s2.AcquireKey(entry.ID); err == nil {
		release()
		t.Fatal("rpd counter must survive reload")
	}
}

func TestKeyRateLimit_ConcurrencyAcquireRelease(t *testing.T) {
	// Given a key with rate_limit.max_concurrency = N
	// When N requests are in flight
	// Then the (N+1)th is rejected
	// And after one release the next request is admitted
	s, _ := newRateLimitTestStore(t)
	entry, err := s.Create("sk-cpa-conc", AccessKey{RateLimit: RateLimit{MaxConcurrency: 1}})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	release := mustAcquire(t, s, entry.ID)
	if rel2, _, err := s.AcquireKey(entry.ID); err == nil {
		rel2()
		t.Fatal("expected concurrency rejection")
	}
	release()
	mustAcquire(t, s, entry.ID)()
}

func TestKeyRateLimit_TPMChargesAfterUsage(t *testing.T) {
	// Given a key with rate_limit.tpm = N
	// When recorded usage debits more than N tokens
	// Then the bucket goes into deficit and the next request is rejected
	// And admission resumes once the deficit refills
	s, clk := newRateLimitTestStore(t)
	entry, err := s.Create("sk-cpa-tpm", AccessKey{RateLimit: RateLimit{TPM: 60}})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	mustAcquire(t, s, entry.ID)()
	if !s.RecordUsage("sk-cpa-tpm", UsageEvent{Tokens: 120}) {
		t.Fatal("record usage failed")
	}
	release, retryAfter, err := s.AcquireKey(entry.ID)
	if err == nil {
		release()
		t.Fatal("expected tpm rejection after overdraw")
	}
	if retryAfter <= 0 {
		t.Fatal("tpm rejection must carry retry-after")
	}
	clk.t = clk.t.Add(2 * time.Minute) // refill 120 tokens
	mustAcquire(t, s, entry.ID)()
}

func TestKeyRateLimit_GroupDefaultsApply(t *testing.T) {
	// Given a group with per_key_limits.rpm = N and a member key with no own limits
	// When the member key issues N+1 requests in one instant
	// Then the first N are admitted and the last is rejected
	s, _ := newRateLimitTestStore(t)
	if _, err := s.UpsertGroup(Group{Name: "basic", PerKeyLimits: RateLimit{RPM: 2}}); err != nil {
		t.Fatalf("upsert group: %v", err)
	}
	entry, err := s.Create("sk-cpa-member", AccessKey{Group: "basic"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	mustAcquire(t, s, entry.ID)()
	mustAcquire(t, s, entry.ID)()
	if release, _, err := s.AcquireKey(entry.ID); err == nil {
		release()
		t.Fatal("group per-key default must apply")
	}
}

func TestKeyRateLimit_KeyOverridesGroupPerField(t *testing.T) {
	// Given group per_key_limits {rpm: A, tpm: B} and a key with rate_limit.rpm = C
	// Then the key admits C rpm while tpm = B still applies
	s, _ := newRateLimitTestStore(t)
	if _, err := s.UpsertGroup(Group{Name: "pro", PerKeyLimits: RateLimit{RPM: 1, TPM: 50}}); err != nil {
		t.Fatalf("upsert group: %v", err)
	}
	entry, err := s.Create("sk-cpa-vip", AccessKey{Group: "pro", RateLimit: RateLimit{RPM: 5}})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	for i := 0; i < 5; i++ {
		mustAcquire(t, s, entry.ID)()
	}
	// Own rpm=5 overrides group rpm=1: 5 admitted in one instant.
	// Inherited tpm=50 still applies.
	if !s.RecordUsage("sk-cpa-vip", UsageEvent{Tokens: 60}) {
		t.Fatal("record usage failed")
	}
	if release, _, err := s.AcquireKey(entry.ID); err == nil {
		release()
		t.Fatal("inherited group tpm default must still apply")
	}
}

func TestKeyRateLimit_KeyNegativeOneDisablesGroupDefault(t *testing.T) {
	// Given a group with per_key_limits.rpm = N and a member key with rate_limit.rpm = -1
	// Then the member key is RPM-unlimited while other group defaults still apply
	s, _ := newRateLimitTestStore(t)
	if _, err := s.UpsertGroup(Group{Name: "team", PerKeyLimits: RateLimit{RPM: 2, MaxConcurrency: 1}}); err != nil {
		t.Fatalf("upsert group: %v", err)
	}
	entry, err := s.Create("sk-cpa-boss", AccessKey{Group: "team", RateLimit: RateLimit{RPM: -1}})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	// RPM is unlimited: 50 sequential acquires in one instant must all pass
	// (a group-default rpm of 2 would reject from the third on).
	for i := 0; i < 50; i++ {
		mustAcquire(t, s, entry.ID)()
	}
	// Concurrency default still applies: two simultaneous in-flight must fail.
	hold := mustAcquire(t, s, entry.ID)
	if rel, _, err := s.AcquireKey(entry.ID); err == nil {
		rel()
		t.Fatal("inherited group concurrency default must still apply")
	}
	hold()
}

func TestGroupRateLimit_TokenBucketShared(t *testing.T) {
	// Given a group with rate_limit_rpm = N (shared across member keys)
	// When member keys together issue N requests in one instant
	// Then the next request from any member is rejected until refill
	s, _ := newRateLimitTestStore(t)
	if _, err := s.UpsertGroup(Group{Name: "shared", RateLimitRPM: 2}); err != nil {
		t.Fatalf("upsert group: %v", err)
	}
	r1, _, err := s.AcquireGroup("shared")
	if err != nil {
		t.Fatalf("acquire 1: %v", err)
	}
	r1()
	r2, _, err := s.AcquireGroup("shared")
	if err != nil {
		t.Fatalf("acquire 2: %v", err)
	}
	r2()
	if rel, _, err := s.AcquireGroup("shared"); err == nil {
		rel()
		t.Fatal("shared group rpm bucket must reject the third instant request")
	}
}

func TestRateLimitOrder_KeyCheckedBeforeGroup(t *testing.T) {
	// Given a key over its own RPM and a group still under its shared RPM
	// When the key issues a request
	// Then the rejection names the key limit (per-key failure wins)
	s, _ := newRateLimitTestStore(t)
	if _, err := s.UpsertGroup(Group{Name: "g", RateLimitRPM: 100}); err != nil {
		t.Fatalf("upsert group: %v", err)
	}
	entry, err := s.Create("sk-cpa-order", AccessKey{Group: "g", RateLimit: RateLimit{RPM: 1}})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	mustAcquire(t, s, entry.ID)()
	_, _, err = s.AcquireKey(entry.ID)
	if err == nil || !strings.Contains(err.Error(), "key") {
		t.Fatalf("expected key-limit error, got %v", err)
	}
}

// Feature: protocol-shaped 429 responses
//
//   Clients speak different protocols; a rate-limited response must match the
//   route's native error envelope and carry a Retry-After header.

func rateLimitedRouter(t *testing.T, s *Store, path string, meta map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.POST(path, func(c *gin.Context) {
		if meta != nil {
			c.Set("accessMetadata", meta)
		}
		c.Next()
	}, GroupAccessMiddleware(s), func(c *gin.Context) {
		c.Status(http.StatusOK)
	})
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func TestRateLimitResponse_OpenAIFormat(t *testing.T) {
	// Given an OpenAI-route request (/v1/chat/completions)
	// When a limit is exceeded
	// Then the body is {"error":{"type":"rate_limit_error",...}} with Retry-After
	s, _ := newRateLimitTestStore(t)
	entry, err := s.Create("sk-cpa-fmt1", AccessKey{RateLimit: RateLimit{RPM: 1}})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	mustAcquire(t, s, entry.ID)()
	w := rateLimitedRouter(t, s, "/v1/chat/completions", map[string]string{"key_id": entry.ID})
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d", w.Code)
	}
	if w.Header().Get("Retry-After") == "" {
		t.Fatal("missing Retry-After header")
	}
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("body: %v", err)
	}
	errObj, ok := body["error"].(map[string]any)
	if !ok || errObj["type"] != "rate_limit_error" {
		t.Fatalf("not an OpenAI error envelope: %s", w.Body.String())
	}
}

func TestRateLimitResponse_ClaudeFormat(t *testing.T) {
	// Given a /v1/messages request
	// When a limit is exceeded
	// Then the body is {"type":"error","error":{"type":"rate_limit_error",...}}
	s, _ := newRateLimitTestStore(t)
	entry, err := s.Create("sk-cpa-fmt2", AccessKey{RateLimit: RateLimit{RPM: 1}})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	mustAcquire(t, s, entry.ID)()
	w := rateLimitedRouter(t, s, "/v1/messages", map[string]string{"key_id": entry.ID})
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d", w.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("body: %v", err)
	}
	errObj, ok := body["error"].(map[string]any)
	if !ok || body["type"] != "error" || errObj["type"] != "rate_limit_error" {
		t.Fatalf("not a Claude error envelope: %s", w.Body.String())
	}
}

func TestRateLimitResponse_GeminiFormat(t *testing.T) {
	// Given a /v1beta/* request
	// When a limit is exceeded
	// Then the body is {"error":{"code":429,"status":"RESOURCE_EXHAUSTED",...}}
	s, _ := newRateLimitTestStore(t)
	entry, err := s.Create("sk-cpa-fmt3", AccessKey{RateLimit: RateLimit{RPM: 1}})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	mustAcquire(t, s, entry.ID)()
	w := rateLimitedRouter(t, s, "/v1beta/models/gemini-pro:generateContent", map[string]string{"key_id": entry.ID})
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d", w.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("body: %v", err)
	}
	errObj, ok := body["error"].(map[string]any)
	if !ok || errObj["status"] != "RESOURCE_EXHAUSTED" {
		t.Fatalf("not a Gemini error envelope: %s", w.Body.String())
	}
}
