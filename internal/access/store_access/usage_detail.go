package storeaccess

import (
	"fmt"
	"maps"
	"sort"
	"time"

	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
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
	// InBytes/OutBytes are payload bytes the proxy received and sent for this
	// dimension, summed across both the client and the provider leg.
	InBytes  int64 `json:"in_bytes,omitempty"`
	OutBytes int64 `json:"out_bytes,omitempty"`
}

// UsageEvent carries one completed request's usage into the store. Model is
// the client-requested (alias) name; AuthID is the upstream credential that
// served the request.
type UsageEvent struct {
	Tokens       int64
	InputTokens  int64
	OutputTokens int64
	Failed       bool
	Model        string
	AuthID       string
	Latency      time.Duration
	TTFT         time.Duration
	// Provider-leg payload bytes: what the proxy sent upstream and read back.
	UpstreamInBytes  int64
	UpstreamOutBytes int64
}

// TrafficEvent carries the client-facing byte counts observed at the HTTP edge
// for one request. It is recorded separately from UsageEvent because the
// response size is only known after the handler has written the body.
type TrafficEvent struct {
	InBytes  int64 // request payload bytes read from the client
	OutBytes int64 // response payload bytes written to the client
	Model    string
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
	BytesIn   int64  `json:"bytes_in"`
	BytesOut  int64  `json:"bytes_out"`
	Bytes     int64  `json:"bytes"`
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

// WithoutTraffic returns a deep copy with every byte counter cleared, including
// the per-dimension maps. Self-service responses use it so key holders can see
// tokens and requests without any traffic figure; the live store is never
// mutated.
func (u Usage) WithoutTraffic() Usage {
	out := copyUsage(u)
	out.ClientInBytes, out.ClientOutBytes = 0, 0
	out.UpstreamInBytes, out.UpstreamOutBytes = 0, 0
	out.PeriodBytes = 0
	out.Models = stripDimTraffic(out.Models)
	out.Daily = stripDimTraffic(out.Daily)
	out.Auths = stripDimTraffic(out.Auths)
	return out
}

func stripDimTraffic(m map[string]DimUsage) map[string]DimUsage {
	if m == nil {
		return nil
	}
	out := make(map[string]DimUsage, len(m))
	for key, dim := range m {
		dim.InBytes, dim.OutBytes = 0, 0
		out[key] = dim
	}
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
	dim.InBytes += ev.UpstreamInBytes
	dim.OutBytes += ev.UpstreamOutBytes
	dim.Requests++
	if ev.Failed {
		dim.Failed++
	}
	dim.LastUsedAt = now.Format(time.RFC3339)
	m[key] = dim
	return m
}

// addTrafficDimLocked increments one dimension's byte counters without
// touching request/token counters, so client-edge traffic can be folded in
// separately from the provider-leg usage event.
func addTrafficDimLocked(m map[string]DimUsage, key string, capacity int, ev TrafficEvent) map[string]DimUsage {
	if key == "" {
		key = "unknown"
	}
	if m == nil {
		m = make(map[string]DimUsage)
	}
	if _, ok := m[key]; !ok && len(m) >= capacity {
		key = usageOtherBucket
	}
	dim := m[key]
	dim.InBytes += ev.InBytes
	dim.OutBytes += ev.OutBytes
	m[key] = dim
	return m
}

// recordTrafficDetailLocked folds client-edge traffic into the key's
// model/day breakdowns. Upstream bytes are attributed to the auth dimension by
// recordDetailLocked, which knows the provider credential; the edge only sees
// the requested model. Caller must hold s.mu.
func (s *Store) recordTrafficDetailLocked(entry *AccessKey, ev TrafficEvent, now time.Time) {
	entry.Usage.Models = addTrafficDimLocked(entry.Usage.Models, ev.Model, maxUsageModelsPerKey, ev)
	day := now.Format("2006-01-02")
	entry.Usage.Daily = addTrafficDimLocked(entry.Usage.Daily, day, usageDailyKeepDays+1, ev)
	s.pruneDailyLocked(entry, now)
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

// TrafficSummary shapes the byte counters for JSON responses. total is the
// bandwidth this key cost the host across both legs; client_* is only the leg
// between the client and this proxy, upstream_* only the provider leg.
func TrafficSummary(u Usage) map[string]any {
	client := u.ClientInBytes + u.ClientOutBytes
	upstream := u.UpstreamInBytes + u.UpstreamOutBytes
	return map[string]any{
		"client_in":      u.ClientInBytes,
		"client_out":     u.ClientOutBytes,
		"upstream_in":    u.UpstreamInBytes,
		"upstream_out":   u.UpstreamOutBytes,
		"client_total":   client,
		"upstream_total": upstream,
		"total":          client + upstream,
		"period_total":   u.PeriodBytes,
		"total_gb":       float64(client+upstream) / float64(1<<30),
		"period_gb":      float64(u.PeriodBytes) / float64(1<<30),
	}
}

// UsageSummary shapes a Usage for JSON responses, adding exact averages and
// the key's live rate gauge. includeAuths controls whether upstream-auth
// attribution is exposed (admin views only; self-service callers must pass
// false so pool internals are not leaked to key holders). includeTraffic
// controls whether byte accounting is exposed anywhere in the summary; with
// false every byte counter is stripped from the totals and the dimension rows,
// so self-service callers never see traffic cost.
func UsageSummary(u Usage, includeAuths, includeTraffic bool, rate coreusage.Rate) map[string]any {
	if !includeTraffic {
		u = u.WithoutTraffic()
	}
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
		"rates":            rate,
	}
	if includeAuths {
		out["auths"] = u.Auths
	}
	if includeTraffic {
		out["bytes"] = TrafficSummary(u)
	}
	return out
}

// UsageTop ranks keys by usage. by is "tokens" (default), "requests",
// "failed", or "bytes" (total payload bytes across both legs); period is "all"
// (default), "month" (current UTC month, summed from daily rows), or "day"
// (today UTC). limit caps the result (default 20, max 200).
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
			BytesIn:   entry.Usage.ClientInBytes + entry.Usage.UpstreamInBytes,
			BytesOut:  entry.Usage.ClientOutBytes + entry.Usage.UpstreamOutBytes,
			Bytes:     entry.Usage.TotalBytes(),
		}
		if monthPrefix != "" || dayKey != "" {
			row.Tokens, row.Requests, row.Failed = 0, 0, 0
			row.BytesIn, row.BytesOut, row.Bytes = 0, 0, 0
			for day, d := range entry.Usage.Daily {
				if (dayKey != "" && day != dayKey) || (monthPrefix != "" && day[:7] != monthPrefix) {
					continue
				}
				row.Tokens += d.Tokens
				row.Requests += d.Requests
				row.Failed += d.Failed
				row.BytesIn += d.InBytes
				row.BytesOut += d.OutBytes
			}
			row.Bytes = row.BytesIn + row.BytesOut
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
		case "bytes":
			return e.Bytes
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
		view.Totals.InBytes += entry.Usage.ClientInBytes + entry.Usage.UpstreamInBytes
		view.Totals.OutBytes += entry.Usage.ClientOutBytes + entry.Usage.UpstreamOutBytes
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
			acc.InBytes += d.InBytes
			acc.OutBytes += d.OutBytes
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
			acc.InBytes += d.InBytes
			acc.OutBytes += d.OutBytes
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
			acc.InBytes += d.InBytes
			acc.OutBytes += d.OutBytes
			if d.LastUsedAt > acc.LastUsedAt {
				acc.LastUsedAt = d.LastUsedAt
			}
			view.Auths[auth] = acc
		}
	}
	return view, nil
}
