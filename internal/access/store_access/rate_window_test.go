package storeaccess

import (
	"testing"
	"time"

	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
)

// Feature: live throughput gauges
//
//   The operator needs the current rate, not just totals: output tokens/sec
//   and requests/sec over a sliding 60s window, per access key. The same
//   window type feeds provider- and upstream-auth-level gauges in the usage
//   monitor. Rates are memory-only gauges; nothing persists.

func TestKeyRate_RecordedInUsage(t *testing.T) {
	// Given usage events recorded for a key
	// Then the key's rate window reflects current output tokens/sec and rps
	s, _ := newRateLimitTestStore(t)
	entry, err := s.Create("sk-cpa-rt", AccessKey{Name: "r"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	s.RecordUsage("sk-cpa-rt", UsageEvent{Tokens: 120, InputTokens: 80, OutputTokens: 40, Model: "m"})
	s.RecordUsage("sk-cpa-rt", UsageEvent{Tokens: 60, InputTokens: 40, OutputTokens: 20, Model: "m"})

	rate, ok := s.KeyRate(entry.ID)
	if !ok {
		t.Fatal("rate missing for known key")
	}
	if rate.OutputTokensPerSecond != 60.0/coreusage.RateWindowSeconds {
		t.Fatalf("output tps = %v", rate.OutputTokensPerSecond)
	}
	if rate.TotalTokensPerSecond != 180.0/coreusage.RateWindowSeconds {
		t.Fatalf("total tps = %v", rate.TotalTokensPerSecond)
	}
	if rate.RequestsPerSecond != 2.0/coreusage.RateWindowSeconds {
		t.Fatalf("rps = %v", rate.RequestsPerSecond)
	}
	if _, ok := s.KeyRate("missing-id"); ok {
		t.Fatal("rate must not exist for unknown key id")
	}
}

func TestKeyRate_WindowExpires(t *testing.T) {
	// Given events older than the window
	// When the clock advances past the window
	// Then their contribution expires and rate drops to zero
	s, clk := newRateLimitTestStore(t)
	entry, err := s.Create("sk-cpa-rx", AccessKey{Name: "x"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	s.RecordUsage("sk-cpa-rx", UsageEvent{Tokens: 60, OutputTokens: 30})
	clk.t = clk.t.Add(coreusage.RateWindowSeconds*time.Second + time.Second)
	rate, ok := s.KeyRate(entry.ID)
	if !ok {
		t.Fatal("rate missing")
	}
	if rate.TotalTokensPerSecond != 0 || rate.RequestsPerSecond != 0 {
		t.Fatalf("stale rate: %+v", rate)
	}
}

func TestKeyRate_InUsageSummary(t *testing.T) {
	// Given a key with recent activity
	// Then UsageSummary exposes a "rates" block with the current gauges
	s, _ := newRateLimitTestStore(t)
	entry, err := s.Create("sk-cpa-rs", AccessKey{Name: "s"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	s.RecordUsage("sk-cpa-rs", UsageEvent{Tokens: 60, OutputTokens: 30})
	rate, _ := s.KeyRate(entry.ID)
	summary := UsageSummary(entry.Usage, false, false, rate)
	got, ok := summary["rates"].(coreusage.Rate)
	if !ok || got.OutputTokensPerSecond == 0 {
		t.Fatalf("summary rates: %+v", summary["rates"])
	}
	if _, has := summary["auths"]; has {
		t.Fatal("self-service summary must not expose auths")
	}
}

func TestKeyRates_SortedSnapshot(t *testing.T) {
	// Given several keys with different live rates
	// Then KeyRates returns rows ordered by total tokens/sec descending
	s, _ := newRateLimitTestStore(t)
	s.Create("sk-cpa-ka", AccessKey{Name: "slow"})
	s.Create("sk-cpa-kb", AccessKey{Name: "fast"})
	s.RecordUsage("sk-cpa-ka", UsageEvent{Tokens: 10, OutputTokens: 5})
	s.RecordUsage("sk-cpa-kb", UsageEvent{Tokens: 600, OutputTokens: 300})

	rows := s.KeyRates()
	if len(rows) != 2 || rows[0].Name != "fast" || rows[1].Name != "slow" {
		t.Fatalf("rows: %+v", rows)
	}
	if rows[0].TotalTokensPerSecond != 600.0/coreusage.RateWindowSeconds {
		t.Fatalf("row rate: %+v", rows[0])
	}
}
