package auth

import (
	"sort"
	"strings"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

const routingDiagnosticsLimit = 128

// RoutingCandidateDiagnostic summarizes non-secret quota state for one provider.
type RoutingCandidateDiagnostic struct {
	Provider                  string   `json:"provider"`
	CandidateCount            int      `json:"candidate_count"`
	KnownQuotaCount           int      `json:"known_quota_count"`
	UnknownQuotaCount         int      `json:"unknown_quota_count"`
	BestRemainingPercent      *float64 `json:"best_remaining_percent,omitempty"`
	FreshestObservationAgeSec *int64   `json:"freshest_observation_age_seconds,omitempty"`
}

// RoutingDiagnostic records one mixed-provider scheduler decision without
// credential identifiers, request content, or upstream response data.
type RoutingDiagnostic struct {
	ObservedAt       time.Time                    `json:"observed_at"`
	Model            string                       `json:"model"`
	SelectedProvider string                       `json:"selected_provider"`
	SelectionReason  string                       `json:"selection_reason"`
	AffinityBound    bool                         `json:"affinity_bound"`
	Candidates       []RoutingCandidateDiagnostic `json:"candidates"`
}

// RecentRoutingDiagnostics returns a newest-first snapshot of recent mixed-pool
// routing decisions. The in-memory buffer is intentionally bounded and is not
// persisted.
func (m *Manager) RecentRoutingDiagnostics(limit int) []RoutingDiagnostic {
	if m == nil {
		return nil
	}
	m.routingDiagnosticsMu.Lock()
	defer m.routingDiagnosticsMu.Unlock()
	if limit <= 0 || limit > len(m.routingDiagnostics) {
		limit = len(m.routingDiagnostics)
	}
	out := make([]RoutingDiagnostic, 0, limit)
	for index := len(m.routingDiagnostics) - 1; index >= len(m.routingDiagnostics)-limit; index-- {
		out = append(out, cloneRoutingDiagnostic(m.routingDiagnostics[index]))
	}
	return out
}

func (m *Manager) recordRoutingDiagnostic(model string, selected *Auth, candidates []*Auth, opts cliproxyexecutor.Options, now time.Time) {
	if m == nil || selected == nil || len(candidates) == 0 {
		return
	}
	model = strings.TrimSpace(model)
	if model == "" {
		return
	}
	if now.IsZero() {
		now = time.Now()
	}

	type providerSummary struct {
		diagnostic RoutingCandidateDiagnostic
		freshest   time.Time
	}
	summaries := make(map[string]*providerSummary)
	allKnown := true
	knownValues := make([]float64, 0, len(candidates))
	for _, candidate := range candidates {
		if candidate == nil {
			continue
		}
		provider := executorKeyFromAuth(candidate)
		if provider == "" {
			continue
		}
		summary := summaries[provider]
		if summary == nil {
			summary = &providerSummary{diagnostic: RoutingCandidateDiagnostic{Provider: provider}}
			summaries[provider] = summary
		}
		summary.diagnostic.CandidateCount++
		observation := authQuotaRemainingPercent(candidate, model, now)
		if !observation.known {
			summary.diagnostic.UnknownQuotaCount++
			allKnown = false
			continue
		}
		summary.diagnostic.KnownQuotaCount++
		knownValues = append(knownValues, observation.percent)
		if summary.diagnostic.BestRemainingPercent == nil || observation.percent > *summary.diagnostic.BestRemainingPercent {
			value := observation.percent
			summary.diagnostic.BestRemainingPercent = &value
		}
		if observation.observedAt.After(summary.freshest) {
			summary.freshest = observation.observedAt
		}
	}

	providers := make([]string, 0, len(summaries))
	for provider := range summaries {
		providers = append(providers, provider)
	}
	sort.Strings(providers)
	candidateDiagnostics := make([]RoutingCandidateDiagnostic, 0, len(providers))
	for _, provider := range providers {
		summary := summaries[provider]
		if !summary.freshest.IsZero() {
			age := int64(now.Sub(summary.freshest).Seconds())
			if age < 0 {
				age = 0
			}
			summary.diagnostic.FreshestObservationAgeSec = &age
		}
		candidateDiagnostics = append(candidateDiagnostics, summary.diagnostic)
	}

	selectedObservation := authQuotaRemainingPercent(selected, model, now)
	maxRemaining := -1.0
	for _, value := range knownValues {
		if value > maxRemaining {
			maxRemaining = value
		}
	}
	affinityBound := false
	if opts.Metadata != nil {
		if value, ok := opts.Metadata[cliproxyexecutor.CanonicalSessionIDMetadataKey].(string); ok {
			affinityBound = strings.TrimSpace(value) != ""
		}
		if !affinityBound {
			if value, ok := opts.Metadata[cliproxyexecutor.LCPAffinitySessionIDMetadataKey].(string); ok {
				affinityBound = strings.TrimSpace(value) != ""
			}
		}
	}

	reason := "configured_strategy"
	switch {
	case affinityBound && selectedObservation.known && maxRemaining >= 0 && selectedObservation.percent < maxRemaining-quotaPercentEpsilon:
		reason = "session_affinity"
	case !allKnown:
		reason = "configured_strategy_unknown_quota"
	case selectedObservation.known && maxRemaining >= 0 && selectedObservation.percent >= maxRemaining-quotaPercentEpsilon && hasLowerQuota(knownValues, maxRemaining):
		reason = "highest_remaining_quota"
	case len(knownValues) > 1:
		reason = "configured_strategy_quota_tie"
	}

	diagnostic := RoutingDiagnostic{
		ObservedAt:       now.UTC(),
		Model:            model,
		SelectedProvider: executorKeyFromAuth(selected),
		SelectionReason:  reason,
		AffinityBound:    affinityBound,
		Candidates:       candidateDiagnostics,
	}
	m.routingDiagnosticsMu.Lock()
	if len(m.routingDiagnostics) >= routingDiagnosticsLimit {
		copy(m.routingDiagnostics, m.routingDiagnostics[len(m.routingDiagnostics)-routingDiagnosticsLimit+1:])
		m.routingDiagnostics = m.routingDiagnostics[:routingDiagnosticsLimit-1]
	}
	m.routingDiagnostics = append(m.routingDiagnostics, diagnostic)
	m.routingDiagnosticsMu.Unlock()
}

func hasLowerQuota(values []float64, maxRemaining float64) bool {
	for _, value := range values {
		if value < maxRemaining-quotaPercentEpsilon {
			return true
		}
	}
	return false
}

func cloneRoutingDiagnostic(diagnostic RoutingDiagnostic) RoutingDiagnostic {
	clone := diagnostic
	clone.Candidates = make([]RoutingCandidateDiagnostic, len(diagnostic.Candidates))
	copy(clone.Candidates, diagnostic.Candidates)
	for index := range clone.Candidates {
		if diagnostic.Candidates[index].BestRemainingPercent != nil {
			value := *diagnostic.Candidates[index].BestRemainingPercent
			clone.Candidates[index].BestRemainingPercent = &value
		}
		if diagnostic.Candidates[index].FreshestObservationAgeSec != nil {
			value := *diagnostic.Candidates[index].FreshestObservationAgeSec
			clone.Candidates[index].FreshestObservationAgeSec = &value
		}
	}
	return clone
}
