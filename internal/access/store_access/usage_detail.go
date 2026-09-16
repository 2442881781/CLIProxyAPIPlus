package storeaccess

import (
	"fmt"
	"maps"
	"sort"
	"time"
)

// Cardinality guards for per-key usage detail maps. Distinct dimensions
// beyond the cap fold into usageOtherBucket so totals stay accurate while the
// persisted store file stays bounded. Daily rows are pruned to a rolling
// window of usageDailyKeepDays days.
const (
	usageOtherBucket     = "__other__"
	maxUsageModelsPerKey = 64
	maxUsageAuthsPerKey  = 64
	usageDailyKeepDays   = 62
)

// DimUsage holds the counters for one attribution dimension (a model, a UTC
// day, or an upstream auth).
type DimUsage struct {
	Tokens     int64  `json:"tokens"`
	Requests   int64  `json:"requests"`
	Failed     int64  `json:"failed,omitempty"`
	LastUsedAt string `json:"last_used_at,omitempty"`
}

// UsageEvent carries one completed request's usage into the store. Model is
// the client-requested (alias) name; AuthID is the upstream credential that
// served the request.
type UsageEvent struct {
	Tokens  int64
	Failed  bool
	Model   string
	AuthID  string
	Latency time.Duration
	TTFT    time.Duration
}

// UsageTopEntry is one row of the usage leaderboard.
type UsageTopEntry struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	KeyPrefix string `json:"key_prefix"`
	Group     string `json:"group,omitempty"`
	Tokens    int64  `json:"tokens"`
	Requests  int64  `json:"requests"`
	Failed    int64  `json:"failed"`
}

// GroupUsageView aggregates member keys' usage detail for one group.
type GroupUsageView struct {
	Name           string              `json:"name"`
	Keys           int                 `json:"keys"`
	Totals         DimUsage            `json:"totals"`
	Models         map[string]DimUsage `json:"models"`
	Daily          map[string]DimUsage `json:"daily"`
	Auths          map[string]DimUsage `json:"auths"`
	LatencyTotalMS int64               `json:"latency_total_ms"`
	TTFTTotalMS    int64               `json:"ttft_total_ms"`
}

// copyUsage deep-copies usage maps so returned entries are detached from the
// live store and safe to walk while RecordUsage writes.
func copyUsage(u Usage) Usage {
	out := u
	out.Models = maps.Clone(u.Models)
	out.Daily = maps.Clone(u.Daily)
	out.Auths = maps.Clone(u.Auths)
	return out
}

func copyAccessKey(entry *AccessKey) *AccessKey {
	if entry == nil {
		return nil
	}
	out := *entry
	out.Usage = copyUsage(entry.Usage)
	return &out
}

// addDimLocked increments one dimension bucket, folding new keys into
// "__other__" once the map reaches cap.
func addDimLocked(m map[string]DimUsage, key string, cap int, ev UsageEvent, now time.Time) map[string]DimUsage {
	if key == "" {
		key = "unknown"
	}
	if m == nil {
		m = make(map[string]DimUsage)
	}
	dim, ok := m[key]
	if !ok && len(m) >= cap {
		key = usageOtherBucket
		dim = m[key]
	}
	dim.Tokens += ev.Tokens
	dim.Requests++
	if ev.Failed {
		dim.Failed++
	}
	dim.LastUsedAt = now.Format(time.RFC3339)
	m[key] = dim
	return m
}

// recordDetailLocked folds the event into the key's model/day/auth breakdowns.
// Caller must hold s.mu.
func (s *Store) recordDetailLocked(entry *AccessKey, ev UsageEvent, now time.Time) {
	entry.Usage.Models = addDimLocked(entry.Usage.Models, ev.Model, maxUsageModelsPerKey, ev, now)
	if ev.AuthID != "" {
		entry.Usage.Auths = addDimLocked(entry.Usage.Auths, ev.AuthID, maxUsageAuthsPerKey, ev, now)
	}
	day := now.Format("2006-01-02")
	entry.Usage.Daily = addDimLocked(entry.Usage.Daily, day, usageDailyKeepDays+1, ev, now)
	s.pruneDailyLocked(entry, now)
}

// pruneDailyLocked drops daily rows older than the rolling retention window.
// ISO day strings sort chronologically. Caller must hold s.mu.
func (s *Store) pruneDailyLocked(entry *AccessKey, now time.Time) {
	if len(entry.Usage.Daily) <= usageDailyKeepDays {
		return
	}
	cutoff := now.AddDate(0, 0, -(usageDailyKeepDays - 1)).Format("2006-01-02")
	for day := range entry.Usage.Daily {
		if day < cutoff {
			delete(entry.Usage.Daily, day)
		}
	}
}

// UsageSummary shapes a Usage for JSON responses, adding exact averages.
// includeAuths controls whether upstream-auth attribution is exposed (admin
// views only; self-service callers must pass false so pool internals are not
// leaked to key holders).
func UsageSummary(u Usage, includeAuths bool) map[string]any {
	requests := u.Requests
	if requests <= 0 {
		requests = 1
	}
	out := map[string]any{
		"total_tokens":     u.TotalTokens,
		"period_tokens":    u.PeriodTokens,
		"period_key":       u.PeriodKey,
		"requests":         u.Requests,
		"failed":           u.Failed,
		"last_used_at":     u.LastUsedAt,
		"day_key":          u.DayKey,
		"day_requests":     u.DayRequests,
		"avg_latency_ms":   u.LatencyTotalMS / requests,
		"avg_ttft_ms":      u.TTFTTotalMS / requests,
		"latency_total_ms": u.LatencyTotalMS,
		"ttft_total_ms":    u.TTFTTotalMS,
		"models":           u.Models,
		"daily":            u.Daily,
	}
	if includeAuths {
		out["auths"] = u.Auths
	}
	return out
}

// UsageTop ranks keys by usage. by is "tokens" (default), "requests", or
// "failed"; period is "all" (default), "month" (current UTC month, summed
// from daily rows), or "day" (today UTC). limit caps the result (default 20,
// max 200).
func (s *Store) UsageTop(by, period string, limit int) []UsageTopEntry {
	if s == nil {
		return nil
	}
	if limit <= 0 {
		limit = 20
	}
	if limit > 200 {
		limit = 200
	}
	now := s.nowTime().UTC()
	var monthPrefix, dayKey string
	switch period {
	case "month":
		monthPrefix = now.Format("2006-01")
	case "day":
		dayKey = now.Format("2006-01-02")
	}

	s.mu.RLock()
	entries := make([]UsageTopEntry, 0, len(s.keys))
	for _, entry := range s.keys {
		row := UsageTopEntry{
			ID:        entry.ID,
			Name:      entry.Name,
			KeyPrefix: entry.KeyPrefix,
			Group:     entry.Group,
			Tokens:    entry.Usage.TotalTokens,
			Requests:  entry.Usage.Requests,
			Failed:    entry.Usage.Failed,
		}
		if monthPrefix != "" || dayKey != "" {
			row.Tokens, row.Requests, row.Failed = 0, 0, 0
			for day, d := range entry.Usage.Daily {
				if (dayKey != "" && day != dayKey) || (monthPrefix != "" && day[:7] != monthPrefix) {
					continue
				}
				row.Tokens += d.Tokens
				row.Requests += d.Requests
				row.Failed += d.Failed
			}
		}
		entries = append(entries, row)
	}
	s.mu.RUnlock()

	metric := func(e UsageTopEntry) int64 {
		switch by {
		case "requests":
			return e.Requests
		case "failed":
			return e.Failed
		default:
			return e.Tokens
		}
	}
	sort.Slice(entries, func(i, j int) bool {
		if metric(entries[i]) != metric(entries[j]) {
			return metric(entries[i]) > metric(entries[j])
		}
		return entries[i].ID < entries[j].ID
	})
	if len(entries) > limit {
		entries = entries[:limit]
	}
	return entries
}

// GroupUsage aggregates member keys' usage detail for the named group.
// Note: a key's history travels with it, so a key moved between groups
// contributes its past rows to the new group.
func (s *Store) GroupUsage(name string) (*GroupUsageView, error) {
	if s == nil || !s.loaded {
		return nil, fmt.Errorf("access key store not initialized")
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if _, ok := s.groups[name]; !ok {
		return nil, fmt.Errorf("group not found")
	}
	view := &GroupUsageView{
		Name:   name,
		Models: make(map[string]DimUsage),
		Daily:  make(map[string]DimUsage),
		Auths:  make(map[string]DimUsage),
	}
	for _, entry := range s.keys {
		if entry.Group != name {
			continue
		}
		view.Keys++
		view.Totals.Tokens += entry.Usage.TotalTokens
		view.Totals.Requests += entry.Usage.Requests
		view.Totals.Failed += entry.Usage.Failed
		if entry.Usage.LastUsedAt > view.Totals.LastUsedAt {
			view.Totals.LastUsedAt = entry.Usage.LastUsedAt
		}
		view.LatencyTotalMS += entry.Usage.LatencyTotalMS
		view.TTFTTotalMS += entry.Usage.TTFTTotalMS
		for model, d := range entry.Usage.Models {
			acc := view.Models[model]
			acc.Tokens += d.Tokens
			acc.Requests += d.Requests
			acc.Failed += d.Failed
			if d.LastUsedAt > acc.LastUsedAt {
				acc.LastUsedAt = d.LastUsedAt
			}
			view.Models[model] = acc
		}
		for day, d := range entry.Usage.Daily {
			acc := view.Daily[day]
			acc.Tokens += d.Tokens
			acc.Requests += d.Requests
			acc.Failed += d.Failed
			if d.LastUsedAt > acc.LastUsedAt {
				acc.LastUsedAt = d.LastUsedAt
			}
			view.Daily[day] = acc
		}
		for auth, d := range entry.Usage.Auths {
			acc := view.Auths[auth]
			acc.Tokens += d.Tokens
			acc.Requests += d.Requests
			acc.Failed += d.Failed
			if d.LastUsedAt > acc.LastUsedAt {
				acc.LastUsedAt = d.LastUsedAt
			}
			view.Auths[auth] = acc
		}
	}
	return view, nil
}
