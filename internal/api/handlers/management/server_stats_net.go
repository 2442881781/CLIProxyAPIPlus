package management

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// netUsageState tracks month-scoped cumulative traffic. Raw NIC counters
// are persisted so deltas survive process restarts; a raw counter that
// moves backwards means the host rebooted and the delta is cur itself.
type netUsageState struct {
	Month  string `json:"month"`             // "2006-01" in local time
	Rx     uint64 `json:"rx_bytes"`          // accumulated this month
	Tx     uint64 `json:"tx_bytes"`          // accumulated this month
	LastRx uint64 `json:"last_rx,omitempty"` // last raw counter seen
	LastTx uint64 `json:"last_tx,omitempty"` // last raw counter seen
}

// netUsageTracker accumulates month-scoped traffic and persists it with a
// write throttle so frequent polling does not hammer the disk.
type netUsageTracker struct {
	path       string
	state      netUsageState
	loaded     bool
	lastWrite  time.Time
	writeEvery time.Duration
}

func newNetUsageTracker(path string) *netUsageTracker {
	return &netUsageTracker{path: path, writeEvery: time.Minute}
}

// Observe folds the latest raw counters into the monthly totals and
// returns them. All errors degrade silently: traffic stats must never
// break the stats endpoint.
func (t *netUsageTracker) Observe(rawRx, rawTx uint64, now time.Time) netUsageState {
	if !t.loaded {
		t.loaded = true
		t.load()
	}
	month := now.Format("2006-01")
	if t.state.Month != month {
		t.state.Month = month
		t.state.Rx, t.state.Tx = 0, 0
	}
	if t.state.LastRx > 0 || t.state.LastTx > 0 {
		t.state.Rx += counterDelta(rawRx, t.state.LastRx)
		t.state.Tx += counterDelta(rawTx, t.state.LastTx)
	}
	t.state.LastRx, t.state.LastTx = rawRx, rawTx
	if now.Sub(t.lastWrite) >= t.writeEvery {
		t.save()
		t.lastWrite = now
	}
	return t.state
}

func counterDelta(cur, prev uint64) uint64 {
	if cur < prev {
		return cur // counters reset (host reboot / namespace change)
	}
	return cur - prev
}

func (t *netUsageTracker) load() {
	data, err := os.ReadFile(t.path)
	if err != nil {
		return
	}
	var st netUsageState
	if json.Unmarshal(data, &st) == nil {
		t.state = st
	}
}

func (t *netUsageTracker) save() {
	if t.path == "" {
		return
	}
	data, err := json.Marshal(t.state)
	if err != nil {
		return
	}
	tmp := t.path + ".tmp"
	if err = os.WriteFile(tmp, data, 0o600); err != nil {
		return
	}
	_ = os.Rename(tmp, t.path)
}

// defaultNetUsagePath places the state file next to the working directory
// data so blue/green slot switches keep one shared history when the working
// directory is stable, and fall back to per-slot otherwise.
func defaultNetUsagePath() string {
	if wd, err := os.Getwd(); err == nil {
		return filepath.Join(wd, "server-net-usage.json")
	}
	return ""
}
