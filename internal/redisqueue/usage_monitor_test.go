package redisqueue

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
)

func isolateUsageMonitorPersistence(t *testing.T) string {
	t.Helper()
	if errConfigure := ConfigureUsageMonitorPersistence(""); errConfigure != nil {
		t.Fatalf("ConfigureUsageMonitorPersistence() error = %v", errConfigure)
	}
	dir := t.TempDir()
	if errConfigure := ConfigureUsageMonitorPersistence(dir); errConfigure != nil {
		t.Fatalf("ConfigureUsageMonitorPersistence() error = %v", errConfigure)
	}
	t.Cleanup(func() {
		_ = ConfigureUsageMonitorPersistence("")
		resetUsageMonitorInMemory(time.Now())
	})
	return dir
}

func TestUsageMonitorAggregatesProviderModelTokens(t *testing.T) {
	isolateUsageMonitorPersistence(t)
	ResetUsageMonitor()
	t.Cleanup(ResetUsageMonitor)

	baseTime := time.Date(2026, time.September, 14, 8, 0, 0, 0, time.UTC)
	records := []struct {
		record coreusage.Record
		failed bool
	}{
		{
			record: coreusage.Record{
				Provider:    "commandcode",
				Model:       "glm-5",
				RequestedAt: baseTime,
				Latency:     1200 * time.Millisecond,
				Detail: coreusage.Detail{
					TokenBreakdown: coreusage.NewSubsetTokenBreakdown(100, 20, 10, 50, 15, 150),
				},
			},
		},
		{
			record: coreusage.Record{
				Provider:    "commandcode",
				Model:       "glm-5",
				RequestedAt: baseTime.Add(time.Minute),
				Latency:     800 * time.Millisecond,
				Detail: coreusage.Detail{
					TokenBreakdown: coreusage.NewSubsetTokenBreakdown(80, 0, 0, 20, 0, 100),
				},
			},
			failed: true,
		},
		{
			record: coreusage.Record{
				Provider:    "codex",
				Model:       "gpt-5.4",
				RequestedAt: baseTime.Add(2 * time.Minute),
				Latency:     500 * time.Millisecond,
				Detail: coreusage.Detail{
					TokenBreakdown: coreusage.NewSubsetTokenBreakdown(40, 0, 0, 10, 0, 50),
				},
			},
		},
	}

	for _, item := range records {
		observeUsageRecord(item.record, item.record.Detail, item.failed, item.record.RequestedAt)
	}

	snapshot := GetUsageMonitorSnapshot()
	if snapshot.Requests != 3 || snapshot.Success != 2 || snapshot.Failed != 1 {
		t.Fatalf("request totals = %d/%d/%d, want 3/2/1", snapshot.Requests, snapshot.Success, snapshot.Failed)
	}
	if snapshot.Tokens.TotalTokens != 300 || snapshot.Tokens.InputTokens != 220 || snapshot.Tokens.OutputTokens != 80 {
		t.Fatalf("token totals = %+v", snapshot.Tokens)
	}
	if len(snapshot.Providers) != 2 || snapshot.Providers[0].Provider != "commandcode" {
		t.Fatalf("providers = %+v", snapshot.Providers)
	}
	model := snapshot.Providers[0].Models[0]
	if model.Model != "glm-5" || model.Requests != 2 || model.Tokens.TotalTokens != 250 {
		t.Fatalf("model stats = %+v", model)
	}
	if model.AverageLatencyMS != 1000 {
		t.Fatalf("average latency = %d, want 1000", model.AverageLatencyMS)
	}
}

func TestResetUsageMonitorStartsNewPeriod(t *testing.T) {
	isolateUsageMonitorPersistence(t)
	ResetUsageMonitor()
	before := time.Now()
	ResetUsageMonitor()
	snapshot := GetUsageMonitorSnapshot()
	if snapshot.Since.Before(before) {
		t.Fatalf("since = %v, want >= %v", snapshot.Since, before)
	}
	if snapshot.Requests != 0 || len(snapshot.Providers) != 0 {
		t.Fatalf("reset snapshot = %+v", snapshot)
	}
}

func TestUsageMonitorDoesNotRequireRedisQueue(t *testing.T) {
	isolateUsageMonitorPersistence(t)
	previousQueueEnabled := Enabled()
	previousUsageEnabled := UsageStatisticsEnabled()
	SetEnabled(false)
	SetUsageStatisticsEnabled(true)
	ResetUsageMonitor()
	t.Cleanup(func() {
		SetEnabled(previousQueueEnabled)
		SetUsageStatisticsEnabled(previousUsageEnabled)
		ResetUsageMonitor()
	})

	plugin := &usageQueuePlugin{}
	plugin.HandleUsage(context.Background(), coreusage.Record{
		Provider: "codex",
		Model:    "gpt-5.4",
		Detail: coreusage.Detail{
			InputTokens:  12,
			OutputTokens: 8,
			TotalTokens:  20,
		},
	})

	snapshot := GetUsageMonitorSnapshot()
	if snapshot.Requests != 1 || snapshot.Tokens.TotalTokens != 20 {
		t.Fatalf("snapshot = %+v, want one request and 20 tokens", snapshot)
	}
	if items := PopOldest(10); len(items) != 0 {
		t.Fatalf("queue received %d items while disabled", len(items))
	}
}

func TestUsageMonitorPersistsAndRestoresSnapshot(t *testing.T) {
	dir := isolateUsageMonitorPersistence(t)
	ResetUsageMonitor()

	timestamp := time.Date(2026, time.September, 14, 17, 0, 0, 0, time.UTC)
	expectedSince := GetUsageMonitorSnapshot().Since
	record := coreusage.Record{
		Provider:    "codex",
		Model:       "gpt-5.4",
		RequestedAt: timestamp,
		Latency:     750 * time.Millisecond,
		Detail: coreusage.Detail{
			TokenBreakdown: coreusage.NewSubsetTokenBreakdown(90, 30, 5, 20, 7, 120),
		},
	}
	observeUsageRecord(record, record.Detail, false, timestamp)
	if errFlush := FlushUsageMonitorPersistence(); errFlush != nil {
		t.Fatalf("FlushUsageMonitorPersistence() error = %v", errFlush)
	}

	path := filepath.Join(dir, usageMonitorPersistenceDir, usageMonitorPersistenceFile)
	info, errStat := os.Stat(path)
	if errStat != nil {
		t.Fatalf("stat persisted snapshot: %v", errStat)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("snapshot mode = %o, want 600", info.Mode().Perm())
	}

	resetUsageMonitorInMemory(time.Now())
	if errConfigure := ConfigureUsageMonitorPersistence(""); errConfigure != nil {
		t.Fatalf("disable persistence: %v", errConfigure)
	}
	if errConfigure := ConfigureUsageMonitorPersistence(dir); errConfigure != nil {
		t.Fatalf("reload persistence: %v", errConfigure)
	}

	snapshot := GetUsageMonitorSnapshot()
	if snapshot.Requests != 1 || snapshot.Tokens.TotalTokens != 120 {
		t.Fatalf("restored snapshot = %+v", snapshot)
	}
	if !snapshot.Since.Equal(expectedSince) || snapshot.UpdatedAt == nil || !snapshot.UpdatedAt.Equal(timestamp) {
		t.Fatalf("restored timestamps since=%v updated_at=%v", snapshot.Since, snapshot.UpdatedAt)
	}
	if len(snapshot.Providers) != 1 || snapshot.Providers[0].Models[0].AverageLatencyMS != 750 {
		t.Fatalf("restored providers = %+v", snapshot.Providers)
	}
}

func TestResetUsageMonitorPersistsEmptySnapshot(t *testing.T) {
	dir := isolateUsageMonitorPersistence(t)
	timestamp := time.Date(2026, time.September, 14, 18, 0, 0, 0, time.UTC)
	record := coreusage.Record{Provider: "devin", Model: "devin/swe-2", RequestedAt: timestamp}
	observeUsageRecord(record, record.Detail, false, timestamp)
	ResetUsageMonitor()

	resetUsageMonitorInMemory(time.Now())
	if errConfigure := ConfigureUsageMonitorPersistence(""); errConfigure != nil {
		t.Fatalf("disable persistence: %v", errConfigure)
	}
	if errConfigure := ConfigureUsageMonitorPersistence(dir); errConfigure != nil {
		t.Fatalf("reload persistence: %v", errConfigure)
	}
	snapshot := GetUsageMonitorSnapshot()
	if snapshot.Requests != 0 || len(snapshot.Providers) != 0 {
		t.Fatalf("restored reset snapshot = %+v", snapshot)
	}
}

func TestUsageStatisticsTogglePreservesHistory(t *testing.T) {
	isolateUsageMonitorPersistence(t)
	previous := UsageStatisticsEnabled()
	SetUsageStatisticsEnabled(true)
	ResetUsageMonitor()
	t.Cleanup(func() { SetUsageStatisticsEnabled(previous) })

	record := coreusage.Record{Provider: "codex", Model: "gpt-5.4"}
	observeUsageRecord(record, record.Detail, false, time.Now())
	SetUsageStatisticsEnabled(false)

	snapshot := GetUsageMonitorSnapshot()
	if snapshot.Enabled || snapshot.Requests != 1 {
		t.Fatalf("disabled snapshot = %+v, want disabled with retained history", snapshot)
	}
}
