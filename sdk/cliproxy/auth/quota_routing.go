package auth

import (
	"math"
	"strconv"
	"strings"
	"time"
)

const (
	// quotaRoutingObservationTTL bounds how long passive upstream quota watermarks
	// may influence credential selection. Stale observations fall back to the
	// configured routing strategy instead of being treated as low quota.
	quotaRoutingObservationTTL = 15 * time.Minute
	// quotaMigrationThresholdPercent is the remaining-capacity threshold below
	// which an established session binding may proactively move to another auth.
	quotaMigrationThresholdPercent  = 10.0
	quotaObservationFutureTolerance = time.Minute
	quotaPercentEpsilon             = 0.000001
)

type quotaRemainingObservation struct {
	percent    float64
	observedAt time.Time
	known      bool
}

// authQuotaRemainingPercent returns the freshest model-scoped or credential-scoped
// remaining quota percentage understood for routing. Unknown, malformed, future,
// and stale observations are deliberately ignored.
func authQuotaRemainingPercent(auth *Auth, model string, now time.Time) quotaRemainingObservation {
	if auth == nil {
		return quotaRemainingObservation{}
	}
	provider := executorKeyFromAuth(auth)
	modelKey := canonicalModelKey(model)
	var freshest quotaRemainingObservation
	if modelKey != "" {
		for stateModel, state := range auth.ModelStates {
			if state == nil || canonicalModelKey(stateModel) != modelKey {
				continue
			}
			if observation := quotaStateRemainingPercent(provider, state.Quota, now); observation.known &&
				(!freshest.known || observation.observedAt.After(freshest.observedAt)) {
				freshest = observation
			}
		}
	}
	if freshest.known {
		return freshest
	}
	return quotaStateRemainingPercent(provider, auth.Quota, now)
}

func quotaStateRemainingPercent(provider string, quota QuotaState, now time.Time) quotaRemainingObservation {
	if len(quota.Signals) == 0 || quota.ObservedAt.IsZero() {
		return quotaRemainingObservation{}
	}
	if now.IsZero() {
		now = time.Now()
	}
	if quota.ObservedAt.After(now.Add(quotaObservationFutureTolerance)) || now.Sub(quota.ObservedAt) > quotaRoutingObservationTTL {
		return quotaRemainingObservation{}
	}

	var remaining float64
	var ok bool
	if normalized, normalizedOK := normalizedRemainingPercent(quota.Signals); normalizedOK {
		return quotaRemainingObservation{percent: normalized, observedAt: quota.ObservedAt, known: true}
	}
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "claude":
		remaining, ok = claudeRemainingPercent(quota.Signals)
	case "codex":
		remaining, ok = codexRemainingPercent(quota.Signals)
	case "devin":
		remaining, ok = devinRemainingPercent(quota.Signals)
	default:
		return quotaRemainingObservation{}
	}
	if !ok || math.IsNaN(remaining) || math.IsInf(remaining, 0) {
		return quotaRemainingObservation{}
	}
	return quotaRemainingObservation{percent: clampQuotaPercent(remaining), observedAt: quota.ObservedAt, known: true}
}

func normalizedRemainingPercent(signals map[string]string) (float64, bool) {
	for key, raw := range signals {
		if strings.EqualFold(strings.TrimSpace(key), "normalized_quota_remaining_percent") {
			return parseQuotaPercent(raw)
		}
	}
	return 0, false
}

func claudeRemainingPercent(signals map[string]string) (float64, bool) {
	maxUsed := 0.0
	found := false
	for key, raw := range signals {
		lowerKey := strings.ToLower(strings.TrimSpace(key))
		if !strings.HasPrefix(lowerKey, "anthropic-ratelimit-unified-") || !strings.HasSuffix(lowerKey, "-utilization") {
			continue
		}
		value, ok := parseQuotaNumber(raw)
		if !ok || value < 0 {
			continue
		}
		// Anthropic's measured utilization headers use a 0..1 ratio. Accept
		// percentage-shaped values as well for compatible gateways.
		if value <= 1 {
			value *= 100
		}
		if value > 100 {
			continue
		}
		if !found || value > maxUsed {
			maxUsed = value
		}
		found = true
	}
	if !found {
		return 0, false
	}
	return 100 - maxUsed, true
}

func codexRemainingPercent(signals map[string]string) (float64, bool) {
	activeLimit := ""
	for key, value := range signals {
		if strings.EqualFold(strings.TrimSpace(key), "X-Codex-Active-Limit") {
			activeLimit = normalizeCodexLimitIdentifier(value)
			break
		}
	}

	baseUsed, baseFound := maxCodexUsedPercent(signals, func(lowerKey string) bool {
		return lowerKey == "x-codex-primary-used-percent" || lowerKey == "x-codex-secondary-used-percent"
	})
	if activeLimit != "" {
		activeAliases := map[string]struct{}{activeLimit: {}}
		for key, value := range signals {
			lowerKey := strings.ToLower(strings.TrimSpace(key))
			if !strings.HasPrefix(lowerKey, "x-codex-") || !strings.HasSuffix(lowerKey, "-limit-name") {
				continue
			}
			namespace := strings.TrimSuffix(strings.TrimPrefix(lowerKey, "x-codex-"), "-limit-name")
			normalizedNamespace := normalizeCodexLimitIdentifier(namespace)
			normalizedName := normalizeCodexLimitIdentifier(value)
			if normalizedNamespace == activeLimit || normalizedName == activeLimit {
				if normalizedNamespace != "" {
					activeAliases[normalizedNamespace] = struct{}{}
				}
				if normalizedName != "" {
					activeAliases[normalizedName] = struct{}{}
				}
			}
		}
		activeUsed, activeFound := maxCodexUsedPercent(signals, func(lowerKey string) bool {
			if !strings.HasPrefix(lowerKey, "x-codex-") || !strings.HasSuffix(lowerKey, "-used-percent") {
				return false
			}
			namespace := strings.TrimSuffix(strings.TrimPrefix(lowerKey, "x-codex-"), "-used-percent")
			namespace = strings.TrimSuffix(namespace, "-primary")
			namespace = strings.TrimSuffix(namespace, "-secondary")
			_, matches := activeAliases[normalizeCodexLimitIdentifier(namespace)]
			return matches
		})
		if activeFound {
			if !baseFound || activeUsed > baseUsed {
				baseUsed = activeUsed
			}
			baseFound = true
		}
	}
	if !baseFound {
		return 0, false
	}
	return 100 - baseUsed, true
}

func maxCodexUsedPercent(signals map[string]string, include func(string) bool) (float64, bool) {
	maxUsed := 0.0
	found := false
	for key, raw := range signals {
		lowerKey := strings.ToLower(strings.TrimSpace(key))
		if !include(lowerKey) {
			continue
		}
		value, ok := parseQuotaPercent(raw)
		if !ok {
			continue
		}
		if !found || value > maxUsed {
			maxUsed = value
		}
		found = true
	}
	return maxUsed, found
}

func devinRemainingPercent(signals map[string]string) (float64, bool) {
	remaining := 100.0
	found := false
	for key, raw := range signals {
		switch strings.ToLower(strings.TrimSpace(key)) {
		case "daily_quota_remaining_percent", "weekly_quota_remaining_percent":
			value, ok := parseQuotaPercent(raw)
			if !ok {
				continue
			}
			if !found || value < remaining {
				remaining = value
			}
			found = true
		}
	}
	return remaining, found
}

func parseQuotaNumber(raw string) (float64, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, false
	}
	value, errParse := strconv.ParseFloat(raw, 64)
	if errParse != nil || math.IsNaN(value) || math.IsInf(value, 0) {
		return 0, false
	}
	return value, true
}

func parseQuotaPercent(raw string) (float64, bool) {
	raw = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(raw), "%"))
	value, ok := parseQuotaNumber(raw)
	if !ok || value < 0 || value > 100 {
		return 0, false
	}
	return value, true
}

func clampQuotaPercent(value float64) float64 {
	if value < 0 {
		return 0
	}
	if value > 100 {
		return 100
	}
	return value
}

func normalizeQuotaSignalName(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	return strings.NewReplacer("_", "-", ".", "-", " ", "-").Replace(value)
}

func normalizeCodexLimitIdentifier(value string) string {
	value = normalizeQuotaSignalName(value)
	for {
		switch {
		case strings.HasPrefix(value, "additional-"):
			value = strings.TrimPrefix(value, "additional-")
		case strings.HasPrefix(value, "codex-"):
			value = strings.TrimPrefix(value, "codex-")
		default:
			return value
		}
	}
}

// quotaPreferredAuths narrows a same-priority candidate set to credentials with
// the greatest fresh remaining quota. If any candidate has unknown or stale
// quota, the original set is returned so the configured selector remains the
// safe fallback and unknown data is never treated as exhausted.
func quotaPreferredAuths(auths []*Auth, model string, now time.Time) []*Auth {
	if len(auths) <= 1 {
		return auths
	}
	best := -1.0
	bestCount := 0
	observations := make([]quotaRemainingObservation, len(auths))
	for index, auth := range auths {
		observation := authQuotaRemainingPercent(auth, model, now)
		if !observation.known {
			return auths
		}
		observations[index] = observation
		switch {
		case bestCount == 0 || observation.percent > best+quotaPercentEpsilon:
			best = observation.percent
			bestCount = 1
		case math.Abs(observation.percent-best) <= quotaPercentEpsilon:
			bestCount++
		}
	}
	if bestCount == len(auths) {
		return auths
	}
	preferred := make([]*Auth, 0, bestCount)
	for index, auth := range auths {
		if math.Abs(observations[index].percent-best) <= quotaPercentEpsilon {
			preferred = append(preferred, auth)
		}
	}
	return preferred
}

func authQuotaBelowMigrationThreshold(auth *Auth, model string, now time.Time) bool {
	observation := authQuotaRemainingPercent(auth, model, now)
	return observation.known && observation.percent < quotaMigrationThresholdPercent
}

func preferredQuotaMigrationAuths(auths []*Auth, current *Auth, model string, now time.Time) []*Auth {
	if current == nil || len(auths) == 0 {
		return nil
	}
	sameProvider := make([]*Auth, 0, len(auths)-1)
	otherProviders := make([]*Auth, 0, len(auths)-1)
	currentProvider := executorKeyFromAuth(current)
	for _, candidate := range auths {
		if candidate == nil || candidate.ID == current.ID {
			continue
		}
		observation := authQuotaRemainingPercent(candidate, model, now)
		// Proactive migration requires positive evidence that the destination is
		// healthy. Unknown or stale quota remains eligible for ordinary fallback,
		// but must not displace an otherwise usable session binding.
		if !observation.known || observation.percent < quotaMigrationThresholdPercent {
			continue
		}
		if executorKeyFromAuth(candidate) == currentProvider {
			sameProvider = append(sameProvider, candidate)
		} else {
			otherProviders = append(otherProviders, candidate)
		}
	}
	alternatives := sameProvider
	if len(alternatives) == 0 {
		alternatives = otherProviders
	}
	alternatives = highestPriorityAuths(alternatives)
	return quotaPreferredAuths(alternatives, model, now)
}
