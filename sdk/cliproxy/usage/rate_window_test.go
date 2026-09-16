package usage

import (
	"testing"
	"time"
)

// Feature: sliding-window throughput gauge shared by access-key, provider and
// upstream-auth rate monitoring. The window is a fixed ring of per-second
// buckets; Rate() reports per-second averages over the whole window.

func TestRateWindow_AddAndRate(t *testing.T) {
	var w RateWindow
	base := time.Unix(1_700_000_000, 0)
	w.Add(base, 100, 40, 140)
	w.Add(base.Add(-time.Second), 200, 80, 280)

	got := w.Rate(base)
	if got.RequestsPerSecond != 2.0/RateWindowSeconds {
		t.Fatalf("rps = %v, want %v", got.RequestsPerSecond, 2.0/RateWindowSeconds)
	}
	if got.InputTokensPerSecond != 300.0/RateWindowSeconds {
		t.Fatalf("input tps = %v", got.InputTokensPerSecond)
	}
	if got.OutputTokensPerSecond != 120.0/RateWindowSeconds {
		t.Fatalf("output tps = %v", got.OutputTokensPerSecond)
	}
	if got.TotalTokensPerSecond != 420.0/RateWindowSeconds {
		t.Fatalf("total tps = %v", got.TotalTokensPerSecond)
	}
}

func TestRateWindow_SlidesAndExpires(t *testing.T) {
	var w RateWindow
	base := time.Unix(1_700_000_000, 0)
	w.Add(base, 10, 60, 60)

	if got := w.Rate(base.Add(RateWindowSeconds * time.Second)); got.TotalTokensPerSecond != 0 {
		t.Fatalf("stale event still counted: %+v", got)
	}
	if got := w.Rate(base.Add(time.Second)); got.TotalTokensPerSecond == 0 {
		t.Fatal("fresh event must still count")
	}
}

func TestRateWindow_BucketReuse(t *testing.T) {
	var w RateWindow
	base := time.Unix(1_700_000_000, 0)
	w.Add(base, 0, 100, 100)
	later := base.Add(RateWindowSeconds * time.Second) // same ring slot
	w.Add(later, 0, 50, 50)

	got := w.Rate(later)
	if got.TotalTokensPerSecond != 50.0/RateWindowSeconds {
		t.Fatalf("wrapped slot kept stale total: %+v", got)
	}

	// A stale out-of-order event landing on a newer slot must not clobber it.
	var w2 RateWindow
	w2.Add(later, 0, 10, 10)
	w2.Add(base, 0, 999, 999)
	if got := w2.Rate(later); got.TotalTokensPerSecond != 10.0/RateWindowSeconds {
		t.Fatalf("stale event clobbered newer slot: %+v", got)
	}
}
