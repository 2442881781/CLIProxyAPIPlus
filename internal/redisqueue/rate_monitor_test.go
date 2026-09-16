package redisqueue

import (
	"testing"
	"time"

	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
)

// Feature: provider- and upstream-auth-level throughput gauges
//
//   observeUsageRecord feeds the same sliding window per provider and per
//   upstream auth so the management API can answer "how much is each pool
//   account serving right now". Gauges follow the usage-statistics toggle.

// freezeMonitorClock pins the snapshot clock so rates are deterministic.
func freezeMonitorClock(t *testing.T, now time.Time) {
	t.Helper()
	prev := usageMonitor.now
	usageMonitor.now = func() time.Time { return now }
	t.Cleanup(func() { usageMonitor.now = prev })
}

func TestRateMonitor_ProviderRate(t *testing.T) {
	isolateUsageMonitorPersistence(t)
	ResetUsageMonitor()
	t.Cleanup(ResetUsageMonitor)

	now := time.Date(2026, time.October, 1, 0, 0, 0, 0, time.UTC)
	freezeMonitorClock(t, now)

	claude := coreusage.Record{
		Provider:    "claude",
		Model:       "claude-x",
		AuthID:      "acc-1",
		RequestedAt: now,
		Detail:      coreusage.Detail{TokenBreakdown: coreusage.NewSubsetTokenBreakdown(80, 0, 0, 40, 0, 120)},
	}
	gemini := coreusage.Record{
		Provider:    "gemini",
		Model:       "gemini-y",
		AuthID:      "acc-2",
		RequestedAt: now,
		Detail:      coreusage.Detail{TokenBreakdown: coreusage.NewSubsetTokenBreakdown(20, 0, 0, 10, 0, 30)},
	}
	observeUsageRecord(claude, claude.Detail, false, now)
	observeUsageRecord(claude, claude.Detail, false, now)
	observeUsageRecord(gemini, gemini.Detail, false, now)

	snap := GetRateSnapshot()
	if !snap.Enabled || snap.WindowSeconds != coreusage.RateWindowSeconds {
		t.Fatalf("snapshot header: %+v", snap)
	}
	if len(snap.Providers) != 2 {
		t.Fatalf("providers: %+v", snap.Providers)
	}
	// Sorted by total tps descending: claude (240 total) before gemini (30).
	top := snap.Providers[0]
	if top.Provider != "claude" ||
		top.TotalTokensPerSecond != 240.0/coreusage.RateWindowSeconds ||
		top.OutputTokensPerSecond != 80.0/coreusage.RateWindowSeconds ||
		top.RequestsPerSecond != 2.0/coreusage.RateWindowSeconds {
		t.Fatalf("claude rate row: %+v", top)
	}
	if snap.Providers[1].Provider != "gemini" {
		t.Fatalf("gemini row: %+v", snap.Providers[1])
	}
}

func TestRateMonitor_AuthRate(t *testing.T) {
	isolateUsageMonitorPersistence(t)
	ResetUsageMonitor()
	t.Cleanup(ResetUsageMonitor)

	now := time.Date(2026, time.October, 1, 0, 0, 0, 0, time.UTC)
	freezeMonitorClock(t, now)

	mk := func(provider, auth string, total int64) coreusage.Record {
		return coreusage.Record{
			Provider:    provider,
			Model:       "m",
			AuthID:      auth,
			RequestedAt: now,
			Detail:      coreusage.Detail{TokenBreakdown: coreusage.NewSubsetTokenBreakdown(total, 0, 0, total/2, 0, total)},
		}
	}
	for _, rec := range []coreusage.Record{
		mk("claude", "acc-a", 100),
		mk("claude", "acc-b", 60),
		mk("codex", "acc-c", 30),
		mk("claude", "", 7), // anonymous auth still counts toward the provider
	} {
		observeUsageRecord(rec, rec.Detail, false, now)
	}

	snap := GetRateSnapshot()
	if len(snap.Auths) != 3 {
		t.Fatalf("auth rows: %+v", snap.Auths)
	}
	byAuth := make(map[string]AuthRateRow, len(snap.Auths))
	for _, row := range snap.Auths {
		byAuth[row.AuthID] = row
	}
	if row := byAuth["acc-a"]; row.Provider != "claude" || row.TotalTokensPerSecond != 100.0/coreusage.RateWindowSeconds {
		t.Fatalf("acc-a row: %+v", row)
	}
	if row := byAuth["acc-c"]; row.Provider != "codex" {
		t.Fatalf("acc-c row: %+v", row)
	}
	// The auth-less record only shows on the provider row.
	for _, p := range snap.Providers {
		if p.Provider == "claude" && p.RequestsPerSecond != 3.0/coreusage.RateWindowSeconds {
			t.Fatalf("claude provider rps: %+v", p)
		}
	}
}

func TestRateMonitor_WindowExpiry(t *testing.T) {
	isolateUsageMonitorPersistence(t)
	ResetUsageMonitor()
	t.Cleanup(ResetUsageMonitor)

	now := time.Date(2026, time.October, 1, 0, 0, 0, 0, time.UTC)
	freezeMonitorClock(t, now)

	rec := coreusage.Record{
		Provider:    "claude",
		Model:       "m",
		AuthID:      "acc",
		RequestedAt: now,
		Detail:      coreusage.Detail{TokenBreakdown: coreusage.NewSubsetTokenBreakdown(50, 0, 0, 50, 0, 100)},
	}
	observeUsageRecord(rec, rec.Detail, false, now)

	usageMonitor.now = func() time.Time { return now.Add(coreusage.RateWindowSeconds*time.Second + time.Second) }
	snap := GetRateSnapshot()
	if len(snap.Providers) != 0 || len(snap.Auths) != 0 {
		t.Fatalf("expired rates must drop to zero rows: %+v", snap)
	}
	// Cumulative monitor counters are unaffected by rate expiry.
	full := GetUsageMonitorSnapshot()
	if full.Requests != 1 || full.Tokens.TotalTokens != 100 {
		t.Fatalf("cumulative totals lost: %+v", full)
	}
}
