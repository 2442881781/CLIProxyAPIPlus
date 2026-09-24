package searchproxy

import (
	"context"
	"math"
	"net/http"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

// Feature: Per-key quota tracking for search providers
//   Tavily and Firecrawl expose live quota endpoints that cost no credits.
//   Exa has no balance API, so spend is accumulated from response costDollars.total
//   and compared against an optional per-key USD budget.

func statusOf(t *testing.T, h *testHarness, provider, apiKey string) KeyStatus {
	t.Helper()
	id := KeyID(provider, apiKey)
	for _, st := range h.svc.Pool().Snapshot() {
		if st.ID == id {
			return st
		}
	}
	t.Fatalf("status for %s not found", id)
	return KeyStatus{}
}

func floatVal(t *testing.T, name string, v *float64) float64 {
	t.Helper()
	if v == nil {
		t.Fatalf("%s is nil", name)
	}
	return *v
}

func approx(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

// Scenario: Tavily quota comes from GET /usage
//
//	Given a tavily key whose /usage reports key.usage=150, key.limit=1000,
//	  account.plan_usage=500, account.plan_limit=15000, paygo 0/0, current_plan "Bootstrap"
//	When the quota is refreshed
//	Then the key quota is remaining=850 of limit=1000 credits, plan "Bootstrap", with the check time
//	And the request carried the pooled key as Bearer auth
func TestQuotaTavilyUsesKeyLimitWhenSet(t *testing.T) {
	up := newFakeUpstream(t, func(w http.ResponseWriter, _ *http.Request, _ string) {
		writeJSON(w, http.StatusOK, `{"key":{"usage":150,"limit":1000},"account":{"current_plan":"Bootstrap","plan_usage":500,"plan_limit":15000,"paygo_usage":0,"paygo_limit":0}}`)
	})
	h := newHarness(t, config.SearchKey{Provider: "tavily", APIKey: "tvly-a", BaseURL: up.URL})

	h.svc.RefreshQuotas(context.Background(), nil)

	reqs := up.Requests()
	if len(reqs) != 1 || reqs[0].Method != http.MethodGet || reqs[0].Path != "/usage" || reqs[0].Header.Get("Authorization") != "Bearer tvly-a" {
		t.Fatalf("quota requests = %#v", reqs)
	}
	q := statusOf(t, h, "tavily", "tvly-a").Quota
	if q == nil || q.Unit != QuotaUnitCredits || q.Plan != "Bootstrap" || !q.CheckedAt.Equal(h.clock.Now()) || q.Error != "" {
		t.Fatalf("quota = %#v", q)
	}
	if !approx(floatVal(t, "remaining", q.Remaining), 850) || !approx(floatVal(t, "limit", q.Limit), 1000) || !approx(floatVal(t, "used", q.Used), 150) {
		t.Fatalf("quota numbers = %v/%v/%v", *q.Used, *q.Limit, *q.Remaining)
	}
}

// Scenario: Tavily key without its own limit falls back to the account plan
//
//	Given /usage reports key.limit=null, plan 14900/15000 used and paygo 20/100
//	When the quota is refreshed
//	Then remaining = (15000-14900) + (100-20) = 180 of limit 15100 credits
func TestQuotaTavilyFallsBackToAccountPlanAndPaygo(t *testing.T) {
	up := newFakeUpstream(t, func(w http.ResponseWriter, _ *http.Request, _ string) {
		writeJSON(w, http.StatusOK, `{"key":{"usage":14920,"limit":null},"account":{"current_plan":"Researcher","plan_usage":14900,"plan_limit":15000,"paygo_usage":20,"paygo_limit":100}}`)
	})
	h := newHarness(t, config.SearchKey{Provider: "tavily", APIKey: "tvly-a", BaseURL: up.URL})

	h.svc.RefreshQuotas(context.Background(), nil)

	q := statusOf(t, h, "tavily", "tvly-a").Quota
	if q == nil || !approx(floatVal(t, "remaining", q.Remaining), 180) || !approx(floatVal(t, "limit", q.Limit), 15100) || !approx(floatVal(t, "used", q.Used), 14920) {
		t.Fatalf("quota = %#v", q)
	}
}

// Scenario: Firecrawl quota comes from GET /v2/team/credit-usage
//
//	Given the endpoint reports remainingCredits=1200, planCredits=3000, billingPeriodEnd=2026-10-01
//	When the quota is refreshed
//	Then remaining=1200 of limit=3000 credits, resetting at 2026-10-01
func TestQuotaFirecrawlCreditUsage(t *testing.T) {
	up := newFakeUpstream(t, func(w http.ResponseWriter, _ *http.Request, _ string) {
		writeJSON(w, http.StatusOK, `{"success":true,"data":{"remainingCredits":1200,"planCredits":3000,"billingPeriodStart":"2026-09-01T00:00:00Z","billingPeriodEnd":"2026-10-01T00:00:00Z"}}`)
	})
	h := newHarness(t, config.SearchKey{Provider: "firecrawl", APIKey: "fc-a", BaseURL: up.URL})

	h.svc.RefreshQuotas(context.Background(), nil)

	if reqs := up.Requests(); len(reqs) != 1 || reqs[0].Path != "/v2/team/credit-usage" || reqs[0].Header.Get("Authorization") != "Bearer fc-a" {
		t.Fatalf("quota requests = %#v", reqs)
	}
	q := statusOf(t, h, "firecrawl", "fc-a").Quota
	wantReset := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	if q == nil || q.Unit != QuotaUnitCredits || !q.ResetAt.Equal(wantReset) ||
		!approx(floatVal(t, "remaining", q.Remaining), 1200) || !approx(floatVal(t, "limit", q.Limit), 3000) || !approx(floatVal(t, "used", q.Used), 1800) {
		t.Fatalf("quota = %#v", q)
	}
}

// Scenario: Quota fetch failure is recorded without touching routing
//
//	Given the quota endpoint answers 500 (or unreachable, or a self-hosted Firecrawl without the endpoint)
//	When the quota is refreshed
//	Then the key keeps its previous quota values, stores the error message and check time
//	And the key stays eligible for picking
func TestQuotaFetchFailureKeepsPreviousValues(t *testing.T) {
	var fail atomic.Bool
	up := newFakeUpstream(t, func(w http.ResponseWriter, _ *http.Request, _ string) {
		if fail.Load() {
			writeJSON(w, http.StatusInternalServerError, `{"error":"boom"}`)
			return
		}
		writeJSON(w, http.StatusOK, `{"data":{"remainingCredits":0,"planCredits":500}}`)
	})
	h := newHarness(t,
		config.SearchKey{Provider: "firecrawl", APIKey: "fc-a", BaseURL: up.URL},
		config.SearchKey{Provider: "firecrawl", APIKey: "fc-b", BaseURL: "http://127.0.0.1:1"},
	)
	h.svc.RefreshQuotas(context.Background(), []string{KeyID("firecrawl", "fc-a")})
	fail.Store(true)
	h.clock.Advance(time.Minute)

	h.svc.RefreshQuotas(context.Background(), nil)

	a := statusOf(t, h, "firecrawl", "fc-a").Quota
	if a == nil || !approx(floatVal(t, "remaining", a.Remaining), 0) || !strings.Contains(a.Error, "500") || !a.CheckedAt.Equal(h.clock.Now()) {
		t.Fatalf("fc-a quota = %#v", a)
	}
	b := statusOf(t, h, "firecrawl", "fc-b")
	if b.Quota == nil || b.Quota.Error == "" || b.Quota.Remaining != nil || b.Exhausted {
		t.Fatalf("fc-b status = %#v", b)
	}
	if got := mustPick(t, h.svc.Pool(), "firecrawl", nil).APIKey; got != "fc-b" {
		t.Fatalf("pick = %s, want fc-b (fc-a exhausted, fc-b unknown quota)", got)
	}
}

// Scenario: Exhausted quota removes the key from rotation
//
//	Given tavily keys A and B, and A's refresh reports remaining=0
//	When picks are made
//	Then only B is returned
func TestQuotaExhaustedKeyIsSkipped(t *testing.T) {
	up := newFakeUpstream(t, func(w http.ResponseWriter, r *http.Request, _ string) {
		if upstreamKey(r) == "A" {
			writeJSON(w, http.StatusOK, `{"key":{"usage":1000,"limit":1000},"account":{"plan_usage":1000,"plan_limit":1000}}`)
			return
		}
		writeJSON(w, http.StatusOK, `{"key":{"usage":1,"limit":1000},"account":{"plan_usage":1,"plan_limit":1000}}`)
	})
	h := newHarness(t,
		config.SearchKey{Provider: "tavily", APIKey: "A", BaseURL: up.URL},
		config.SearchKey{Provider: "tavily", APIKey: "B", BaseURL: up.URL},
	)

	h.svc.RefreshQuotas(context.Background(), nil)

	if !statusOf(t, h, "tavily", "A").Exhausted || statusOf(t, h, "tavily", "B").Exhausted {
		t.Fatal("exhausted flags wrong")
	}
	for i := 0; i < 3; i++ {
		if got := mustPick(t, h.svc.Pool(), "tavily", nil).APIKey; got != "B" {
			t.Fatalf("pick %d = %s, want B", i, got)
		}
	}
}

// Scenario: Refilled quota restores a key cooled down for quota reasons
//
//	Given key A was cooled down after a 432 and marked exhausted
//	When a later refresh reports remaining>0
//	Then A is eligible immediately (quota cooldown cleared)
//	But a key cooled down for 429 rate limiting keeps its cooldown
func TestQuotaRefillClearsQuotaCooldownOnly(t *testing.T) {
	up := newFakeUpstream(t, func(w http.ResponseWriter, _ *http.Request, _ string) {
		writeJSON(w, http.StatusOK, `{"key":{"usage":1,"limit":1000},"account":{"plan_usage":1,"plan_limit":1000}}`)
	})
	h := newHarness(t,
		config.SearchKey{Provider: "tavily", APIKey: "A", BaseURL: up.URL},
		config.SearchKey{Provider: "tavily", APIKey: "R", BaseURL: up.URL},
	)
	pool := h.svc.Pool()
	pool.Cooldown(KeyID("tavily", "A"), QuotaCooldown, 432)
	pool.Cooldown(KeyID("tavily", "R"), RateLimitCooldown, 429)

	h.svc.RefreshQuotas(context.Background(), nil)

	if statusOf(t, h, "tavily", "A").CooldownUntil.IsZero() == false {
		t.Fatal("quota cooldown for A should be cleared")
	}
	if statusOf(t, h, "tavily", "R").CooldownUntil.IsZero() {
		t.Fatal("rate-limit cooldown for R should remain")
	}
	if got := mustPick(t, pool, "tavily", nil).APIKey; got != "A" {
		t.Fatalf("pick = %s, want A", got)
	}
}

// Scenario: Exa spend accumulates from REST and MCP responses
//
//	Given an exa key with budget $10
//	When two proxied /search calls return costDollars.total 0.005 and 0.012 (one via REST, one via MCP)
//	Then the key quota shows spent $0.017, remaining $9.983 of $10 (unit usd)
func TestExaSpendAccumulatesFromResponses(t *testing.T) {
	var calls atomic.Int32
	up := newFakeUpstream(t, func(w http.ResponseWriter, _ *http.Request, _ string) {
		if calls.Add(1) == 1 {
			writeJSON(w, http.StatusOK, `{"results":[],"costDollars":{"total":0.005,"search":{"neural":0.005}}}`)
			return
		}
		writeJSON(w, http.StatusOK, `{"results":[],"costDollars":{"total":0.012}}`)
	})
	h := newHarness(t, config.SearchKey{Provider: "exa", APIKey: "exa-a", BaseURL: up.URL, Budget: 10})

	if rec := h.do(http.MethodPost, "/search/exa/search", `{"query":"q"}`, nil); rec.Code != http.StatusOK {
		t.Fatalf("rest status = %d", rec.Code)
	}
	if res := h.callTool(t, "exa_search", `{"query":"q"}`); res.IsError {
		t.Fatalf("mcp result = %#v", res)
	}

	q := statusOf(t, h, "exa", "exa-a").Quota
	if q == nil || q.Unit != QuotaUnitUSD || !approx(floatVal(t, "used", q.Used), 0.017) ||
		!approx(floatVal(t, "limit", q.Limit), 10) || !approx(floatVal(t, "remaining", q.Remaining), 9.983) {
		t.Fatalf("quota = %#v", q)
	}
}

// Scenario: Exa key over budget is skipped
//
//	Given an exa key with budget $0.01 that has spent $0.012
//	When a pick is made
//	Then the key is not eligible; raising the budget in config makes it eligible again
func TestExaOverBudgetKeyIsSkipped(t *testing.T) {
	h := newHarness(t, config.SearchKey{Provider: "exa", APIKey: "exa-a", Budget: 0.01})
	h.svc.Pool().AddSpend(KeyID("exa", "exa-a"), 0.012)

	if _, err := h.svc.Pool().Pick("exa", nil); err == nil {
		t.Fatal("over-budget key should not be picked")
	}
	if !statusOf(t, h, "exa", "exa-a").Exhausted {
		t.Fatal("over-budget key should be exhausted")
	}

	h.svc.UpdateConfig(&config.Config{SearchKey: []config.SearchKey{{Provider: "exa", APIKey: "exa-a", Budget: 1}}})
	if got := mustPick(t, h.svc.Pool(), "exa", nil).APIKey; got != "exa-a" {
		t.Fatalf("pick = %s after raising budget", got)
	}
}

// Scenario: Exa key without budget only reports spend
//
//	Given an exa key with no budget
//	Then its quota shows spent only, no remaining/limit, and it is never skipped for spend
func TestExaWithoutBudgetReportsSpendOnly(t *testing.T) {
	h := newHarness(t, config.SearchKey{Provider: "exa", APIKey: "exa-a"})
	h.svc.Pool().AddSpend(KeyID("exa", "exa-a"), 42)

	st := statusOf(t, h, "exa", "exa-a")
	if st.Quota == nil || !approx(floatVal(t, "used", st.Quota.Used), 42) || st.Quota.Limit != nil || st.Quota.Remaining != nil || st.Exhausted {
		t.Fatalf("status = %#v", st)
	}
	mustPick(t, h.svc.Pool(), "exa", nil)
}

// Scenario: Exa spend resets at the start of each UTC month
//
//	Given an exa key spent $3 in 2026-09
//	When the clock moves to 2026-10-01T00:00Z
//	Then spent is $0 for the new month
func TestExaSpendResetsMonthly(t *testing.T) {
	h := newHarness(t, config.SearchKey{Provider: "exa", APIKey: "exa-a", Budget: 10})
	h.svc.Pool().AddSpend(KeyID("exa", "exa-a"), 3)

	h.clock.Set(time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC))

	q := statusOf(t, h, "exa", "exa-a").Quota
	if q == nil || !approx(floatVal(t, "used", q.Used), 0) || !approx(floatVal(t, "remaining", q.Remaining), 10) {
		t.Fatalf("quota after month change = %#v", q)
	}
	wantReset := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	if !q.ResetAt.Equal(wantReset) {
		t.Fatalf("reset-at = %v, want %v", q.ResetAt, wantReset)
	}
}

// Scenario: Exa spend can be reset manually
//
//	Given an exa key spent $3
//	When its spend is reset
//	Then spent is $0 and the key becomes eligible if it was over budget
func TestExaSpendManualReset(t *testing.T) {
	h := newHarness(t, config.SearchKey{Provider: "exa", APIKey: "exa-a", Budget: 1})
	h.svc.Pool().AddSpend(KeyID("exa", "exa-a"), 3)

	if !h.svc.ResetSpend("exa", "exa-a") {
		t.Fatal("ResetSpend returned false")
	}
	if h.svc.ResetSpend("exa", "missing") {
		t.Fatal("ResetSpend for missing key returned true")
	}
	q := statusOf(t, h, "exa", "exa-a").Quota
	if q == nil || !approx(floatVal(t, "used", q.Used), 0) {
		t.Fatalf("quota = %#v", q)
	}
	mustPick(t, h.svc.Pool(), "exa", nil)
}

// Scenario: Exa spend survives restarts
//
//	Given spend was recorded with a store file in the auth dir
//	When a new service loads the same auth dir
//	Then the previous month-to-date spend is restored; removed keys are dropped on save
func TestExaSpendPersistsAcrossRestart(t *testing.T) {
	dir := t.TempDir()
	clock := &fakeClock{t: time.Date(2026, 9, 23, 0, 0, 0, 0, time.UTC)}
	keys := []config.SearchKey{{Provider: "exa", APIKey: "exa-a"}, {Provider: "exa", APIKey: "exa-b"}}
	first := NewService(clock.Now)
	first.UpdateConfig(&config.Config{AuthDir: dir, SearchKey: keys})
	first.RecordSpend(KeyID("exa", "exa-a"), 1.5)
	first.RecordSpend(KeyID("exa", "exa-b"), 2)

	second := NewService(clock.Now)
	second.UpdateConfig(&config.Config{AuthDir: dir, SearchKey: keys[:1]})
	second.RecordSpend(KeyID("exa", "exa-a"), 0.5)

	third := NewService(clock.Now)
	third.UpdateConfig(&config.Config{AuthDir: dir, SearchKey: keys})
	spent := map[string]float64{}
	for _, st := range third.Pool().Snapshot() {
		if st.Quota != nil && st.Quota.Used != nil {
			spent[st.ID] = *st.Quota.Used
		}
	}
	if !approx(spent[KeyID("exa", "exa-a")], 2) || !approx(spent[KeyID("exa", "exa-b")], 0) {
		t.Fatalf("restored spend = %v", spent)
	}
}

// Scenario: Background refresher
//
//	Given tavily and firecrawl keys
//	When the quota refresher starts
//	Then it refreshes every remote-quota key immediately and then on each tick,
//	  skips a key whose previous refresh is still in flight, and stops when its context is cancelled
func TestQuotaRefresherRunsImmediatelyThenOnTick(t *testing.T) {
	var mu sync.Mutex
	hits := map[string]int{}
	arrived := make(chan string, 16)
	release := make(chan struct{})
	up := newFakeUpstream(t, func(w http.ResponseWriter, r *http.Request, _ string) {
		key := upstreamKey(r)
		mu.Lock()
		hits[key]++
		n := hits[key]
		mu.Unlock()
		arrived <- key
		if key == "fc-slow" && n == 1 {
			<-release
		}
		writeJSON(w, http.StatusOK, `{"key":{"usage":1,"limit":10},"data":{"remainingCredits":5,"planCredits":10}}`)
	})
	h := newHarness(t,
		config.SearchKey{Provider: "tavily", APIKey: "tvly-a", BaseURL: up.URL},
		config.SearchKey{Provider: "firecrawl", APIKey: "fc-slow", BaseURL: up.URL},
		config.SearchKey{Provider: "exa", APIKey: "exa-a", BaseURL: up.URL},
	)
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(unblock) // runs before the upstream server closes
	ticks := make(chan time.Time)
	h.svc.newTicker = func(time.Duration) (<-chan time.Time, func()) { return ticks, func() {} }
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	checkedNow := func(apiKey, provider string) func() bool {
		return func() bool {
			q := statusOf(t, h, provider, apiKey).Quota
			return q != nil && q.CheckedAt.Equal(h.clock.Now())
		}
	}

	done := h.svc.StartQuotaRefresher(ctx, time.Hour)
	waitArrivals(t, arrived, 2)
	waitUntil(t, checkedNow("tvly-a", "tavily"))

	h.clock.Advance(time.Minute)
	ticks <- time.Time{}
	waitArrivals(t, arrived, 1) // tvly-a again; fc-slow is still in flight
	waitUntil(t, checkedNow("tvly-a", "tavily"))

	unblock()
	waitUntil(t, checkedNow("fc-slow", "firecrawl"))
	h.clock.Advance(time.Minute)
	ticks <- time.Time{}
	waitArrivals(t, arrived, 2)

	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("refresher did not stop")
	}
	mu.Lock()
	defer mu.Unlock()
	if hits["tvly-a"] != 3 || hits["fc-slow"] != 2 || hits["exa-a"] != 0 {
		t.Fatalf("hits = %v", hits)
	}
}

// waitUntil yields until cond holds, failing after a generous deadline.
func waitUntil(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not reached")
		}
		runtime.Gosched()
	}
}

func waitArrivals(t *testing.T, arrived <-chan string, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		select {
		case <-arrived:
		case <-time.After(5 * time.Second):
			t.Fatalf("timed out waiting for quota request %d/%d", i+1, n)
		}
	}
}

// Scenario: Status snapshot carries quota
//
//	When the status snapshot is taken after a refresh
//	Then each key has quota fields: unit, used, limit, remaining, plan, reset-at, checked-at, error, exhausted
func TestSnapshotIncludesQuota(t *testing.T) {
	up := newFakeUpstream(t, func(w http.ResponseWriter, _ *http.Request, _ string) {
		writeJSON(w, http.StatusOK, `{"key":{"usage":10,"limit":10},"account":{"current_plan":"Free","plan_usage":10,"plan_limit":1000}}`)
	})
	h := newHarness(t,
		config.SearchKey{Provider: "tavily", APIKey: "tvly-a", BaseURL: up.URL},
		config.SearchKey{Provider: "firecrawl", APIKey: "fc-a", BaseURL: up.URL},
	)
	before := statusOf(t, h, "tavily", "tvly-a")
	if before.Quota != nil || before.Exhausted {
		t.Fatalf("quota before refresh = %#v", before)
	}

	h.svc.RefreshQuotas(context.Background(), []string{KeyID("tavily", "tvly-a")})

	st := statusOf(t, h, "tavily", "tvly-a")
	if st.Quota == nil || st.Quota.Plan != "Free" || st.Quota.CheckedAt.IsZero() || !st.Exhausted {
		t.Fatalf("status = %#v", st)
	}
	if statusOf(t, h, "firecrawl", "fc-a").Quota != nil {
		t.Fatal("unrefreshed firecrawl key should have no quota yet")
	}
}

// Scenario: Tavily quota resets on the first day of the next UTC month
//
//	Given the clock is 2026-09-23T10:00Z and a tavily key's /usage succeeds
//	When the quota is refreshed
//	Then its reset-at is 2026-10-01T00:00Z (Tavily resets credits monthly regardless of billing date)
//	And a refresh at 2026-12-15 reports reset-at 2027-01-01T00:00Z
func TestQuotaTavilyResetAtNextMonth(t *testing.T) {
	up := newFakeUpstream(t, func(w http.ResponseWriter, _ *http.Request, _ string) {
		writeJSON(w, http.StatusOK, `{"key":{"usage":1,"limit":1000},"account":{"plan_usage":1,"plan_limit":1000}}`)
	})
	h := newHarness(t, config.SearchKey{Provider: "tavily", APIKey: "tvly-a", BaseURL: up.URL})
	cases := []struct{ now, want time.Time }{
		{time.Date(2026, 9, 23, 10, 0, 0, 0, time.UTC), time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)},
		{time.Date(2026, 12, 15, 0, 0, 0, 0, time.UTC), time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)},
	}
	for _, tc := range cases {
		h.clock.Set(tc.now)
		h.svc.RefreshQuotas(context.Background(), nil)
		if q := statusOf(t, h, "tavily", "tvly-a").Quota; q == nil || !q.ResetAt.Equal(tc.want) {
			t.Fatalf("at %v reset-at = %#v, want %v", tc.now, q, tc.want)
		}
	}
}

// Scenario: The refresher also refreshes right after each UTC month rollover
//
//	Given the clock is 2026-09-30T23:00Z and the refresher is started
//	Then a month timer is armed for 1h (until 2026-10-01T00:00Z)
//	When that timer fires
//	Then every remote-quota key is refreshed again without waiting for the interval tick
//	And the next month timer is armed until 2026-11-01T00:00Z
func TestQuotaRefresherRefreshesAtMonthRollover(t *testing.T) {
	arrived := make(chan string, 8)
	up := newFakeUpstream(t, func(w http.ResponseWriter, r *http.Request, _ string) {
		arrived <- upstreamKey(r)
		writeJSON(w, http.StatusOK, `{"key":{"usage":1,"limit":10}}`)
	})
	h := newHarness(t, config.SearchKey{Provider: "tavily", APIKey: "tvly-a", BaseURL: up.URL})
	h.clock.Set(time.Date(2026, 9, 30, 23, 0, 0, 0, time.UTC))
	h.svc.newTicker = func(time.Duration) (<-chan time.Time, func()) { return make(chan time.Time), func() {} }
	type armed struct {
		d  time.Duration
		ch chan time.Time
	}
	timers := make(chan armed, 4)
	h.svc.newTimer = func(d time.Duration) (<-chan time.Time, func()) {
		ch := make(chan time.Time, 1)
		timers <- armed{d: d, ch: ch}
		return ch, func() {}
	}
	nextTimer := func() armed {
		t.Helper()
		select {
		case a := <-timers:
			return a
		case <-time.After(5 * time.Second):
			t.Fatal("month timer not armed")
			return armed{}
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := h.svc.StartQuotaRefresher(ctx, time.Hour)
	waitArrivals(t, arrived, 1)
	first := nextTimer()
	if first.d != time.Hour {
		t.Fatalf("first month timer = %v, want 1h", first.d)
	}
	waitUntil(t, func() bool {
		q := statusOf(t, h, "tavily", "tvly-a").Quota
		return q != nil && !q.CheckedAt.IsZero()
	})

	h.clock.Set(time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC))
	first.ch <- time.Time{}
	waitArrivals(t, arrived, 1)
	if second := nextTimer(); second.d != 31*24*time.Hour {
		t.Fatalf("second month timer = %v, want %v", second.d, 31*24*time.Hour)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("refresher did not stop")
	}
}
