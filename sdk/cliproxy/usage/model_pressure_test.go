package usage

import (
	"context"
	"math"
	"sync"
	"testing"
	"time"
)

// Feature: authoritative per-model pressure gauges
//
//   The operator sells monthly plans keyed by client-facing model names. One
//   model can be served by several providers/credentials, so pressure is
//   aggregated at the alias dimension — the name the customer asked for.
//
//   Authority contract:
//     - in_flight is an exact atomic gauge: +1 when a client request enters the
//       conductor funnel, -1 when its response is fully delivered or abandoned.
//       Credential retries inside one request never double-count.
//     - rate/error/latency fields are exact event sums over a sliding 60s
//       window of COMPLETED requests — no sampling. Streaming token counts
//       attribute to the completion second (documented, not smoothed).
//     - requests rejected before reaching the funnel (our own 429s) are
//       excluded: they consumed no upstream capacity.

func pressureRowByModel(rows []ModelPressureRow, model string) (ModelPressureRow, bool) {
	for _, row := range rows {
		if row.Model == model {
			return row, true
		}
	}
	return ModelPressureRow{}, false
}

func TestModelPressure_InFlightExact(t *testing.T) {
	// Given two Begin calls on one model and one on another
	// Then Snapshot reports in_flight 2 and 1 respectively
	// When one scope ends, its model's in_flight decrements
	// And End is idempotent (double-End never goes negative)
	tr := NewModelPressureTracker()
	s1 := tr.Begin("model-a")
	s2 := tr.Begin("model-a")
	s3 := tr.Begin("model-b")

	row, ok := pressureRowByModel(tr.Snapshot(), "model-a")
	if !ok || row.InFlight != 2 {
		t.Fatalf("model-a in_flight = %v, want 2", row.InFlight)
	}
	if row, ok = pressureRowByModel(tr.Snapshot(), "model-b"); !ok || row.InFlight != 1 {
		t.Fatalf("model-b in_flight = %v, want 1", row.InFlight)
	}

	s1.End()
	s1.End() // idempotent: must not double-decrement
	if row, _ = pressureRowByModel(tr.Snapshot(), "model-a"); row.InFlight != 1 {
		t.Fatalf("model-a in_flight after End = %v, want 1", row.InFlight)
	}

	s2.End()
	s3.End()
	if rows := tr.Snapshot(); len(rows) != 0 {
		t.Fatalf("snapshot after all End = %v rows, want 0", len(rows))
	}
}

func TestModelPressure_ObserveFeedRates(t *testing.T) {
	// Given completed-request observations for a model
	// Then the model's window reflects exact rps, input/output/total tps
	// And a failed observation counts into failed_per_second and error_rate
	tr := NewModelPressureTracker()
	now := time.Unix(1_000_000, 0)
	tr.now = func() time.Time { return now }

	tr.Observe("model-a", ModelPressureEvent{InputTokens: 60, OutputTokens: 30, TotalTokens: 90})
	now = now.Add(10 * time.Second)
	tr.Observe("model-a", ModelPressureEvent{OutputTokens: 10, TotalTokens: 10, Failed: true})

	row, ok := pressureRowByModel(tr.Snapshot(), "model-a")
	if !ok {
		t.Fatal("model-a missing from snapshot")
	}
	if got := row.RequestsPerSecond; math.Abs(got-2.0/60) > 1e-9 {
		t.Fatalf("rps = %v, want %v", got, 2.0/60)
	}
	if got := row.InputTokensPerSecond; math.Abs(got-1.0) > 1e-9 {
		t.Fatalf("input tps = %v, want 1.0", got)
	}
	if got := row.OutputTokensPerSecond; math.Abs(got-40.0/60) > 1e-9 {
		t.Fatalf("output tps = %v, want %v", got, 40.0/60)
	}
	if got := row.TotalTokensPerSecond; math.Abs(got-100.0/60) > 1e-9 {
		t.Fatalf("total tps = %v, want %v", got, 100.0/60)
	}
	if got := row.FailedPerSecond; math.Abs(got-1.0/60) > 1e-9 {
		t.Fatalf("failed/s = %v, want %v", got, 1.0/60)
	}
	if got := row.ErrorRate; math.Abs(got-0.5) > 1e-9 {
		t.Fatalf("error_rate = %v, want 0.5", got)
	}
}

func TestModelPressure_LatencyAverages(t *testing.T) {
	// Given observations carrying latency and TTFT
	// Then the snapshot row reports exact window averages
	tr := NewModelPressureTracker()
	now := time.Unix(1_000_000, 0)
	tr.now = func() time.Time { return now }

	tr.Observe("model-a", ModelPressureEvent{Latency: 100 * time.Millisecond, TTFT: 40 * time.Millisecond})
	tr.Observe("model-a", ModelPressureEvent{Latency: 300 * time.Millisecond, TTFT: 60 * time.Millisecond})
	tr.Observe("model-a", ModelPressureEvent{Latency: 200 * time.Millisecond}) // no TTFT reported

	row, ok := pressureRowByModel(tr.Snapshot(), "model-a")
	if !ok {
		t.Fatal("model-a missing from snapshot")
	}
	if row.AvgLatencyMS != 200 {
		t.Fatalf("avg_latency_ms = %v, want 200", row.AvgLatencyMS)
	}
	if row.AvgTTFTMS != 50 {
		t.Fatalf("avg_ttft_ms = %v, want 50 (averaged over ttft-reporting events)", row.AvgTTFTMS)
	}
}

func TestModelPressure_WindowExpiryDropsIdleRows(t *testing.T) {
	// Given a model whose last activity is older than the window
	// Then it disappears from the snapshot entirely (no phantom pressure)
	// But a model with in_flight > 0 and no recent completions stays listed
	tr := NewModelPressureTracker()
	now := time.Unix(1_000_000, 0)
	tr.now = func() time.Time { return now }

	tr.Observe("stale", ModelPressureEvent{TotalTokens: 5})
	live := tr.Begin("live")

	now = now.Add(61 * time.Second)
	rows := tr.Snapshot()
	if _, ok := pressureRowByModel(rows, "stale"); ok {
		t.Fatal("stale model should drop out of snapshot")
	}
	if row, ok := pressureRowByModel(rows, "live"); !ok || row.InFlight != 1 {
		t.Fatalf("live model missing or wrong in_flight: %+v", row)
	}
	live.End()
}

func TestModelPressure_AliasKeying(t *testing.T) {
	// Given observations for the same upstream model under two aliases
	// Then they aggregate under the client-facing alias, not the upstream name
	// And empty alias falls back to the upstream model name
	tr := NewModelPressureTracker()
	restore := SetDefaultModelPressureForTesting(tr)
	defer restore()

	plugin := modelPressurePlugin{}
	ctx := context.Background()
	plugin.HandleUsage(ctx, Record{Model: "upstream-m", Alias: "public-x", Detail: Detail{TotalTokens: 10}})
	plugin.HandleUsage(ctx, Record{Model: "upstream-m", Alias: "public-y", Detail: Detail{TotalTokens: 20}})
	plugin.HandleUsage(ctx, Record{Model: "upstream-solo", Detail: Detail{TotalTokens: 5}})

	rows := tr.Snapshot()
	if _, ok := pressureRowByModel(rows, "upstream-m"); ok {
		t.Fatal("upstream model name must not appear when alias is set")
	}
	if _, ok := pressureRowByModel(rows, "public-x"); !ok {
		t.Fatal("alias public-x missing")
	}
	if _, ok := pressureRowByModel(rows, "public-y"); !ok {
		t.Fatal("alias public-y missing")
	}
	if _, ok := pressureRowByModel(rows, "upstream-solo"); !ok {
		t.Fatal("model without alias should key on upstream name")
	}
}

func TestModelPressure_SnapshotSorted(t *testing.T) {
	// Given several models with different in_flight and rates
	// Then snapshot rows order by in_flight desc, then total tps desc
	tr := NewModelPressureTracker()
	now := time.Unix(1_000_000, 0)
	tr.now = func() time.Time { return now }

	tr.Observe("low", ModelPressureEvent{TotalTokens: 10})
	tr.Observe("high", ModelPressureEvent{TotalTokens: 100})
	s1 := tr.Begin("busy")
	s2 := tr.Begin("busy")

	rows := tr.Snapshot()
	if len(rows) != 3 {
		t.Fatalf("rows = %v, want 3", len(rows))
	}
	if rows[0].Model != "busy" || rows[0].InFlight != 2 {
		t.Fatalf("rows[0] = %+v, want busy in_flight=2", rows[0])
	}
	if rows[1].Model != "high" {
		t.Fatalf("rows[1] = %+v, want high", rows[1])
	}
	if rows[2].Model != "low" {
		t.Fatalf("rows[2] = %+v, want low", rows[2])
	}
	s1.End()
	s2.End()
}

func TestModelPressure_ConcurrentSafety(t *testing.T) {
	// Given concurrent Begin/End and Observe from many goroutines
	// Then no data race and in_flight returns to exactly zero
	tr := NewModelPressureTracker()
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				s := tr.Begin("m")
				tr.Observe("m", ModelPressureEvent{TotalTokens: 1})
				_ = tr.Snapshot()
				s.End()
			}
		}()
	}
	wg.Wait()
	if got := tr.InFlight("m"); got != 0 {
		t.Fatalf("in_flight = %v, want 0", got)
	}
}
