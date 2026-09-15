package redisqueue

import (
	"sort"
	"strings"
	"sync"
	"time"

	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
)

const maxUsageMonitorRows = 4096

// UsageTokenTotals contains canonical, non-overlapping token counters.
type UsageTokenTotals struct {
	InputTokens         int64 `json:"input_tokens"`
	OutputTokens        int64 `json:"output_tokens"`
	ReasoningTokens     int64 `json:"reasoning_tokens"`
	CacheReadTokens     int64 `json:"cache_read_tokens"`
	CacheCreationTokens int64 `json:"cache_creation_tokens"`
	UnclassifiedTokens  int64 `json:"unclassified_tokens"`
	TotalTokens         int64 `json:"total_tokens"`
}

// UsageModelStats contains accumulated usage for one provider and model.
type UsageModelStats struct {
	Model            string           `json:"model"`
	Requests         int64            `json:"requests"`
	Success          int64            `json:"success"`
	Failed           int64            `json:"failed"`
	AverageLatencyMS int64            `json:"average_latency_ms"`
	LastUsedAt       time.Time        `json:"last_used_at"`
	Tokens           UsageTokenTotals `json:"tokens"`
	latencyTotalMS   int64
}

// UsageProviderStats contains model usage grouped under one provider.
type UsageProviderStats struct {
	Provider         string            `json:"provider"`
	Requests         int64             `json:"requests"`
	Success          int64             `json:"success"`
	Failed           int64             `json:"failed"`
	AverageLatencyMS int64             `json:"average_latency_ms"`
	LastUsedAt       time.Time         `json:"last_used_at"`
	Tokens           UsageTokenTotals  `json:"tokens"`
	Models           []UsageModelStats `json:"models"`
	latencyTotalMS   int64
}

// UsageMonitorSnapshot is a non-destructive view of accumulated usage.
type UsageMonitorSnapshot struct {
	Enabled   bool                 `json:"enabled"`
	Since     time.Time            `json:"since"`
	UpdatedAt *time.Time           `json:"updated_at,omitempty"`
	Truncated bool                 `json:"truncated"`
	Requests  int64                `json:"requests"`
	Success   int64                `json:"success"`
	Failed    int64                `json:"failed"`
	Tokens    UsageTokenTotals     `json:"tokens"`
	Providers []UsageProviderStats `json:"providers"`
}

type usageMonitorState struct {
	mu        sync.RWMutex
	since     time.Time
	updatedAt time.Time
	truncated bool
	requests  int64
	success   int64
	failed    int64
	tokens    UsageTokenTotals
	rows      map[string]*UsageModelStats
}

var usageMonitor = usageMonitorState{
	since: time.Now(),
	rows:  make(map[string]*UsageModelStats),
}

func normalizeUsageDimension(value, fallback string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback
	}
	return value
}

func addUsageTokens(target *UsageTokenTotals, source UsageTokenTotals) {
	target.InputTokens += source.InputTokens
	target.OutputTokens += source.OutputTokens
	target.ReasoningTokens += source.ReasoningTokens
	target.CacheReadTokens += source.CacheReadTokens
	target.CacheCreationTokens += source.CacheCreationTokens
	target.UnclassifiedTokens += source.UnclassifiedTokens
	target.TotalTokens += source.TotalTokens
}

func usageTokensFromDetail(detail coreusage.Detail) UsageTokenTotals {
	breakdown := detail.TokenBreakdown
	return UsageTokenTotals{
		InputTokens:         breakdown.Input.TotalTokens,
		OutputTokens:        breakdown.Output.TotalTokens,
		ReasoningTokens:     breakdown.Output.ReasoningTokens,
		CacheReadTokens:     breakdown.Input.CacheReadTokens,
		CacheCreationTokens: breakdown.Input.CacheWriteTokens,
		UnclassifiedTokens:  breakdown.UnclassifiedTokens,
		TotalTokens:         breakdown.TotalTokens,
	}
}

func observeUsageRecord(record coreusage.Record, detail coreusage.Detail, failed bool, timestamp time.Time) {
	provider := normalizeUsageDimension(record.Provider, "unknown")
	model := normalizeUsageDimension(record.Model, "unknown")
	key := provider + "\x00" + model
	tokens := usageTokensFromDetail(detail)
	latencyMS := record.Latency.Milliseconds()
	if latencyMS < 0 {
		latencyMS = 0
	}

	usageMonitor.mu.Lock()
	if usageMonitor.since.IsZero() {
		usageMonitor.since = timestamp
	}
	usageMonitor.updatedAt = timestamp
	usageMonitor.requests++
	if failed {
		usageMonitor.failed++
	} else {
		usageMonitor.success++
	}
	addUsageTokens(&usageMonitor.tokens, tokens)

	row := usageMonitor.rows[key]
	if row == nil {
		if len(usageMonitor.rows) >= maxUsageMonitorRows {
			usageMonitor.truncated = true
			usageMonitor.mu.Unlock()
			scheduleUsageMonitorPersistence()
			return
		}
		if usageMonitor.rows == nil {
			usageMonitor.rows = make(map[string]*UsageModelStats)
		}
		row = &UsageModelStats{Model: model}
		usageMonitor.rows[key] = row
	}
	row.Requests++
	if failed {
		row.Failed++
	} else {
		row.Success++
	}
	row.latencyTotalMS += latencyMS
	row.AverageLatencyMS = row.latencyTotalMS / row.Requests
	if timestamp.After(row.LastUsedAt) {
		row.LastUsedAt = timestamp
	}
	addUsageTokens(&row.Tokens, tokens)
	usageMonitor.mu.Unlock()
	scheduleUsageMonitorPersistence()
}

// ResetUsageMonitor clears accumulated usage and starts a new monitoring period.
func ResetUsageMonitor() {
	resetUsageMonitorInMemory(time.Now())
	persistUsageMonitorImmediately()
}

// GetUsageMonitorSnapshot returns a non-destructive aggregate sorted by token usage.
func GetUsageMonitorSnapshot() UsageMonitorSnapshot {
	usageMonitor.mu.RLock()
	defer usageMonitor.mu.RUnlock()

	providers := make(map[string]*UsageProviderStats)
	for key, stored := range usageMonitor.rows {
		if stored == nil {
			continue
		}
		providerName, _, _ := strings.Cut(key, "\x00")
		provider := providers[providerName]
		if provider == nil {
			provider = &UsageProviderStats{Provider: providerName}
			providers[providerName] = provider
		}
		row := *stored
		provider.Models = append(provider.Models, row)
		provider.Requests += row.Requests
		provider.Success += row.Success
		provider.Failed += row.Failed
		provider.latencyTotalMS += row.latencyTotalMS
		if row.LastUsedAt.After(provider.LastUsedAt) {
			provider.LastUsedAt = row.LastUsedAt
		}
		addUsageTokens(&provider.Tokens, row.Tokens)
	}

	ordered := make([]UsageProviderStats, 0, len(providers))
	for _, provider := range providers {
		if provider.Requests > 0 {
			provider.AverageLatencyMS = provider.latencyTotalMS / provider.Requests
		}
		sort.Slice(provider.Models, func(i, j int) bool {
			left, right := provider.Models[i], provider.Models[j]
			if left.Tokens.TotalTokens != right.Tokens.TotalTokens {
				return left.Tokens.TotalTokens > right.Tokens.TotalTokens
			}
			return left.Model < right.Model
		})
		ordered = append(ordered, *provider)
	}
	sort.Slice(ordered, func(i, j int) bool {
		left, right := ordered[i], ordered[j]
		if left.Tokens.TotalTokens != right.Tokens.TotalTokens {
			return left.Tokens.TotalTokens > right.Tokens.TotalTokens
		}
		return left.Provider < right.Provider
	})

	var updatedAt *time.Time
	if !usageMonitor.updatedAt.IsZero() {
		value := usageMonitor.updatedAt
		updatedAt = &value
	}
	return UsageMonitorSnapshot{
		Enabled:   UsageStatisticsEnabled(),
		Since:     usageMonitor.since,
		UpdatedAt: updatedAt,
		Truncated: usageMonitor.truncated,
		Requests:  usageMonitor.requests,
		Success:   usageMonitor.success,
		Failed:    usageMonitor.failed,
		Tokens:    usageMonitor.tokens,
		Providers: ordered,
	}
}
