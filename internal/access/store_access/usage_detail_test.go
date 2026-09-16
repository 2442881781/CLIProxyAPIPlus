package storeaccess

import (
	"sync"
	"testing"
	"time"
)

// Feature: per-key usage detail for monthly-plan operations
//
//   The operator needs to answer "who burned what": per-key breakdown by
//   model, per-key daily time series, per-key upstream-auth attribution
//   (admin-only), and per-key latency stats. Counters persist in the store
//   file; cardinality is capped so the file stays small (excess folds into an
//   "__other__" bucket; daily keeps a rolling window).

func usageEv(tokens int64, model, auth string) UsageEvent {
	return UsageEvent{Tokens: tokens, Model: model, AuthID: auth}
}

func TestUsageDetail_ModelBreakdown(t *testing.T) {
	// Given a key with usage events on models A and B
	// Then usage.models reports per-model tokens/requests/failed/last_used
	// And totals remain consistent with the model rows
	s, _ := newRateLimitTestStore(t)
	entry, err := s.Create("sk-cpa-md", AccessKey{Name: "m"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	s.RecordUsage("sk-cpa-md", usageEv(100, "claude-a", "auth1"))
	s.RecordUsage("sk-cpa-md", usageEv(50, "gpt-b", "auth1"))
	s.RecordUsage("sk-cpa-md", UsageEvent{Tokens: 30, Model: "claude-a", AuthID: "auth1", Failed: true})

	got := s.Get(entry.ID)
	if got == nil {
		t.Fatal("key missing")
	}
	a := got.Usage.Models["claude-a"]
	if a.Tokens != 130 || a.Requests != 2 || a.Failed != 1 {
		t.Fatalf("model a: %+v", a)
	}
	b := got.Usage.Models["gpt-b"]
	if b.Tokens != 50 || b.Requests != 1 {
		t.Fatalf("model b: %+v", b)
	}
	var sum int64
	for _, d := range got.Usage.Models {
		sum += d.Tokens
	}
	if sum != got.Usage.TotalTokens {
		t.Fatalf("model rows %d != total %d", sum, got.Usage.TotalTokens)
	}
}

func TestUsageDetail_DailySeries(t *testing.T) {
	// Given usage events on two different UTC days
	// Then usage.daily holds one row per day with tokens and requests
	s, clk := newRateLimitTestStore(t)
	if _, err := s.Create("sk-cpa-dd", AccessKey{Name: "d"}); err != nil {
		t.Fatalf("create: %v", err)
	}
	s.RecordUsage("sk-cpa-dd", usageEv(10, "m", "a"))
	clk.t = clk.t.Add(26 * time.Hour) // next UTC day
	s.RecordUsage("sk-cpa-dd", usageEv(20, "m", "a"))

	got := s.Lookup("sk-cpa-dd")
	if len(got.Usage.Daily) != 2 {
		t.Fatalf("daily rows: %+v", got.Usage.Daily)
	}
	var totalReqs, totalTokens int64
	for _, d := range got.Usage.Daily {
		totalReqs += d.Requests
		totalTokens += d.Tokens
	}
	if totalReqs != 2 || totalTokens != 30 {
		t.Fatalf("daily sums: %+v", got.Usage.Daily)
	}
}

func TestUsageDetail_DailyRetention(t *testing.T) {
	// Given daily rows beyond the retention window
	// When a new day is recorded
	// Then the oldest days are dropped and the window stays bounded
	s, clk := newRateLimitTestStore(t)
	if _, err := s.Create("sk-cpa-ret", AccessKey{Name: "r"}); err != nil {
		t.Fatalf("create: %v", err)
	}
	for i := 0; i < usageDailyKeepDays+5; i++ {
		s.RecordUsage("sk-cpa-ret", usageEv(1, "m", "a"))
		clk.t = clk.t.Add(24 * time.Hour)
	}
	got := s.Lookup("sk-cpa-ret")
	if len(got.Usage.Daily) > usageDailyKeepDays {
		t.Fatalf("daily rows unbounded: %d", len(got.Usage.Daily))
	}
}

func TestUsageDetail_AuthAttribution(t *testing.T) {
	// Given usage events attributed to upstream auths X and Y
	// Then usage.auths reports per-auth tokens and requests
	s, _ := newRateLimitTestStore(t)
	if _, err := s.Create("sk-cpa-au", AccessKey{Name: "a"}); err != nil {
		t.Fatalf("create: %v", err)
	}
	s.RecordUsage("sk-cpa-au", usageEv(100, "m", "sub-account-1"))
	s.RecordUsage("sk-cpa-au", usageEv(200, "m", "sub-account-2"))
	got := s.Lookup("sk-cpa-au")
	if got.Usage.Auths["sub-account-1"].Tokens != 100 || got.Usage.Auths["sub-account-2"].Tokens != 200 {
		t.Fatalf("auth rows: %+v", got.Usage.Auths)
	}
}

func TestUsageDetail_LatencyAverages(t *testing.T) {
	// Given usage events with latency and TTFT
	// Then totals accumulate so responses can expose exact averages
	s, _ := newRateLimitTestStore(t)
	if _, err := s.Create("sk-cpa-lt", AccessKey{Name: "l"}); err != nil {
		t.Fatalf("create: %v", err)
	}
	s.RecordUsage("sk-cpa-lt", UsageEvent{Tokens: 10, Latency: 100 * time.Millisecond, TTFT: 40 * time.Millisecond})
	s.RecordUsage("sk-cpa-lt", UsageEvent{Tokens: 10, Latency: 300 * time.Millisecond, TTFT: 60 * time.Millisecond})
	got := s.Lookup("sk-cpa-lt")
	if got.Usage.LatencyTotalMS != 400 || got.Usage.TTFTTotalMS != 100 {
		t.Fatalf("latency totals: %+v", got.Usage)
	}
	if avg := got.Usage.LatencyTotalMS / got.Usage.Requests; avg != 200 {
		t.Fatalf("avg latency = %d", avg)
	}
}

func TestUsageDetail_CardinalityOverflow(t *testing.T) {
	// Given a key that consumed more distinct models than the cap
	// Then extra models fold into the "__other__" bucket
	// And total tokens still equal the sum of all rows
	s, _ := newRateLimitTestStore(t)
	if _, err := s.Create("sk-cpa-ovf", AccessKey{Name: "o"}); err != nil {
		t.Fatalf("create: %v", err)
	}
	for i := 0; i < maxUsageModelsPerKey+10; i++ {
		s.RecordUsage("sk-cpa-ovf", usageEv(1, "model-"+string(rune('a'+i%26))+string(rune('a'+i/26)), "a"))
	}
	got := s.Lookup("sk-cpa-ovf")
	if len(got.Usage.Models) > maxUsageModelsPerKey+1 {
		t.Fatalf("models unbounded: %d", len(got.Usage.Models))
	}
	if _, ok := got.Usage.Models[usageOtherBucket]; !ok {
		t.Fatal("overflow must fold into __other__")
	}
	var sum int64
	for _, d := range got.Usage.Models {
		sum += d.Tokens
	}
	if sum != got.Usage.TotalTokens {
		t.Fatalf("sum %d != total %d", sum, got.Usage.TotalTokens)
	}
}

func TestUsageDetail_ConcurrentMapSafety(t *testing.T) {
	// Given a key being recorded while List/Get run concurrently
	// Then no data race occurs (copies are deep)
	s, _ := newRateLimitTestStore(t)
	if _, err := s.Create("sk-cpa-race", AccessKey{Name: "r"}); err != nil {
		t.Fatalf("create: %v", err)
	}
	var wg sync.WaitGroup
	stop := make(chan struct{})
	wg.Add(1)
	go func() {
		defer wg.Done()
		i := 0
		for {
			select {
			case <-stop:
				return
			default:
				s.RecordUsage("sk-cpa-race", usageEv(1, "m"+string(rune('a'+i%8)), "a"))
				i++
			}
		}
	}()
	for i := 0; i < 200; i++ {
		list := s.List()
		if len(list) == 0 {
			t.Fatal("list lost the key")
		}
		// Walk the maps on the returned copy; without a deep copy this races.
		for range list[0].Usage.Models {
		}
		if got := s.Lookup("sk-cpa-race"); got != nil {
			for range got.Usage.Daily {
			}
		}
	}
	close(stop)
	wg.Wait()
}

func TestUsageTop_RankByTokens(t *testing.T) {
	// Given keys with different monthly consumption
	// Then usage-top orders them descending and honors limit
	s, _ := newRateLimitTestStore(t)
	s.Create("sk-cpa-k1", AccessKey{Name: "small"})
	s.Create("sk-cpa-k2", AccessKey{Name: "big"})
	s.Create("sk-cpa-k3", AccessKey{Name: "mid"})
	s.RecordUsage("sk-cpa-k1", usageEv(10, "m", "a"))
	s.RecordUsage("sk-cpa-k2", usageEv(300, "m", "a"))
	s.RecordUsage("sk-cpa-k3", usageEv(100, "m", "a"))

	top := s.UsageTop("tokens", "all", 2)
	if len(top) != 2 || top[0].Tokens != 300 || top[1].Tokens != 100 {
		t.Fatalf("top: %+v", top)
	}
}

func TestUsageTop_RankByRequestsAndFailed(t *testing.T) {
	// by=requests orders by request count; by=failed orders by failure count
	s, _ := newRateLimitTestStore(t)
	s.Create("sk-cpa-q1", AccessKey{Name: "chatty"})
	s.Create("sk-cpa-q2", AccessKey{Name: "flaky"})
	s.RecordUsage("sk-cpa-q1", usageEv(5, "m", "a"))
	s.RecordUsage("sk-cpa-q1", usageEv(5, "m", "a"))
	s.RecordUsage("sk-cpa-q2", UsageEvent{Tokens: 5, Failed: true})
	s.RecordUsage("sk-cpa-q2", UsageEvent{Tokens: 5, Failed: true})
	s.RecordUsage("sk-cpa-q2", UsageEvent{Tokens: 5, Failed: true})

	top := s.UsageTop("requests", "all", 10)
	if top[0].Requests != 3 || top[1].Requests != 2 {
		t.Fatalf("requests rank: %+v", top)
	}
	top = s.UsageTop("failed", "all", 10)
	if top[0].Failed != 3 {
		t.Fatalf("failed rank: %+v", top)
	}
}

func TestGroupUsage_AggregatesMemberKeys(t *testing.T) {
	// Given a group with member keys holding per-model and daily rows
	// Then group usage sums member rows per dimension
	s, _ := newRateLimitTestStore(t)
	if _, err := s.UpsertGroup(Group{Name: "plan-a"}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	s.Create("sk-cpa-ga", AccessKey{Name: "u1", Group: "plan-a"})
	s.Create("sk-cpa-gb", AccessKey{Name: "u2", Group: "plan-a"})
	s.RecordUsage("sk-cpa-ga", usageEv(100, "m1", "auth1"))
	s.RecordUsage("sk-cpa-gb", usageEv(200, "m1", "auth2"))
	s.RecordUsage("sk-cpa-gb", usageEv(50, "m2", "auth2"))

	view, err := s.GroupUsage("plan-a")
	if err != nil {
		t.Fatalf("group usage: %v", err)
	}
	if view.Totals.Tokens != 350 || view.Totals.Requests != 3 {
		t.Fatalf("totals: %+v", view.Totals)
	}
	if view.Models["m1"].Tokens != 300 || view.Models["m2"].Tokens != 50 {
		t.Fatalf("models: %+v", view.Models)
	}
	if view.Auths["auth1"].Tokens != 100 || view.Auths["auth2"].Tokens != 250 {
		t.Fatalf("auths: %+v", view.Auths)
	}
	if _, err := s.GroupUsage("missing"); err == nil {
		t.Fatal("missing group must error")
	}
}
