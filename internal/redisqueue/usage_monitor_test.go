package redisqueue

import (
	"context"
	"testing"
	"time"

	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
)

func TestUsageMonitorAggregatesProviderModelTokens(t *testing.T) {
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
