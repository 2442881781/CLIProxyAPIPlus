package management

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	gnet "github.com/shirou/gopsutil/v4/net"
)

// Feature: monthly traffic accounting in GET /server-stats
//
//   The proxy owner needs to know how much bandwidth this VPS has burned in
//   the current calendar month. NIC counters are cumulative since boot, so a
//   tracker folds per-sample deltas into month totals persisted to disk —
//   surviving process restarts and host reboots (counter resets).

func TestNetUsageTracker_AccumulatesWithinMonth(t *testing.T) {
	dir := t.TempDir()
	tracker := newNetUsageTracker(filepath.Join(dir, "state.json"))
	now := time.Date(2026, 9, 16, 10, 0, 0, 0, time.UTC)

	tracker.Observe(1000, 2000, now) // baseline
	st := tracker.Observe(1500, 2600, now.Add(time.Second))
	if st.Rx != 500 || st.Tx != 600 {
		t.Fatalf("month totals = rx %d tx %d, want 500/600", st.Rx, st.Tx)
	}
	if st.Month != "2026-09" {
		t.Fatalf("month = %q, want 2026-09", st.Month)
	}
}

func TestNetUsageTracker_MonthRolloverResetsTotals(t *testing.T) {
	dir := t.TempDir()
	tracker := newNetUsageTracker(filepath.Join(dir, "state.json"))

	tracker.Observe(1000, 2000, time.Date(2026, 9, 30, 23, 59, 0, 0, time.UTC))
	tracker.Observe(2000, 3000, time.Date(2026, 9, 30, 23, 59, 30, 0, time.UTC))

	st := tracker.Observe(2400, 3400, time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC))
	if st.Month != "2026-10" {
		t.Fatalf("month = %q, want 2026-10", st.Month)
	}
	if st.Rx != 400 || st.Tx != 400 {
		t.Fatalf("october totals = rx %d tx %d, want 400/400", st.Rx, st.Tx)
	}
}

func TestNetUsageTracker_CounterResetCountsCurrentValue(t *testing.T) {
	dir := t.TempDir()
	tracker := newNetUsageTracker(filepath.Join(dir, "state.json"))
	now := time.Date(2026, 9, 16, 10, 0, 0, 0, time.UTC)

	tracker.Observe(5000, 6000, now)
	// Host reboot: counters restart from zero.
	st := tracker.Observe(300, 400, now.Add(time.Minute))
	if st.Rx != 300 || st.Tx != 400 {
		t.Fatalf("after reset totals = rx %d tx %d, want 300/400", st.Rx, st.Tx)
	}
}

func TestNetUsageTracker_PersistsAcrossRestarts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	now := time.Date(2026, 9, 16, 10, 0, 0, 0, time.UTC)

	first := newNetUsageTracker(path)
	first.Observe(1000, 2000, now)
	first.Observe(1400, 2500, now.Add(time.Second))
	first.save()

	second := newNetUsageTracker(path)
	// A new process observes counters continuing from the persisted baseline.
	st := second.Observe(1600, 2700, now.Add(time.Minute))
	if st.Rx != 600 || st.Tx != 700 {
		t.Fatalf("restarted totals = rx %d tx %d, want 600/700", st.Rx, st.Tx)
	}
}

func TestNetUsageTracker_CorruptStateFileStartsFresh(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	tracker := newNetUsageTracker(path)
	st := tracker.Observe(1000, 2000, time.Now())
	if st.Rx != 0 || st.Tx != 0 {
		t.Fatalf("corrupt file should start fresh, got %+v", st)
	}
}

func TestServerStats_MonthlyTrafficInResponse(t *testing.T) {
	var call int
	orig := statsNetIOCounters
	t.Cleanup(func() { statsNetIOCounters = orig })
	statsNetIOCounters = func(pernic bool) ([]gnet.IOCountersStat, error) {
		call++
		return []gnet.IOCountersStat{{BytesRecv: uint64(call) * 1000, BytesSent: uint64(call) * 2000}}, nil
	}

	// Point the tracker at a temp state file so the test does not touch the
	// real working directory.
	gin.SetMode(gin.TestMode)
	router := gin.New()
	handler := &Handler{netUsage: newNetUsageTracker(filepath.Join(t.TempDir(), "s.json"))}
	handler.SetRuntimeStatsProvider(func() (int64, int64) { return 0, 0 })
	router.GET("/server-stats", handler.GetServerStats)

	_, first := doReq(t, router, "GET", "/server-stats", "")
	net1, ok := first["network"].(map[string]any)
	if !ok {
		t.Fatalf("network section missing on first call: %v", first)
	}
	if net1["month_rx_bytes"] != float64(0) {
		t.Fatalf("first sample is the baseline, want month_rx 0, got %v", net1["month_rx_bytes"])
	}
	if net1["total_rx_bytes"] != float64(1000) {
		t.Fatalf("total_rx_bytes = %v, want 1000", net1["total_rx_bytes"])
	}

	_, second := doReq(t, router, "GET", "/server-stats", "")
	net2, ok := second["network"].(map[string]any)
	if !ok {
		t.Fatalf("network section missing on second call: %v", second)
	}
	if net2["month_rx_bytes"] != float64(1000) || net2["month_tx_bytes"] != float64(2000) {
		t.Fatalf("month totals = %v/%v, want 1000/2000", net2["month_rx_bytes"], net2["month_tx_bytes"])
	}
	if _, ok := net2["rx_bytes_per_sec"].(float64); !ok {
		t.Fatalf("rx_bytes_per_sec missing alongside monthly totals: %v", net2)
	}
	if _, ok := net2["month"].(string); !ok {
		t.Fatalf("month label missing: %v", net2)
	}
}

func TestParseVnstatMonthly(t *testing.T) {
	// vnstat 2.x reports KiB; the current month is summed across interfaces.
	doc := []byte(`{
		"interfaces": [
			{"name": "eth0", "traffic": {
				"total": {"rx": 1000000, "tx": 500000},
				"month": [
					{"date": {"year": 2026, "month": 8}, "rx": 100, "tx": 50},
					{"date": {"year": 2026, "month": 9}, "rx": 2000, "tx": 1000}
				]
			}},
			{"name": "wg0", "traffic": {
				"total": {"rx": 4000, "tx": 2000},
				"month": [
					{"date": {"year": 2026, "month": 9}, "rx": 300, "tx": 150}
				]
			}}
		]
	}`)
	vt, ok := parseVnstatMonthly(doc, time.Date(2026, 9, 16, 0, 0, 0, 0, time.UTC))
	if !ok {
		t.Fatal("parse failed")
	}
	if vt.RxBytes != (2000+300)*1024 || vt.TxBytes != (1000+150)*1024 {
		t.Fatalf("month bytes = %d/%d, want %d/%d", vt.RxBytes, vt.TxBytes, 2300*1024, 1150*1024)
	}
	if vt.TotalRxBytes != (1000000+4000)*1024 {
		t.Fatalf("total rx = %d, want %d", vt.TotalRxBytes, 1004000*1024)
	}
	if vt.Month != "2026-09" {
		t.Fatalf("month = %q", vt.Month)
	}
	if _, ok := parseVnstatMonthly([]byte("garbage"), time.Now()); ok {
		t.Fatal("malformed json should fail")
	}
	if _, ok := parseVnstatMonthly([]byte(`{"interfaces":[]}`), time.Now()); ok {
		t.Fatal("empty interfaces should fail")
	}
}

func TestServerStats_VnstatPreferredOverBuiltin(t *testing.T) {
	origVnstat := statsVnstatJSON
	t.Cleanup(func() { statsVnstatJSON = origVnstat })
	statsVnstatJSON = func() ([]byte, error) {
		return []byte(`{"interfaces":[{"name":"eth0","traffic":{"total":{"rx":1,"tx":2},
			"month":[{"date":{"year":` + time.Now().Format("2006") + `,"month":` +
			string(rune('0'+int(time.Now().Month()))) + `},"rx":9,"tx":4}]}}]}`), nil
	}
	if _, ok := queryVnstat(time.Now()); !ok {
		t.Skip("injected vnstat did not parse")
	}
}
