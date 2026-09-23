// Package searchproxy pools upstream web search provider keys (Tavily, Exa, Firecrawl)
// and exposes them through a REST reverse proxy and an MCP server.
package searchproxy

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

// ErrProviderNotConfigured is returned when a provider has no configured keys.
var ErrProviderNotConfigured = errors.New("search provider not configured")

// NoAvailableKeyError is returned when every key of a provider is disabled, cooling down, or already tried.
type NoAvailableKeyError struct {
	Provider string
	// RetryAt is the earliest time a cooling key recovers; zero when none will recover on its own.
	RetryAt time.Time
}

func (e *NoAvailableKeyError) Error() string {
	return fmt.Sprintf("no available %s key", e.Provider)
}

// Key is an immutable view of one pooled upstream key.
type Key struct {
	// ID is a stable identifier derived from provider and key hash; it never contains key material.
	ID       string
	Provider string
	APIKey   string
	Label    string
	BaseURL  string
	ProxyURL string
}

// KeyStatus is the runtime state of one key, safe to expose in management views.
type KeyStatus struct {
	ID            string    `json:"id"`
	Provider      string    `json:"provider"`
	Label         string    `json:"label,omitempty"`
	MaskedKey     string    `json:"masked-key"`
	Disabled      bool      `json:"disabled"`
	CooldownUntil time.Time `json:"cooldown-until,omitempty"`
	LastStatus    int       `json:"last-status,omitempty"`
	LastUsed      time.Time `json:"last-used,omitempty"`
	Requests      int64     `json:"requests"`
	Failures      int64     `json:"failures"`
}

type keyState struct {
	entry         config.SearchKey
	id            string
	cooldownUntil time.Time
	lastStatus    int
	lastUsed      time.Time
	requests      int64
	failures      int64
}

type providerPool struct {
	keys   []*keyState
	cursor int
}

// Pool holds per-provider key rotation state. It is safe for concurrent use.
type Pool struct {
	mu        sync.Mutex
	now       func() time.Time
	providers map[string]*providerPool
	byID      map[string]*keyState
}

// NewPool creates an empty pool. now defaults to time.Now when nil.
func NewPool(now func() time.Time) *Pool {
	if now == nil {
		now = time.Now
	}
	return &Pool{now: now, providers: map[string]*providerPool{}, byID: map[string]*keyState{}}
}

// KeyID returns the stable identifier for a provider key.
func KeyID(provider, apiKey string) string {
	sum := sha256.Sum256([]byte(apiKey))
	return provider + ":" + hex.EncodeToString(sum[:])[:12]
}

// Update replaces the configured keys. Runtime state of keys that remain is preserved.
func (p *Pool) Update(entries []config.SearchKey) {
	p.mu.Lock()
	defer p.mu.Unlock()

	providers := make(map[string]*providerPool)
	byID := make(map[string]*keyState, len(entries))
	for _, entry := range entries {
		normalized, ok := config.NormalizeSearchKey(entry)
		if !ok {
			continue
		}
		id := KeyID(normalized.Provider, normalized.APIKey)
		if _, dup := byID[id]; dup {
			continue
		}
		state := p.byID[id]
		if state == nil {
			state = &keyState{id: id}
		}
		state.entry = normalized
		byID[id] = state
		pp := providers[normalized.Provider]
		if pp == nil {
			pp = &providerPool{}
			if old := p.providers[normalized.Provider]; old != nil {
				pp.cursor = old.cursor
			}
			providers[normalized.Provider] = pp
		}
		pp.keys = append(pp.keys, state)
	}
	p.providers = providers
	p.byID = byID
}

// HasProvider reports whether at least one key (enabled or not) is configured for provider.
func (p *Pool) HasProvider(provider string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	pp := p.providers[provider]
	return pp != nil && len(pp.keys) > 0
}

// Pick returns the next eligible key of provider in round-robin order, skipping disabled,
// cooling down, and excluded (already tried) keys.
func (p *Pool) Pick(provider string, exclude map[string]bool) (Key, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	pp := p.providers[provider]
	if pp == nil || len(pp.keys) == 0 {
		return Key{}, ErrProviderNotConfigured
	}
	now := p.now()
	var retryAt time.Time
	n := len(pp.keys)
	for i := 0; i < n; i++ {
		idx := (pp.cursor + i) % n
		state := pp.keys[idx]
		if state.entry.Disabled || exclude[state.id] {
			continue
		}
		if state.cooldownUntil.After(now) {
			if retryAt.IsZero() || state.cooldownUntil.Before(retryAt) {
				retryAt = state.cooldownUntil
			}
			continue
		}
		pp.cursor = (idx + 1) % n
		return state.key(), nil
	}
	return Key{}, &NoAvailableKeyError{Provider: provider, RetryAt: retryAt}
}

// Lookup returns the key with the given ID when it is still configured.
func (p *Pool) Lookup(id string) (Key, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	state := p.byID[id]
	if state == nil {
		return Key{}, false
	}
	return state.key(), true
}

// Report records the outcome of one upstream call made with key id.
func (p *Pool) Report(id string, status int, failed bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	state := p.byID[id]
	if state == nil {
		return
	}
	state.requests++
	if failed {
		state.failures++
	}
	state.lastStatus = status
	state.lastUsed = p.now()
}

// Cooldown removes key id from rotation for d.
func (p *Pool) Cooldown(id string, d time.Duration, status int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	state := p.byID[id]
	if state == nil || d <= 0 {
		return
	}
	state.cooldownUntil = p.now().Add(d)
	state.lastStatus = status
}

// ResetCooldown clears the cooldown of the matching key and returns how many keys were reset.
func (p *Pool) ResetCooldown(provider, apiKey string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	state := p.byID[KeyID(provider, apiKey)]
	if state == nil || state.cooldownUntil.IsZero() {
		return 0
	}
	state.cooldownUntil = time.Time{}
	return 1
}

// ResetAllCooldowns clears every active cooldown and returns how many keys were reset.
func (p *Pool) ResetAllCooldowns() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := p.now()
	count := 0
	for _, state := range p.byID {
		if state.cooldownUntil.After(now) {
			count++
		}
		state.cooldownUntil = time.Time{}
	}
	return count
}

// Snapshot returns the status of every configured key in config order.
func (p *Pool) Snapshot() []KeyStatus {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := p.now()
	out := make([]KeyStatus, 0, len(p.byID))
	for _, provider := range config.SearchProviders {
		pp := p.providers[provider]
		if pp == nil {
			continue
		}
		for _, state := range pp.keys {
			st := KeyStatus{
				ID:         state.id,
				Provider:   state.entry.Provider,
				Label:      state.entry.Label,
				MaskedKey:  maskKey(state.entry.APIKey),
				Disabled:   state.entry.Disabled,
				LastStatus: state.lastStatus,
				LastUsed:   state.lastUsed,
				Requests:   state.requests,
				Failures:   state.failures,
			}
			if state.cooldownUntil.After(now) {
				st.CooldownUntil = state.cooldownUntil
			}
			out = append(out, st)
		}
	}
	return out
}

func (s *keyState) key() Key {
	return Key{
		ID:       s.id,
		Provider: s.entry.Provider,
		APIKey:   s.entry.APIKey,
		Label:    s.entry.Label,
		BaseURL:  s.entry.BaseURL,
		ProxyURL: s.entry.ProxyURL,
	}
}

func maskKey(key string) string {
	if len(key) <= 8 {
		return "****"
	}
	return key[:4] + "****" + key[len(key)-4:]
}
