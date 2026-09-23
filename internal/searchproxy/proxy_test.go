package searchproxy

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
)

// Feature: REST reverse proxy /search/{provider}/{path...}
//   Downstream auth is already done by CPA middleware before this handler runs.
//   Fake upstreams are httptest servers.

const testDownstreamKey = "cpa-downstream-key"

type capturedRequest struct {
	Method   string
	Path     string
	RawQuery string
	Header   http.Header
	Body     string
}

type fakeUpstream struct {
	*httptest.Server
	mu       sync.Mutex
	requests []capturedRequest
}

func newFakeUpstream(t *testing.T, handler func(w http.ResponseWriter, r *http.Request, body string)) *fakeUpstream {
	t.Helper()
	up := &fakeUpstream{}
	up.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		up.mu.Lock()
		up.requests = append(up.requests, capturedRequest{
			Method: r.Method, Path: r.URL.Path, RawQuery: r.URL.RawQuery, Header: r.Header.Clone(), Body: string(raw),
		})
		up.mu.Unlock()
		handler(w, r, string(raw))
	}))
	t.Cleanup(up.Close)
	return up
}

func (u *fakeUpstream) Requests() []capturedRequest {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]capturedRequest(nil), u.requests...)
}

func writeJSON(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, body)
}

// upstreamKey returns whichever upstream key the request carried, independent of provider style.
func upstreamKey(r *http.Request) string {
	if v := r.Header.Get("X-Api-Key"); v != "" {
		return v
	}
	return strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
}

type testHarness struct {
	svc     *Service
	clock   *fakeClock
	engine  *gin.Engine
	mu      sync.Mutex
	records []coreusage.Record
}

func newHarness(t *testing.T, keys ...config.SearchKey) *testHarness {
	t.Helper()
	gin.SetMode(gin.TestMode)
	h := &testHarness{clock: &fakeClock{t: time.Date(2026, 9, 23, 0, 0, 0, 0, time.UTC)}}
	h.svc = NewService(h.clock.Now)
	h.svc.SetUsagePublisher(func(_ context.Context, rec coreusage.Record) {
		h.mu.Lock()
		h.records = append(h.records, rec)
		h.mu.Unlock()
	})
	h.svc.UpdateConfig(&config.Config{SearchKey: keys})
	h.engine = gin.New()
	h.engine.Any("/search/*path", func(c *gin.Context) {
		c.Set("userApiKey", testDownstreamKey)
		c.Next()
	}, h.svc.Handle)
	return h
}

func (h *testHarness) do(method, target, body string, headers map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.engine.ServeHTTP(rec, req)
	return rec
}

func (h *testHarness) usageRecords() []coreusage.Record {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]coreusage.Record(nil), h.records...)
}

// Scenario: Forward path, query, method and body to the provider base URL
//
//	Given a tavily key pool pointing at a fake upstream
//	When POST /search/tavily/search?x=1 with a JSON body is proxied
//	Then the upstream receives POST /search?x=1 with the same body
//	And the client receives the upstream status, content-type and body unchanged
func TestProxyForwardsRequestToProviderBaseURL(t *testing.T) {
	up := newFakeUpstream(t, func(w http.ResponseWriter, _ *http.Request, _ string) {
		w.Header().Set("X-Upstream", "yes")
		writeJSON(w, http.StatusCreated, `{"results":[1]}`)
	})
	h := newHarness(t, config.SearchKey{Provider: "tavily", APIKey: "tvly-up", BaseURL: up.URL})

	rec := h.do(http.MethodPost, "/search/tavily/search?x=1", `{"query":"q"}`, nil)

	if rec.Code != http.StatusCreated || rec.Body.String() != `{"results":[1]}` {
		t.Fatalf("response = %d %s", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("Content-Type") != "application/json" || rec.Header().Get("X-Upstream") != "yes" {
		t.Fatalf("response headers = %v", rec.Header())
	}
	reqs := up.Requests()
	if len(reqs) != 1 {
		t.Fatalf("upstream requests = %d, want 1", len(reqs))
	}
	got := reqs[0]
	if got.Method != http.MethodPost || got.Path != "/search" || got.RawQuery != "x=1" || got.Body != `{"query":"q"}` {
		t.Fatalf("upstream request = %#v", got)
	}
}

// Scenario: Firecrawl versioned paths are forwarded verbatim
//
//	When POST /search/firecrawl/v2/scrape is proxied
//	Then the upstream receives POST /v2/scrape
func TestProxyForwardsFirecrawlVersionedPath(t *testing.T) {
	up := newFakeUpstream(t, func(w http.ResponseWriter, _ *http.Request, _ string) {
		writeJSON(w, http.StatusOK, `{"success":true}`)
	})
	h := newHarness(t, config.SearchKey{Provider: "firecrawl", APIKey: "fc-up", BaseURL: up.URL})

	rec := h.do(http.MethodPost, "/search/firecrawl/v2/scrape", `{"url":"https://example.com"}`, nil)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if reqs := up.Requests(); len(reqs) != 1 || reqs[0].Path != "/v2/scrape" {
		t.Fatalf("upstream requests = %#v", reqs)
	}
}

// Scenario Outline: Downstream credentials are stripped and the pooled key is injected
//
//	Given the client authenticated with a CPA key via <location>
//	When the request is proxied to <provider>
//	Then the upstream sees no CPA key in headers, query or body
//	And the upstream sees the pooled key as <injection>
//	Examples:
//	  | provider  | location                  | injection                                    |
//	  | tavily    | Authorization Bearer      | Authorization Bearer + body api_key replaced |
//	  | tavily    | body api_key              | Authorization Bearer + body api_key replaced |
//	  | exa       | X-Api-Key                 | x-api-key                                    |
//	  | firecrawl | Authorization Bearer      | Authorization Bearer                         |
//	  | any       | query key / auth_token    | query params removed                         |
func TestProxyStripsDownstreamCredentialsAndInjectsPooledKey(t *testing.T) {
	cases := []struct {
		provider   string
		upKey      string
		headers    map[string]string
		body       string
		wantAuth   string
		wantXKey   string
		wantBodyAK string
	}{
		{"tavily", "tvly-up", map[string]string{"Authorization": "Bearer " + testDownstreamKey}, `{"query":"q","api_key":"` + testDownstreamKey + `"}`, "Bearer tvly-up", "", "tvly-up"},
		{"tavily", "tvly-up", nil, `{"api_key":"` + testDownstreamKey + `","query":"q"}`, "Bearer tvly-up", "", "tvly-up"},
		{"exa", "exa-up", map[string]string{"X-Api-Key": testDownstreamKey}, `{"query":"q","api_key":"` + testDownstreamKey + `"}`, "", "exa-up", ""},
		{"firecrawl", "fc-up", map[string]string{"Authorization": "Bearer " + testDownstreamKey, "X-Goog-Api-Key": testDownstreamKey}, `{"url":"u"}`, "Bearer fc-up", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.provider, func(t *testing.T) {
			up := newFakeUpstream(t, func(w http.ResponseWriter, _ *http.Request, _ string) {
				writeJSON(w, http.StatusOK, `{}`)
			})
			h := newHarness(t, config.SearchKey{Provider: tc.provider, APIKey: tc.upKey, BaseURL: up.URL})

			rec := h.do(http.MethodPost, "/search/"+tc.provider+"/search?key="+testDownstreamKey+"&auth_token="+testDownstreamKey+"&keep=1", tc.body, tc.headers)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
			}
			got := up.Requests()[0]
			if got.RawQuery != "keep=1" {
				t.Fatalf("query = %q, want keep=1", got.RawQuery)
			}
			if got.Header.Get("Authorization") != tc.wantAuth || got.Header.Get("X-Api-Key") != tc.wantXKey {
				t.Fatalf("auth headers = %q / %q", got.Header.Get("Authorization"), got.Header.Get("X-Api-Key"))
			}
			var body map[string]any
			if err := json.Unmarshal([]byte(got.Body), &body); err != nil {
				t.Fatalf("upstream body not JSON: %v", err)
			}
			if ak, _ := body["api_key"].(string); ak != tc.wantBodyAK {
				t.Fatalf("body api_key = %q, want %q", ak, tc.wantBodyAK)
			}
			for name, values := range got.Header {
				for _, v := range values {
					if strings.Contains(v, testDownstreamKey) {
						t.Fatalf("header %s leaks downstream key", name)
					}
				}
			}
			if strings.Contains(got.Body, testDownstreamKey) || strings.Contains(got.RawQuery, testDownstreamKey) {
				t.Fatalf("downstream key leaked: query=%q body=%s", got.RawQuery, got.Body)
			}
		})
	}
}

// Scenario: Rotate to the next key on a rotate-class failure
//
//	Given tavily keys A and B, and the upstream answers 432 for A and 200 for B
//	When a search is proxied
//	Then the client receives B's 200 response
//	And A is cooled down with the quota default
func TestProxyRotatesKeyOnQuotaFailure(t *testing.T) {
	up := newFakeUpstream(t, func(w http.ResponseWriter, r *http.Request, _ string) {
		if upstreamKey(r) == "A" {
			writeJSON(w, 432, `{"detail":"plan limit"}`)
			return
		}
		writeJSON(w, http.StatusOK, `{"from":"B"}`)
	})
	h := newHarness(t,
		config.SearchKey{Provider: "tavily", APIKey: "A", BaseURL: up.URL},
		config.SearchKey{Provider: "tavily", APIKey: "B", BaseURL: up.URL},
	)

	rec := h.do(http.MethodPost, "/search/tavily/search", `{"query":"q"}`, nil)

	if rec.Code != http.StatusOK || rec.Body.String() != `{"from":"B"}` {
		t.Fatalf("response = %d %s", rec.Code, rec.Body.String())
	}
	for _, st := range h.svc.Pool().Snapshot() {
		if st.ID == KeyID("tavily", "A") && !st.CooldownUntil.Equal(h.clock.Now().Add(QuotaCooldown)) {
			t.Fatalf("A cooldown = %v, want %v", st.CooldownUntil, h.clock.Now().Add(QuotaCooldown))
		}
	}
}

// Scenario: Pass-class failures are returned without rotation
//
//	Given the exa upstream answers 400 for key A
//	When a search is proxied
//	Then the client receives the 400 as-is and only A was tried
func TestProxyDoesNotRotateOnPassFailure(t *testing.T) {
	up := newFakeUpstream(t, func(w http.ResponseWriter, _ *http.Request, _ string) {
		writeJSON(w, http.StatusBadRequest, `{"error":"bad query"}`)
	})
	h := newHarness(t,
		config.SearchKey{Provider: "exa", APIKey: "A", BaseURL: up.URL},
		config.SearchKey{Provider: "exa", APIKey: "B", BaseURL: up.URL},
	)

	rec := h.do(http.MethodPost, "/search/exa/search", `{"query":""}`, nil)

	if rec.Code != http.StatusBadRequest || rec.Body.String() != `{"error":"bad query"}` {
		t.Fatalf("response = %d %s", rec.Code, rec.Body.String())
	}
	if reqs := up.Requests(); len(reqs) != 1 || upstreamKey(&http.Request{Header: reqs[0].Header}) != "A" {
		t.Fatalf("upstream requests = %d, want only A", len(reqs))
	}
}

// Scenario: Every key fails with a rotate-class status
//
//	Given firecrawl keys A and B both answer 429
//	When a scrape is proxied
//	Then the client receives the last upstream 429 response
//	And both keys are cooled down
func TestProxyReturnsLastFailureWhenAllKeysRotate(t *testing.T) {
	up := newFakeUpstream(t, func(w http.ResponseWriter, r *http.Request, _ string) {
		writeJSON(w, http.StatusTooManyRequests, `{"error":"rate limited `+upstreamKey(r)+`"}`)
	})
	h := newHarness(t,
		config.SearchKey{Provider: "firecrawl", APIKey: "A", BaseURL: up.URL},
		config.SearchKey{Provider: "firecrawl", APIKey: "B", BaseURL: up.URL},
	)

	rec := h.do(http.MethodPost, "/search/firecrawl/v2/scrape", `{"url":"u"}`, nil)

	if rec.Code != http.StatusTooManyRequests || rec.Body.String() != `{"error":"rate limited B"}` {
		t.Fatalf("response = %d %s", rec.Code, rec.Body.String())
	}
	for _, st := range h.svc.Pool().Snapshot() {
		if st.CooldownUntil.IsZero() {
			t.Fatalf("key %s not cooled down", st.ID)
		}
	}
}

// Scenario: No available key
//
//	Given every tavily key is cooling down
//	When a search is proxied
//	Then the client receives 503 with a JSON error and Retry-After of the earliest recovery
func TestProxyReturns503WhenNoKeyAvailable(t *testing.T) {
	h := newHarness(t, config.SearchKey{Provider: "tavily", APIKey: "A", BaseURL: "http://127.0.0.1:1"})
	h.svc.Pool().Cooldown(KeyID("tavily", "A"), 90*time.Second, 432)

	rec := h.do(http.MethodPost, "/search/tavily/search", `{"query":"q"}`, nil)

	if rec.Code != http.StatusServiceUnavailable || rec.Header().Get("Retry-After") != "90" {
		t.Fatalf("response = %d retry-after=%q", rec.Code, rec.Header().Get("Retry-After"))
	}
	var payload map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil || payload["error"] == nil {
		t.Fatalf("body = %s, want JSON error", rec.Body.String())
	}
}

// Scenario: Unknown or unconfigured provider
//
//	When /search/unknown/x or /search/exa/search (no exa keys) is requested
//	Then the client receives 404 with a JSON error
func TestProxyRejectsUnknownOrUnconfiguredProvider(t *testing.T) {
	h := newHarness(t, config.SearchKey{Provider: "tavily", APIKey: "A", BaseURL: "http://127.0.0.1:1"})

	for _, target := range []string{"/search/unknown/x", "/search/exa/search", "/search/tavily"} {
		rec := h.do(http.MethodPost, target, `{}`, nil)
		var payload map[string]any
		if rec.Code != http.StatusNotFound || json.Unmarshal(rec.Body.Bytes(), &payload) != nil || payload["error"] == nil {
			t.Fatalf("%s = %d %s, want 404 JSON error", target, rec.Code, rec.Body.String())
		}
	}
}

// Scenario: Streaming responses are relayed incrementally
//
//	Given the tavily upstream streams SSE chunks for /research with stream=true
//	When the request is proxied
//	Then each chunk is flushed to the client as it arrives
func TestProxyStreamsSSEResponses(t *testing.T) {
	release := make(chan struct{})
	up := newFakeUpstream(t, func(w http.ResponseWriter, _ *http.Request, _ string) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "data: first\n\n")
		w.(http.Flusher).Flush()
		<-release
		_, _ = io.WriteString(w, "data: second\n\n")
	})
	h := newHarness(t, config.SearchKey{Provider: "tavily", APIKey: "A", BaseURL: up.URL})
	server := httptest.NewServer(h.engine)
	defer server.Close()

	resp, err := http.Post(server.URL+"/search/tavily/research", "application/json", strings.NewReader(`{"input":"q","stream":true}`))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	reader := bufio.NewReader(resp.Body)

	line, err := reader.ReadString('\n')
	if err != nil || line != "data: first\n" {
		t.Fatalf("first line = %q err=%v", line, err)
	}
	close(release)
	rest, _ := io.ReadAll(reader)
	if !strings.Contains(string(rest), "data: second") {
		t.Fatalf("rest = %q", rest)
	}
}

// Scenario: Async job polling sticks to the key that created the job
//
//	Given firecrawl keys A and B, and POST /v2/crawl via key A returns {"id":"job-1"}
//	When GET /search/firecrawl/v2/crawl/job-1 is proxied
//	Then the upstream receives the GET with key A even if round-robin would pick B
func TestProxyJobPollingUsesCreatingKey(t *testing.T) {
	up := newFakeUpstream(t, func(w http.ResponseWriter, r *http.Request, _ string) {
		if r.Method == http.MethodPost {
			writeJSON(w, http.StatusOK, `{"success":true,"id":"job-1"}`)
			return
		}
		writeJSON(w, http.StatusOK, `{"status":"completed","key":"`+upstreamKey(r)+`"}`)
	})
	h := newHarness(t,
		config.SearchKey{Provider: "firecrawl", APIKey: "A", BaseURL: up.URL},
		config.SearchKey{Provider: "firecrawl", APIKey: "B", BaseURL: up.URL},
	)

	create := h.do(http.MethodPost, "/search/firecrawl/v2/crawl", `{"url":"u"}`, nil)
	if create.Code != http.StatusOK || create.Body.String() != `{"success":true,"id":"job-1"}` {
		t.Fatalf("create = %d %s", create.Code, create.Body.String())
	}
	for i := 0; i < 2; i++ {
		poll := h.do(http.MethodGet, "/search/firecrawl/v2/crawl/job-1", "", nil)
		if !strings.Contains(poll.Body.String(), `"key":"A"`) {
			t.Fatalf("poll %d = %s, want served by key A", i, poll.Body.String())
		}
	}
}

// Scenario Outline: Job ids are captured from each provider's create response
//
//	Examples:
//	  | provider  | create request        | id field     | poll request                  |
//	  | tavily    | POST /research        | request_id   | GET /research/{id}            |
//	  | firecrawl | POST /v2/batch/scrape | id           | GET/DELETE /v2/batch/scrape/{id} |
//	  | exa       | POST /agent/runs      | id           | GET /agent/runs/{id}          |
func TestProxyCapturesJobIDsPerProvider(t *testing.T) {
	cases := []struct {
		provider, createPath, createResp, pollMethod, pollPath string
	}{
		{"tavily", "/research", `{"request_id":"r-1","status":"pending"}`, http.MethodGet, "/research/r-1"},
		{"firecrawl", "/v2/batch/scrape", `{"success":true,"id":"b-1"}`, http.MethodDelete, "/v2/batch/scrape/b-1"},
		{"exa", "/agent/runs", `{"id":"run-1","status":"running"}`, http.MethodGet, "/agent/runs/run-1"},
	}
	for _, tc := range cases {
		t.Run(tc.provider, func(t *testing.T) {
			up := newFakeUpstream(t, func(w http.ResponseWriter, r *http.Request, _ string) {
				if r.Method == http.MethodPost {
					writeJSON(w, http.StatusOK, tc.createResp)
					return
				}
				writeJSON(w, http.StatusOK, `{"key":"`+upstreamKey(r)+`"}`)
			})
			h := newHarness(t,
				config.SearchKey{Provider: tc.provider, APIKey: "A", BaseURL: up.URL},
				config.SearchKey{Provider: tc.provider, APIKey: "B", BaseURL: up.URL},
			)

			if rec := h.do(http.MethodPost, "/search/"+tc.provider+tc.createPath, `{}`, nil); rec.Code != http.StatusOK {
				t.Fatalf("create = %d", rec.Code)
			}
			poll := h.do(tc.pollMethod, "/search/"+tc.provider+tc.pollPath, "", nil)
			if poll.Body.String() != `{"key":"A"}` {
				t.Fatalf("poll = %s, want key A", poll.Body.String())
			}
		})
	}
}

// Scenario: Job affinity for a removed key
//
//	Given job-1 was created with key A and A is then removed from config
//	When the job is polled
//	Then the client receives 409 with an error explaining the owning key is gone
func TestProxyJobPollingFailsWhenOwningKeyRemoved(t *testing.T) {
	up := newFakeUpstream(t, func(w http.ResponseWriter, _ *http.Request, _ string) {
		writeJSON(w, http.StatusOK, `{"id":"job-1"}`)
	})
	h := newHarness(t, config.SearchKey{Provider: "firecrawl", APIKey: "A", BaseURL: up.URL})
	h.do(http.MethodPost, "/search/firecrawl/v2/crawl", `{"url":"u"}`, nil)

	h.svc.UpdateConfig(&config.Config{SearchKey: []config.SearchKey{{Provider: "firecrawl", APIKey: "B", BaseURL: up.URL}}})
	rec := h.do(http.MethodGet, "/search/firecrawl/v2/crawl/job-1", "", nil)

	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "error") {
		t.Fatalf("response = %d %s, want 409 JSON error", rec.Code, rec.Body.String())
	}
	if len(up.Requests()) != 1 {
		t.Fatalf("poll should not reach upstream")
	}
}

// Scenario: Each proxied call publishes one usage record
//
//	When a search is proxied successfully (or fails)
//	Then one usage record is published with provider, model "search/<provider>", the CPA api key,
//	the upstream key id, latency and failed flag
func TestProxyPublishesUsageRecord(t *testing.T) {
	up := newFakeUpstream(t, func(w http.ResponseWriter, r *http.Request, body string) {
		if strings.Contains(body, "bad") {
			writeJSON(w, http.StatusBadRequest, `{}`)
			return
		}
		writeJSON(w, http.StatusOK, `{}`)
	})
	h := newHarness(t, config.SearchKey{Provider: "exa", APIKey: "A", BaseURL: up.URL})

	h.do(http.MethodPost, "/search/exa/search", `{"query":"ok"}`, nil)
	h.do(http.MethodPost, "/search/exa/search", `{"query":"bad"}`, nil)

	records := h.usageRecords()
	if len(records) != 2 {
		t.Fatalf("records = %d, want 2", len(records))
	}
	for i, rec := range records {
		if rec.Provider != "exa" || rec.Model != "search/exa" || rec.APIKey != testDownstreamKey ||
			rec.AuthID != KeyID("exa", "A") || rec.RequestedAt.IsZero() || rec.Failed != (i == 1) {
			t.Fatalf("record %d = %#v", i, rec)
		}
	}
}

// Scenario: Per-key proxy-url is honored
//
//	Given key A has proxy-url set
//	When a request is proxied with key A
//	Then the upstream request goes through that proxy
func TestProxyHonorsPerKeyProxyURL(t *testing.T) {
	up := newFakeUpstream(t, func(w http.ResponseWriter, _ *http.Request, _ string) {
		writeJSON(w, http.StatusOK, `{"via":"direct"}`)
	})
	var proxied []string
	var mu sync.Mutex
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		proxied = append(proxied, r.URL.String())
		mu.Unlock()
		writeJSON(w, http.StatusOK, `{"via":"proxy"}`)
	}))
	defer proxy.Close()
	h := newHarness(t, config.SearchKey{Provider: "tavily", APIKey: "A", BaseURL: up.URL, ProxyURL: proxy.URL})

	rec := h.do(http.MethodPost, "/search/tavily/search", `{"query":"q"}`, nil)

	if rec.Body.String() != `{"via":"proxy"}` {
		t.Fatalf("response = %s, want via proxy", rec.Body.String())
	}
	mu.Lock()
	defer mu.Unlock()
	if len(proxied) != 1 || !strings.HasPrefix(proxied[0], up.URL+"/search") {
		t.Fatalf("proxy saw %v", proxied)
	}
	if len(up.Requests()) != 0 {
		t.Fatalf("upstream contacted directly")
	}
}
