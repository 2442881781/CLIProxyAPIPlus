package auth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// ProviderQuotaSourceSnapshot contains a safe normalized quota snapshot for one
// provider-managed credential. It never contains the credential itself.
type ProviderQuotaSourceSnapshot struct {
	ID               string                       `json:"id"`
	Label            string                       `json:"label,omitempty"`
	RemainingPercent float64                      `json:"remaining_percent"`
	ObservedAt       time.Time                    `json:"observed_at,omitempty"`
	LastAttemptAt    time.Time                    `json:"last_attempt_at,omitempty"`
	LastError        string                       `json:"last_error,omitempty"`
	Quota            pluginapi.QuotaFetchResponse `json:"quota"`
}

// ProviderQuotaSourceID returns a stable non-secret identity for one
// provider-managed credential.
func ProviderQuotaSourceID(provider, credential string) string {
	provider = strings.ToLower(strings.TrimSpace(provider))
	digest := sha256.Sum256([]byte(provider + "\x00" + credential))
	return provider + "-" + hex.EncodeToString(digest[:])
}

// ProviderQuotaSnapshot contains provider-managed quota state. RemainingPercent
// represents the best currently usable source in a provider-owned credential pool.
type ProviderQuotaSnapshot struct {
	Provider         string                        `json:"provider"`
	RemainingPercent float64                       `json:"remaining_percent"`
	ObservedAt       time.Time                     `json:"observed_at,omitempty"`
	LastAttemptAt    time.Time                     `json:"last_attempt_at,omitempty"`
	LastError        string                        `json:"last_error,omitempty"`
	Sources          []ProviderQuotaSourceSnapshot `json:"sources,omitempty"`
}

// ProviderQuotaStore persists provider-managed quota independently from auth secrets.
type ProviderQuotaStore interface {
	LoadProviderQuotas(context.Context) ([]ProviderQuotaSnapshot, error)
	SaveProviderQuota(context.Context, ProviderQuotaSnapshot) error
}

// ProviderQuotaStoreProvider exposes a backend-specific provider quota store.
type ProviderQuotaStoreProvider interface {
	ProviderQuotaStore() ProviderQuotaStore
}

// SetProviderQuotaStore swaps the provider quota persistence backend.
func (m *Manager) SetProviderQuotaStore(store ProviderQuotaStore) {
	if m == nil {
		return
	}
	m.providerQuotaMu.Lock()
	m.providerQuotaStore = store
	m.providerQuotaMu.Unlock()
}

// LoadProviderQuotas restores provider-managed quota snapshots from persistence.
func (m *Manager) LoadProviderQuotas(ctx context.Context) error {
	if m == nil {
		return nil
	}
	m.providerQuotaMu.RLock()
	store := m.providerQuotaStore
	m.providerQuotaMu.RUnlock()
	if store == nil {
		return nil
	}
	snapshots, errLoad := store.LoadProviderQuotas(ctx)
	if errLoad != nil {
		return errLoad
	}
	next := make(map[string]ProviderQuotaSnapshot, len(snapshots))
	for i := range snapshots {
		snapshot := normalizeProviderQuotaSnapshot(snapshots[i])
		if snapshot.Provider == "" {
			continue
		}
		next[snapshot.Provider] = snapshot
	}
	m.providerQuotaMu.Lock()
	m.providerQuotas = next
	m.providerQuotaMu.Unlock()
	return nil
}

// ObserveProviderQuota publishes and persists a successful normalized snapshot.
func (m *Manager) ObserveProviderQuota(ctx context.Context, snapshot ProviderQuotaSnapshot) error {
	if m == nil {
		return nil
	}
	snapshot = normalizeProviderQuotaSnapshot(snapshot)
	if snapshot.Provider == "" || snapshot.ObservedAt.IsZero() {
		return nil
	}
	m.providerQuotaMu.Lock()
	current, exists := m.providerQuotas[snapshot.Provider]
	if exists && !current.ObservedAt.IsZero() && snapshot.ObservedAt.Before(current.ObservedAt) {
		m.providerQuotaMu.Unlock()
		return nil
	}
	m.providerQuotas[snapshot.Provider] = cloneProviderQuotaSnapshot(snapshot)
	store := m.providerQuotaStore
	m.providerQuotaMu.Unlock()
	if store != nil {
		return store.SaveProviderQuota(ctx, snapshot)
	}
	return nil
}

// RecordProviderQuotaFailure records a failed refresh while retaining the last
// successful quota data and observation time.
func (m *Manager) RecordProviderQuotaFailure(ctx context.Context, provider string, attemptedAt time.Time, message string) error {
	if m == nil {
		return nil
	}
	provider = strings.ToLower(strings.TrimSpace(provider))
	if provider == "" {
		return nil
	}
	m.providerQuotaMu.Lock()
	snapshot := m.providerQuotas[provider]
	snapshot.Provider = provider
	snapshot.LastAttemptAt = attemptedAt.UTC()
	snapshot.LastError = strings.TrimSpace(message)
	m.providerQuotas[provider] = cloneProviderQuotaSnapshot(snapshot)
	store := m.providerQuotaStore
	m.providerQuotaMu.Unlock()
	if store != nil {
		return store.SaveProviderQuota(ctx, snapshot)
	}
	return nil
}

// ObserveProviderCredentialQuota updates an ordinary auth candidate and the
// corresponding provider snapshot so routing and management share one source.
func (m *Manager) ObserveProviderCredentialQuota(ctx context.Context, authID string, observedAt time.Time, signals map[string]string) (*Auth, error) {
	updated, errObserve := m.ObserveQuota(ctx, authID, observedAt, signals)
	if errObserve != nil || updated == nil {
		return updated, errObserve
	}
	observation := authQuotaRemainingPercent(updated, "", observedAt)
	if !observation.known {
		return updated, nil
	}
	provider := executorKeyFromAuth(updated)
	if provider == "" {
		return updated, nil
	}
	snapshot, _ := m.ProviderQuota(provider)
	snapshot.Provider = provider
	snapshot.LastAttemptAt = observedAt.UTC()
	credential := ""
	if updated.Attributes != nil {
		credential = strings.TrimSpace(updated.Attributes["api_key"])
	}
	if credential != "" {
		sourceID := ProviderQuotaSourceID(provider, credential)
		found := false
		for i := range snapshot.Sources {
			if snapshot.Sources[i].ID != sourceID {
				continue
			}
			snapshot.Sources[i].RemainingPercent = observation.percent
			snapshot.Sources[i].ObservedAt = observedAt.UTC()
			snapshot.Sources[i].LastAttemptAt = observedAt.UTC()
			snapshot.Sources[i].LastError = ""
			found = true
			break
		}
		if !found {
			snapshot.Sources = append(snapshot.Sources, ProviderQuotaSourceSnapshot{
				ID: sourceID, RemainingPercent: observation.percent,
				ObservedAt: observedAt.UTC(), LastAttemptAt: observedAt.UTC(),
			})
		}
	}
	best := observation.percent
	latest := observedAt.UTC()
	for i := range snapshot.Sources {
		if snapshot.Sources[i].ObservedAt.IsZero() || observedAt.Sub(snapshot.Sources[i].ObservedAt) > quotaRoutingObservationTTL {
			continue
		}
		if snapshot.Sources[i].RemainingPercent > best {
			best = snapshot.Sources[i].RemainingPercent
		}
		if snapshot.Sources[i].ObservedAt.After(latest) {
			latest = snapshot.Sources[i].ObservedAt
		}
	}
	snapshot.RemainingPercent = best
	snapshot.ObservedAt = latest
	snapshot.LastError = ""
	return updated, m.ObserveProviderQuota(ctx, snapshot)
}

// ProviderQuota returns one provider-managed quota snapshot.
func (m *Manager) ProviderQuota(provider string) (ProviderQuotaSnapshot, bool) {
	if m == nil {
		return ProviderQuotaSnapshot{}, false
	}
	provider = strings.ToLower(strings.TrimSpace(provider))
	m.providerQuotaMu.RLock()
	snapshot, ok := m.providerQuotas[provider]
	m.providerQuotaMu.RUnlock()
	if !ok {
		return ProviderQuotaSnapshot{}, false
	}
	return cloneProviderQuotaSnapshot(snapshot), true
}

// ProviderQuotas returns all provider-managed quota snapshots.
func (m *Manager) ProviderQuotas() []ProviderQuotaSnapshot {
	if m == nil {
		return nil
	}
	m.providerQuotaMu.RLock()
	out := make([]ProviderQuotaSnapshot, 0, len(m.providerQuotas))
	for _, snapshot := range m.providerQuotas {
		out = append(out, cloneProviderQuotaSnapshot(snapshot))
	}
	m.providerQuotaMu.RUnlock()
	return out
}

func (m *Manager) applyProviderQuotaFallback(auths []*Auth, model string, now time.Time) {
	if m == nil || len(auths) == 0 {
		return
	}
	m.providerQuotaMu.RLock()
	defer m.providerQuotaMu.RUnlock()
	for _, candidate := range auths {
		if candidate == nil || authQuotaRemainingPercent(candidate, model, now).known {
			continue
		}
		provider := executorKeyFromAuth(candidate)
		snapshot, ok := m.providerQuotas[provider]
		if !ok || snapshot.ObservedAt.IsZero() {
			continue
		}
		remainingPercent := snapshot.RemainingPercent
		observedAt := snapshot.ObservedAt
		sourceMatched := false
		if candidate.Attributes != nil {
			if credential := strings.TrimSpace(candidate.Attributes["api_key"]); credential != "" {
				sourceID := ProviderQuotaSourceID(provider, credential)
				for i := range snapshot.Sources {
					if snapshot.Sources[i].ID == sourceID && !snapshot.Sources[i].ObservedAt.IsZero() {
						remainingPercent = snapshot.Sources[i].RemainingPercent
						observedAt = snapshot.Sources[i].ObservedAt
						sourceMatched = true
						break
					}
				}
				if !sourceMatched && len(snapshot.Sources) > 0 {
					continue
				}
			}
		}
		candidate.Quota.ObservedAt = observedAt
		candidate.Quota.Signals = map[string]string{
			"normalized_quota_remaining_percent": formatNormalizedQuotaPercent(remainingPercent),
		}
	}
}

func formatNormalizedQuotaPercent(value float64) string {
	return strings.TrimRight(strings.TrimRight(fmtFloat(value), "0"), ".")
}

func fmtFloat(value float64) string {
	return strconv.FormatFloat(clampQuotaPercent(value), 'f', 6, 64)
}

func normalizeProviderQuotaSnapshot(snapshot ProviderQuotaSnapshot) ProviderQuotaSnapshot {
	snapshot.Provider = strings.ToLower(strings.TrimSpace(snapshot.Provider))
	snapshot.RemainingPercent = clampQuotaPercent(snapshot.RemainingPercent)
	snapshot.ObservedAt = snapshot.ObservedAt.UTC()
	snapshot.LastAttemptAt = snapshot.LastAttemptAt.UTC()
	snapshot.LastError = strings.TrimSpace(snapshot.LastError)
	for i := range snapshot.Sources {
		snapshot.Sources[i].ID = strings.TrimSpace(snapshot.Sources[i].ID)
		snapshot.Sources[i].Label = strings.TrimSpace(snapshot.Sources[i].Label)
		snapshot.Sources[i].RemainingPercent = clampQuotaPercent(snapshot.Sources[i].RemainingPercent)
		snapshot.Sources[i].ObservedAt = snapshot.Sources[i].ObservedAt.UTC()
		snapshot.Sources[i].LastAttemptAt = snapshot.Sources[i].LastAttemptAt.UTC()
		snapshot.Sources[i].LastError = strings.TrimSpace(snapshot.Sources[i].LastError)
	}
	return snapshot
}

func cloneProviderQuotaSnapshot(snapshot ProviderQuotaSnapshot) ProviderQuotaSnapshot {
	copySnapshot := snapshot
	if len(snapshot.Sources) > 0 {
		copySnapshot.Sources = make([]ProviderQuotaSourceSnapshot, len(snapshot.Sources))
		for i := range snapshot.Sources {
			copySnapshot.Sources[i] = snapshot.Sources[i]
			copySnapshot.Sources[i].Quota = cloneQuotaFetchResponse(snapshot.Sources[i].Quota)
		}
	}
	return copySnapshot
}

func cloneQuotaFetchResponse(in pluginapi.QuotaFetchResponse) pluginapi.QuotaFetchResponse {
	out := in
	if in.Subscription != nil {
		subscription := *in.Subscription
		out.Subscription = &subscription
	}
	if len(in.Groups) > 0 {
		out.Groups = make([]pluginapi.QuotaGroup, len(in.Groups))
		for i := range in.Groups {
			out.Groups[i] = in.Groups[i]
			out.Groups[i].Buckets = append([]pluginapi.QuotaBucket(nil), in.Groups[i].Buckets...)
		}
	}
	return out
}
