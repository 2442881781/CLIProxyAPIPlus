package cliproxy

import (
	"context"
	"fmt"
	"math/rand"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/providerquota"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

const (
	providerQuotaRefreshInterval = 5 * time.Minute
	providerQuotaRefreshJitter   = 30 * time.Second
	providerQuotaRequestTimeout  = 20 * time.Second
)

func (s *Service) startProviderQuotaRefresh(ctx context.Context) {
	if s == nil || s.coreManager == nil {
		return
	}
	s.providerQuotaRefreshMu.Lock()
	if s.providerQuotaRefreshCancel != nil {
		s.providerQuotaRefreshMu.Unlock()
		return
	}
	refreshCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	s.providerQuotaRefreshCtx = refreshCtx
	s.providerQuotaRefreshCancel = cancel
	s.providerQuotaRefreshDone = done
	s.providerQuotaRefreshMu.Unlock()

	s.refreshProviderQuotas(refreshCtx, "startup")
	go func() {
		defer close(done)
		timer := time.NewTimer(jitterProviderQuotaInterval())
		defer timer.Stop()
		for {
			select {
			case <-refreshCtx.Done():
				return
			case <-timer.C:
				s.refreshProviderQuotas(refreshCtx, "periodic")
				timer.Reset(jitterProviderQuotaInterval())
			}
		}
	}()
}

func (s *Service) stopProviderQuotaRefresh() {
	if s == nil {
		return
	}
	s.providerQuotaRefreshMu.Lock()
	cancel := s.providerQuotaRefreshCancel
	done := s.providerQuotaRefreshDone
	s.providerQuotaRefreshCtx = nil
	s.providerQuotaRefreshCancel = nil
	s.providerQuotaRefreshDone = nil
	s.providerQuotaRefreshMu.Unlock()
	if cancel != nil {
		cancel()
	}
	if done != nil {
		<-done
	}
}

func (s *Service) triggerProviderQuotaRefresh() {
	if s == nil || s.coreManager == nil {
		return
	}
	s.providerQuotaRefreshMu.Lock()
	ctx := s.providerQuotaRefreshCtx
	s.providerQuotaRefreshMu.Unlock()
	if ctx != nil {
		go s.refreshProviderQuotas(ctx, "config-update")
	}
}

func (s *Service) refreshProviderQuotas(ctx context.Context, reason string) {
	if s == nil || s.coreManager == nil || ctx == nil || ctx.Err() != nil {
		return
	}
	if !s.providerQuotaRefreshRunning.CompareAndSwap(false, true) {
		return
	}
	defer s.providerQuotaRefreshRunning.Store(false)

	s.cfgMu.RLock()
	cfg := s.cfg.CloneForRuntime()
	s.cfgMu.RUnlock()
	if s.pluginHost != nil && cfg != nil && cfg.Routing.QuotaAware {
		failures := s.coreManager.RefreshCredentialProviderQuotas(ctx, s.pluginHost, providerquota.RemainingPercent, coreauth.ProviderQuotaRefreshOptions{
			Timeout: providerQuotaRequestTimeout, MaxConcurrent: 4,
		})
		for authID, errRefresh := range failures {
			log.WithFields(log.Fields{"auth_id": authID, "reason": reason}).WithError(errRefresh).Warn("credential quota refresh failed")
		}
	}
	if cfg == nil || !cfg.Routing.QuotaAware {
		return
	}
	for authID, errRefresh := range s.coreManager.RefreshProviderCredentialQuotas(ctx, []string{"devin"}, providerQuotaRequestTimeout, 4) {
		log.WithFields(log.Fields{"auth_id": authID, "reason": reason}).WithError(errRefresh).Warn("executor quota refresh failed")
	}
	for _, provider := range providerquota.SupportedProviders(cfg) {
		if ctx.Err() != nil {
			return
		}
		attemptedAt := time.Now().UTC()
		requestCtx, cancel := context.WithTimeout(ctx, providerQuotaRequestTimeout)
		snapshot, errCollect := providerquota.CollectProvider(requestCtx, cfg, provider)
		cancel()
		if errCollect != nil {
			message := strings.TrimSpace(errCollect.Error())
			if errPersist := s.coreManager.RecordProviderQuotaFailure(context.Background(), provider, attemptedAt, message); errPersist != nil {
				log.WithFields(log.Fields{"provider": provider, "reason": reason}).WithError(errPersist).Warn("failed to persist provider quota refresh failure")
			}
			log.WithFields(log.Fields{"provider": provider, "reason": reason}).WithError(errCollect).Warn("provider quota refresh failed")
			continue
		}
		snapshot = s.mergeProviderQuotaSnapshot(snapshot)
		if errObserve := s.coreManager.ObserveProviderQuota(context.Background(), snapshot); errObserve != nil {
			log.WithFields(log.Fields{"provider": provider, "reason": reason}).WithError(errObserve).Warn("failed to persist provider quota snapshot")
		}
	}
}

// RefreshProviderQuotas performs an immediate server-side refresh. It is used
// by the management API so manual refreshes share the lifecycle collector and
// persistence path.
func (s *Service) RefreshProviderQuotas(ctx context.Context, provider string) error {
	if s == nil || s.coreManager == nil {
		return fmt.Errorf("provider quota manager unavailable")
	}
	s.cfgMu.RLock()
	cfg := s.cfg.CloneForRuntime()
	s.cfgMu.RUnlock()
	provider = strings.ToLower(strings.TrimSpace(provider))
	requestCtx, cancel := context.WithTimeout(ctx, providerQuotaRequestTimeout)
	defer cancel()
	snapshot, errCollect := providerquota.CollectProvider(requestCtx, cfg, provider)
	if errCollect != nil {
		attemptedAt := time.Now().UTC()
		if errPersist := s.coreManager.RecordProviderQuotaFailure(context.Background(), provider, attemptedAt, errCollect.Error()); errPersist != nil {
			log.WithField("provider", provider).WithError(errPersist).Warn("failed to persist provider quota refresh failure")
		}
		return errCollect
	}
	snapshot = s.mergeProviderQuotaSnapshot(snapshot)
	return s.coreManager.ObserveProviderQuota(context.Background(), snapshot)
}

func (s *Service) mergeProviderQuotaSnapshot(snapshot coreauth.ProviderQuotaSnapshot) coreauth.ProviderQuotaSnapshot {
	if s == nil || s.coreManager == nil {
		return snapshot
	}
	existing, ok := s.coreManager.ProviderQuota(snapshot.Provider)
	if !ok || len(existing.Sources) == 0 {
		return snapshot
	}
	seen := make(map[string]struct{}, len(snapshot.Sources))
	for i := range snapshot.Sources {
		seen[snapshot.Sources[i].ID] = struct{}{}
	}
	for i := range existing.Sources {
		if _, found := seen[existing.Sources[i].ID]; found {
			continue
		}
		snapshot.Sources = append(snapshot.Sources, existing.Sources[i])
	}
	best := snapshot.RemainingPercent
	latest := snapshot.ObservedAt
	now := time.Now().UTC()
	for i := range snapshot.Sources {
		if snapshot.Sources[i].ObservedAt.IsZero() || now.Sub(snapshot.Sources[i].ObservedAt) > 15*time.Minute {
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
	return snapshot
}

func jitterProviderQuotaInterval() time.Duration {
	if providerQuotaRefreshJitter <= 0 {
		return providerQuotaRefreshInterval
	}
	span := int64(providerQuotaRefreshJitter)*2 + 1
	return providerQuotaRefreshInterval - providerQuotaRefreshJitter + time.Duration(rand.Int63n(span))
}
