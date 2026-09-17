package redisqueue

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
)

// fakeUsageMonitorPersister is an in-memory durable backend for tests.
type fakeUsageMonitorPersister struct {
	mu      sync.Mutex
	data    []byte
	loadErr error
	saveErr error
	loads   int
	saves   int
}

func (f *fakeUsageMonitorPersister) LoadUsageMonitor(context.Context) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.loads++
	if f.loadErr != nil {
		return nil, f.loadErr
	}
	return append([]byte(nil), f.data...), nil
}

func (f *fakeUsageMonitorPersister) SaveUsageMonitor(_ context.Context, data []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.saves++
	if f.saveErr != nil {
		return f.saveErr
	}
	f.data = append([]byte(nil), data...)
	return nil
}

func (f *fakeUsageMonitorPersister) snapshot() []byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]byte(nil), f.data...)
}

func (f *fakeUsageMonitorPersister) setSnapshot(data []byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.data = append([]byte(nil), data...)
}

// withUsageMonitorBackend installs a fake backend for the duration of a test.
func withUsageMonitorBackend(t *testing.T, persister *fakeUsageMonitorPersister) {
	t.Helper()
	SetUsageMonitorPersister(persister)
	t.Cleanup(func() {
		SetUsageMonitorPersister(nil)
		if errConfigure := ConfigureUsageMonitorPersistence(""); errConfigure != nil {
			t.Fatalf("reset persistence: %v", errConfigure)
		}
		resetUsageMonitorInMemory(time.Now())
	})
}

func recordSwe2Usage(t *testing.T, timestamp time.Time, totalTokens int64) {
	t.Helper()
	record := coreusage.Record{
		Provider:    "devin",
		Model:       "devin/swe-2",
		RequestedAt: timestamp,
		Latency:     1500 * time.Millisecond,
		Detail: coreusage.Detail{
			TokenBreakdown: coreusage.NewSubsetTokenBreakdown(100, 40, 9, 30, 0, totalTokens),
		},
	}
	observeUsageRecord(record, record.Detail, false, timestamp)
}

// The backend must receive every flushed snapshot so a slot switch cannot lose
// history even when the auth spool is rebuilt from scratch.
func TestUsageMonitorPersistsSnapshotToBackend(t *testing.T) {
	persister := &fakeUsageMonitorPersister{}
	withUsageMonitorBackend(t, persister)

	dir := t.TempDir()
	if errConfigure := ConfigureUsageMonitorPersistence(dir); errConfigure != nil {
		t.Fatalf("ConfigureUsageMonitorPersistence() error = %v", errConfigure)
	}
	ResetUsageMonitor()

	timestamp := time.Date(2026, time.September, 17, 10, 0, 0, 0, time.UTC)
	recordSwe2Usage(t, timestamp, 179)
	if errFlush := FlushUsageMonitorPersistence(); errFlush != nil {
		t.Fatalf("FlushUsageMonitorPersistence() error = %v", errFlush)
	}
	if len(persister.snapshot()) == 0 {
		t.Fatal("backend received no snapshot")
	}

	// Simulate the production failure: fresh process, wiped auth spool.
	resetUsageMonitorInMemory(time.Now())
	if errConfigure := ConfigureUsageMonitorPersistence(""); errConfigure != nil {
		t.Fatalf("disable persistence: %v", errConfigure)
	}
	if errRemove := os.RemoveAll(filepath.Join(dir, usageMonitorPersistenceDir)); errRemove != nil {
		t.Fatalf("remove local snapshot: %v", errRemove)
	}
	if errConfigure := ConfigureUsageMonitorPersistence(dir); errConfigure != nil {
		t.Fatalf("reload persistence: %v", errConfigure)
	}

	snapshot := GetUsageMonitorSnapshot()
	if snapshot.Requests != 1 || snapshot.Tokens.TotalTokens != 179 {
		t.Fatalf("restored from backend = %+v", snapshot)
	}
	if len(snapshot.Providers) != 1 || snapshot.Providers[0].Provider != "devin" {
		t.Fatalf("restored providers = %+v", snapshot.Providers)
	}
}

// Deployments that predate the backend still hold their history in the local
// snapshot file; the first run must promote it into the backend.
func TestUsageMonitorSeedsBackendFromLocalSnapshot(t *testing.T) {
	dir := isolateUsageMonitorPersistence(t)
	ResetUsageMonitor()

	timestamp := time.Date(2026, time.September, 17, 9, 0, 0, 0, time.UTC)
	recordSwe2Usage(t, timestamp, 240)
	if errFlush := FlushUsageMonitorPersistence(); errFlush != nil {
		t.Fatalf("FlushUsageMonitorPersistence() error = %v", errFlush)
	}

	persister := &fakeUsageMonitorPersister{}
	withUsageMonitorBackend(t, persister)

	resetUsageMonitorInMemory(time.Now())
	if errConfigure := ConfigureUsageMonitorPersistence(""); errConfigure != nil {
		t.Fatalf("disable persistence: %v", errConfigure)
	}
	if errConfigure := ConfigureUsageMonitorPersistence(dir); errConfigure != nil {
		t.Fatalf("reload persistence: %v", errConfigure)
	}

	if len(persister.snapshot()) == 0 {
		t.Fatal("backend was not seeded from the local snapshot")
	}
	snapshot := GetUsageMonitorSnapshot()
	if snapshot.Requests != 1 || snapshot.Tokens.TotalTokens != 240 {
		t.Fatalf("restored snapshot = %+v", snapshot)
	}
}

// The backend is authoritative once it has a snapshot: a stale local file must
// not roll the counters back.
func TestUsageMonitorPrefersBackendOverLocalSnapshot(t *testing.T) {
	dir := isolateUsageMonitorPersistence(t)
	ResetUsageMonitor()

	timestamp := time.Date(2026, time.September, 17, 9, 30, 0, 0, time.UTC)
	recordSwe2Usage(t, timestamp, 10)
	if errFlush := FlushUsageMonitorPersistence(); errFlush != nil {
		t.Fatalf("FlushUsageMonitorPersistence() error = %v", errFlush)
	}

	backend := persistedUsageMonitor{
		Version:   usageMonitorPersistenceVersion,
		Since:     time.Date(2026, time.September, 17, 7, 0, 0, 0, time.UTC),
		UpdatedAt: timestamp,
		Requests:  7,
		Success:   7,
		Tokens:    UsageTokenTotals{TotalTokens: 700},
		Rows: []persistedUsageModelStats{{
			Provider: "codex",
			Model:    "gpt-5.6-sol",
			Requests: 7,
			Success:  7,
			Tokens:   UsageTokenTotals{TotalTokens: 700},
		}},
	}
	encoded, errMarshal := json.Marshal(backend)
	if errMarshal != nil {
		t.Fatalf("marshal backend snapshot: %v", errMarshal)
	}

	persister := &fakeUsageMonitorPersister{}
	persister.setSnapshot(encoded)
	withUsageMonitorBackend(t, persister)

	resetUsageMonitorInMemory(time.Now())
	if errConfigure := ConfigureUsageMonitorPersistence(""); errConfigure != nil {
		t.Fatalf("disable persistence: %v", errConfigure)
	}
	if errConfigure := ConfigureUsageMonitorPersistence(dir); errConfigure != nil {
		t.Fatalf("reload persistence: %v", errConfigure)
	}

	snapshot := GetUsageMonitorSnapshot()
	if snapshot.Requests != 7 || snapshot.Tokens.TotalTokens != 700 {
		t.Fatalf("backend snapshot was not authoritative = %+v", snapshot)
	}
	if len(snapshot.Providers) != 1 || snapshot.Providers[0].Provider != "codex" {
		t.Fatalf("restored providers = %+v", snapshot.Providers)
	}
}

// A backend keeps persistence active even when no auth directory is set, so an
// unset auth-dir no longer disables usage history.
func TestUsageMonitorPersistsToBackendWithoutAuthDir(t *testing.T) {
	persister := &fakeUsageMonitorPersister{}
	withUsageMonitorBackend(t, persister)

	dir := t.TempDir()
	if errConfigure := ConfigureUsageMonitorPersistence(dir); errConfigure != nil {
		t.Fatalf("ConfigureUsageMonitorPersistence() error = %v", errConfigure)
	}
	ResetUsageMonitor()

	timestamp := time.Date(2026, time.September, 17, 10, 30, 0, 0, time.UTC)
	recordSwe2Usage(t, timestamp, 55)
	if errFlush := FlushUsageMonitorPersistence(); errFlush != nil {
		t.Fatalf("FlushUsageMonitorPersistence() error = %v", errFlush)
	}
	if len(persister.snapshot()) == 0 {
		t.Fatal("backend received no snapshot")
	}

	var stored persistedUsageMonitor
	if errUnmarshal := json.Unmarshal(persister.snapshot(), &stored); errUnmarshal != nil {
		t.Fatalf("decode backend snapshot: %v", errUnmarshal)
	}
	if stored.Requests != 1 || stored.Tokens.TotalTokens != 55 {
		t.Fatalf("backend snapshot = %+v", stored)
	}

	// Dropping the local path must not disable the backend.
	resetUsageMonitorInMemory(time.Now())
	if errConfigure := ConfigureUsageMonitorPersistence(""); errConfigure != nil {
		t.Fatalf("ConfigureUsageMonitorPersistence(\"\") error = %v", errConfigure)
	}
	snapshot := GetUsageMonitorSnapshot()
	if snapshot.Requests != 1 || snapshot.Tokens.TotalTokens != 55 {
		t.Fatalf("restored without auth dir = %+v", snapshot)
	}
}

// A failing backend must not block file persistence or crash the caller.
func TestUsageMonitorBackendSaveFailureKeepsFileMirror(t *testing.T) {
	dir := isolateUsageMonitorPersistence(t)
	ResetUsageMonitor()

	persister := &fakeUsageMonitorPersister{saveErr: errors.New("backend unavailable")}
	withUsageMonitorBackend(t, persister)

	path := filepath.Join(dir, usageMonitorPersistenceDir, usageMonitorPersistenceFile)
	if errRemove := os.Remove(path); errRemove != nil && !errors.Is(errRemove, os.ErrNotExist) {
		t.Fatalf("remove previous snapshot: %v", errRemove)
	}

	timestamp := time.Date(2026, time.September, 17, 11, 0, 0, 0, time.UTC)
	recordSwe2Usage(t, timestamp, 33)
	if errFlush := FlushUsageMonitorPersistence(); errFlush == nil {
		t.Fatal("expected the backend save error to surface")
	}

	data, errRead := os.ReadFile(path)
	if errRead != nil {
		t.Fatalf("file mirror was not written: %v", errRead)
	}
	var mirrored persistedUsageMonitor
	if errUnmarshal := json.Unmarshal(data, &mirrored); errUnmarshal != nil {
		t.Fatalf("decode file mirror: %v", errUnmarshal)
	}
	if mirrored.Requests != 1 || mirrored.Tokens.TotalTokens != 33 {
		t.Fatalf("file mirror = %+v", mirrored)
	}
}
