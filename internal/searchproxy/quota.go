package searchproxy

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/tidwall/gjson"
)

// Quota units.
const (
	QuotaUnitCredits = "credits"
	QuotaUnitUSD     = "usd"
)

// DefaultQuotaRefreshInterval is how often the background refresher polls remote quota endpoints.
const DefaultQuotaRefreshInterval = 30 * time.Minute

// quotaEndpoints are the per-provider GET endpoints that report quota without consuming credits.
var quotaEndpoints = map[string]string{
	config.SearchProviderTavily:    "/usage",
	config.SearchProviderFirecrawl: "/v2/team/credit-usage",
}

// Quota is the known balance of one key. Nil numeric fields mean unknown.
type Quota struct {
	Unit      string    `json:"unit,omitempty"`
	Used      *float64  `json:"used,omitempty"`
	Limit     *float64  `json:"limit,omitempty"`
	Remaining *float64  `json:"remaining,omitempty"`
	Plan      string    `json:"plan,omitempty"`
	ResetAt   time.Time `json:"reset-at,omitempty"`
	CheckedAt time.Time `json:"checked-at,omitempty"`
	Error     string    `json:"error,omitempty"`
}

func floatPtr(v float64) *float64 { return &v }

func (q *Quota) clone() *Quota {
	if q == nil {
		return nil
	}
	c := *q
	for _, field := range []**float64{&c.Used, &c.Limit, &c.Remaining} {
		if *field != nil {
			*field = floatPtr(**field)
		}
	}
	return &c
}

// isQuotaStatus reports statuses that mean "out of credits" rather than rate limiting or bad keys.
func isQuotaStatus(status int) bool {
	return status == http.StatusPaymentRequired || status == 432 || status == 433
}

// quotaTargets returns keys whose provider has a remote quota endpoint, optionally filtered by id.
func (p *Pool) quotaTargets(ids []string) []Key {
	p.mu.Lock()
	defer p.mu.Unlock()
	wanted := map[string]bool{}
	for _, id := range ids {
		wanted[id] = true
	}
	var keys []Key
	for _, provider := range config.SearchProviders {
		if _, ok := quotaEndpoints[provider]; !ok {
			continue
		}
		pp := p.providers[provider]
		if pp == nil {
			continue
		}
		for _, state := range pp.keys {
			if len(wanted) == 0 || wanted[state.id] {
				keys = append(keys, state.key())
			}
		}
	}
	return keys
}

// beginRefresh marks a key as refreshing; it returns false when a refresh is already in flight.
func (p *Pool) beginRefresh(id string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	state := p.byID[id]
	if state == nil || state.refreshing {
		return false
	}
	state.refreshing = true
	return true
}

// applyQuota stores a refresh result. A failed refresh keeps previous numbers and routing state.
// A successful one marks the key exhausted at zero remaining, or clears quota-caused cooldowns.
func (p *Pool) applyQuota(id string, quota *Quota, errFetch error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	state := p.byID[id]
	if state == nil {
		return
	}
	state.refreshing = false
	now := p.now()
	if errFetch != nil {
		next := state.quota.clone()
		if next == nil {
			next = &Quota{}
		}
		next.Error = errFetch.Error()
		next.CheckedAt = now
		state.quota = next
		return
	}
	quota.CheckedAt = now
	state.quota = quota
	state.quotaExhausted = quota.Remaining != nil && *quota.Remaining <= 0
	if !state.quotaExhausted && isQuotaStatus(state.cooldownStatus) {
		state.cooldownUntil = time.Time{}
		state.cooldownStatus = 0
	}
}

func (s *keyState) exhausted(now time.Time) bool {
	if s.quotaExhausted {
		return true
	}
	return s.entry.Budget > 0 && s.spentIn(now) >= s.entry.Budget
}

func (s *keyState) quotaView(now time.Time) *Quota {
	if s.entry.Provider != config.SearchProviderExa {
		return s.quota.clone()
	}
	spent := s.spentIn(now)
	q := &Quota{Unit: QuotaUnitUSD, Used: floatPtr(spent), ResetAt: nextMonth(now)}
	if s.entry.Budget > 0 {
		remaining := s.entry.Budget - spent
		if remaining < 0 {
			remaining = 0
		}
		q.Limit = floatPtr(s.entry.Budget)
		q.Remaining = floatPtr(remaining)
	}
	return q
}

// RefreshQuotas queries remote quota for the given key ids (all remote-quota keys when empty)
// and waits for the results. Keys with a refresh already in flight are skipped.
func (s *Service) RefreshQuotas(ctx context.Context, ids []string) {
	s.launchQuotaRefreshes(ctx, ids).Wait()
}

// StartQuotaRefresher refreshes quota immediately, then every interval and right after each
// UTC month rollover (when Tavily credits reset), until ctx ends.
// The returned channel is closed once the loop has stopped.
func (s *Service) StartQuotaRefresher(ctx context.Context, interval time.Duration) <-chan struct{} {
	done := make(chan struct{})
	ticks, stopTicks := s.newTicker(interval)
	go func() {
		defer close(done)
		defer stopTicks()
		s.launchQuotaRefreshes(ctx, nil)
		now := s.now()
		monthly, stopMonthly := s.newTimer(nextMonth(now).Sub(now))
		defer func() { stopMonthly() }()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticks:
				s.launchQuotaRefreshes(ctx, nil)
			case <-monthly:
				s.launchQuotaRefreshes(ctx, nil)
				stopMonthly()
				now = s.now()
				monthly, stopMonthly = s.newTimer(nextMonth(now).Sub(now))
			}
		}
	}()
	return done
}

// launchQuotaRefreshes starts one goroutine per key so a slow upstream never blocks the others.
// No request timeout is applied; overlapping refreshes of the same key are skipped instead.
func (s *Service) launchQuotaRefreshes(ctx context.Context, ids []string) *sync.WaitGroup {
	var wg sync.WaitGroup
	for _, key := range s.pool.quotaTargets(ids) {
		if !s.pool.beginRefresh(key.ID) {
			continue
		}
		wg.Add(1)
		go func(key Key) {
			defer wg.Done()
			quota, err := s.fetchQuota(ctx, key)
			s.pool.applyQuota(key.ID, quota, err)
		}(key)
	}
	return &wg
}

func defaultTicker(d time.Duration) (<-chan time.Time, func()) {
	t := time.NewTicker(d)
	return t.C, t.Stop
}

func defaultTimer(d time.Duration) (<-chan time.Time, func()) {
	t := time.NewTimer(d)
	return t.C, func() { t.Stop() }
}

func (s *Service) fetchQuota(ctx context.Context, key Key) (*Quota, error) {
	base := key.BaseURL
	if base == "" {
		base = defaultBaseURLs[key.Provider]
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+quotaEndpoints[key.Provider], nil)
	if err != nil {
		return nil, fmt.Errorf("build quota request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+key.APIKey)
	req.Header.Set("Accept", "application/json")
	client, err := s.httpClient(key.ProxyURL)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("quota request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read quota response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("quota endpoint returned HTTP %d: %s", resp.StatusCode, truncate(strings.TrimSpace(string(body)), 200))
	}
	if key.Provider == config.SearchProviderTavily {
		return parseTavilyUsage(body, s.now())
	}
	return parseFirecrawlCreditUsage(body)
}

// parseTavilyUsage prefers the key's own limit and caps it by the account balance;
// without a key limit the account plan plus pay-as-you-go allowance applies.
// Tavily does not report a reset time; its credits reset on the first day of each month.
func parseTavilyUsage(body []byte, now time.Time) (*Quota, error) {
	if !gjson.ValidBytes(body) || !gjson.GetBytes(body, "key").Exists() {
		return nil, fmt.Errorf("unexpected tavily usage response")
	}
	num := func(path string) float64 { return gjson.GetBytes(body, path).Float() }
	hasAccount := gjson.GetBytes(body, "account.plan_limit").Exists()
	accountLimit := num("account.plan_limit") + num("account.paygo_limit")
	accountRemaining := nonNegative(num("account.plan_limit")-num("account.plan_usage")) +
		nonNegative(num("account.paygo_limit")-num("account.paygo_usage"))

	q := &Quota{Unit: QuotaUnitCredits, Plan: gjson.GetBytes(body, "account.current_plan").String(), ResetAt: nextMonth(now)}
	if keyLimit := gjson.GetBytes(body, "key.limit"); keyLimit.Type == gjson.Number {
		remaining := nonNegative(keyLimit.Float() - num("key.usage"))
		if hasAccount && accountRemaining < remaining {
			remaining = accountRemaining
		}
		q.Used, q.Limit, q.Remaining = floatPtr(num("key.usage")), floatPtr(keyLimit.Float()), floatPtr(remaining)
		return q, nil
	}
	if !hasAccount {
		q.Used = floatPtr(num("key.usage"))
		return q, nil
	}
	q.Used = floatPtr(num("account.plan_usage") + num("account.paygo_usage"))
	q.Limit, q.Remaining = floatPtr(accountLimit), floatPtr(accountRemaining)
	return q, nil
}

func parseFirecrawlCreditUsage(body []byte) (*Quota, error) {
	remaining := gjson.GetBytes(body, "data.remainingCredits")
	if remaining.Type != gjson.Number {
		return nil, fmt.Errorf("unexpected firecrawl credit usage response")
	}
	q := &Quota{Unit: QuotaUnitCredits, Remaining: floatPtr(remaining.Float())}
	if plan := gjson.GetBytes(body, "data.planCredits"); plan.Type == gjson.Number {
		q.Limit = floatPtr(plan.Float())
		if plan.Float() >= remaining.Float() {
			q.Used = floatPtr(plan.Float() - remaining.Float())
		}
	}
	if end, err := time.Parse(time.RFC3339, gjson.GetBytes(body, "data.billingPeriodEnd").String()); err == nil {
		q.ResetAt = end
	}
	return q, nil
}

func nonNegative(v float64) float64 {
	if v < 0 {
		return 0
	}
	return v
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
