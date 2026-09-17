package auth

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// RefreshProviderCredentialQuotas asks registered executors to refresh
// credential metadata and quota signals without waiting for token expiry.
func (m *Manager) RefreshProviderCredentialQuotas(ctx context.Context, providers []string, timeout time.Duration, maxConcurrent int) map[string]error {
	failures := make(map[string]error)
	if m == nil || len(providers) == 0 {
		return failures
	}
	providerSet := make(map[string]struct{}, len(providers))
	for _, provider := range providers {
		if normalized := strings.ToLower(strings.TrimSpace(provider)); normalized != "" {
			providerSet[normalized] = struct{}{}
		}
	}
	if maxConcurrent <= 0 {
		maxConcurrent = 4
	}
	sem := make(chan struct{}, maxConcurrent)
	var failuresMu sync.Mutex
	var wg sync.WaitGroup
	for _, auth := range m.List() {
		if auth == nil || auth.Disabled || auth.Status == StatusDisabled {
			continue
		}
		provider := executorKeyFromAuth(auth)
		if IsPluginStaticAuth(auth) {
			continue
		}
		if _, allowed := providerSet[provider]; !allowed {
			continue
		}
		m.mu.RLock()
		executor := m.executors[provider]
		m.mu.RUnlock()
		if executor == nil {
			continue
		}
		auth := auth.Clone()
		sem <- struct{}{}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			requestCtx := ctx
			cancel := func() {}
			if timeout > 0 {
				requestCtx, cancel = context.WithTimeout(ctx, timeout)
			}
			defer cancel()
			updated, errRefresh := executor.Refresh(requestCtx, auth.Clone())
			if errRefresh != nil {
				failuresMu.Lock()
				failures[auth.ID] = errRefresh
				failuresMu.Unlock()
				return
			}
			if updated == nil || updated.Quota.ObservedAt.IsZero() || len(updated.Quota.Signals) == 0 {
				return
			}
			observation := authQuotaRemainingPercent(updated, "", updated.Quota.ObservedAt)
			if !observation.known {
				return
			}
			_, errObserve := m.ObserveProviderCredentialQuota(context.Background(), auth.ID, updated.Quota.ObservedAt, updated.Quota.Signals)
			if errObserve != nil {
				failuresMu.Lock()
				failures[auth.ID] = errObserve
				failuresMu.Unlock()
			}
		}()
	}
	wg.Wait()
	return failures
}

// ProviderQuotaFetcher obtains normalized quota for one ordinary auth record.
type ProviderQuotaFetcher interface {
	FetchCredentialQuota(context.Context, *Auth) (pluginapi.QuotaFetchResponse, bool, error)
}

type providerQuotaRefreshScheduler struct {
	manager    *Manager
	fetcher    ProviderQuotaFetcher
	normalize  func(pluginapi.QuotaFetchResponse) (float64, bool)
	timeout    time.Duration
	sem        chan struct{}
	failuresMu sync.Mutex
	failures   map[string]error
	wg         sync.WaitGroup
}

type ProviderQuotaRefreshOptions struct {
	Timeout       time.Duration
	MaxConcurrent int
}

// RefreshCredentialProviderQuotas refreshes all auth records handled by the
// supplied fetcher. Successful observations update both Auth.Quota and the
// provider snapshot. Failures leave the last successful observation intact.
func (m *Manager) RefreshCredentialProviderQuotas(ctx context.Context, fetcher ProviderQuotaFetcher, normalize func(pluginapi.QuotaFetchResponse) (float64, bool), options ProviderQuotaRefreshOptions) map[string]error {
	failures := make(map[string]error)
	if m == nil || fetcher == nil || normalize == nil {
		return failures
	}
	if options.MaxConcurrent <= 0 {
		options.MaxConcurrent = 4
	}
	scheduler := &providerQuotaRefreshScheduler{
		manager: m, fetcher: fetcher, normalize: normalize, timeout: options.Timeout,
		sem: make(chan struct{}, options.MaxConcurrent), failures: failures,
	}
	for _, auth := range m.List() {
		if auth == nil || auth.Disabled || auth.Status == StatusDisabled {
			continue
		}
		if IsPluginStaticAuth(auth) {
			continue
		}
		if errContext := ctx.Err(); errContext != nil {
			scheduler.recordFailure(auth.ID, errContext)
			break
		}
		scheduler.schedule(ctx, auth)
	}
	scheduler.wg.Wait()
	return failures
}

func (s *providerQuotaRefreshScheduler) schedule(ctx context.Context, auth *Auth) {
	auth = auth.Clone()
	s.sem <- struct{}{}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer func() { <-s.sem }()
		requestCtx := ctx
		cancel := func() {}
		if s.timeout > 0 {
			requestCtx, cancel = context.WithTimeout(ctx, s.timeout)
		}
		defer cancel()
		quota, handled, errFetch := s.fetcher.FetchCredentialQuota(requestCtx, auth)
		if !handled {
			return
		}
		if errFetch != nil {
			s.recordFailure(auth.ID, errFetch)
			return
		}
		remainingPercent, ok := s.normalize(quota)
		if !ok {
			return
		}
		observedAt := time.Now().UTC()
		_, errObserve := s.manager.ObserveProviderCredentialQuota(context.Background(), auth.ID, observedAt, map[string]string{
			"normalized_quota_remaining_percent": formatNormalizedQuotaPercent(remainingPercent),
		})
		if errObserve != nil {
			s.recordFailure(auth.ID, errObserve)
		}
	}()
}

func (s *providerQuotaRefreshScheduler) recordFailure(authID string, err error) {
	s.failuresMu.Lock()
	s.failures[authID] = err
	s.failuresMu.Unlock()
}
