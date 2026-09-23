package searchproxy

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

// Feature: Upstream key pool per search provider
//   Keys come from config `search-api-key` entries grouped by provider
//   (tavily / exa / firecrawl). Time is driven by an injectable clock.

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Set(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = t
}

func (c *fakeClock) Advance(d time.Duration) { c.Set(c.Now().Add(d)) }

func newTestPool(t *testing.T, keys ...config.SearchKey) (*Pool, *fakeClock) {
	t.Helper()
	clock := &fakeClock{t: time.Date(2026, 9, 23, 0, 0, 0, 0, time.UTC)}
	pool := NewPool(clock.Now)
	pool.Update(keys)
	return pool, clock
}

func mustPick(t *testing.T, pool *Pool, provider string, exclude map[string]bool) Key {
	t.Helper()
	key, err := pool.Pick(provider, exclude)
	if err != nil {
		t.Fatalf("Pick(%s): %v", provider, err)
	}
	return key
}

// Scenario: Round-robin across healthy keys
//
//	Given the tavily pool has keys A, B, C, all healthy
//	When four picks are made
//	Then the picked keys are A, B, C, A
func TestPoolPickRoundRobinAcrossHealthyKeys(t *testing.T) {
	pool, _ := newTestPool(t,
		config.SearchKey{Provider: "tavily", APIKey: "A"},
		config.SearchKey{Provider: "tavily", APIKey: "B"},
		config.SearchKey{Provider: "tavily", APIKey: "C"},
	)

	var got []string
	for i := 0; i < 4; i++ {
		got = append(got, mustPick(t, pool, "tavily", nil).APIKey)
	}
	if strings.Join(got, ",") != "A,B,C,A" {
		t.Fatalf("picks = %v, want A,B,C,A", got)
	}
}

// Scenario: Disabled keys are never picked
//
//	Given the exa pool has key A disabled and key B enabled
//	When picks are made
//	Then only B is returned
func TestPoolPickSkipsDisabledKeys(t *testing.T) {
	pool, _ := newTestPool(t,
		config.SearchKey{Provider: "exa", APIKey: "A", Disabled: true},
		config.SearchKey{Provider: "exa", APIKey: "B"},
	)

	for i := 0; i < 3; i++ {
		if got := mustPick(t, pool, "exa", nil).APIKey; got != "B" {
			t.Fatalf("pick %d = %s, want B", i, got)
		}
	}
}

// Scenario: Picking excludes keys already tried in this request
//
//	Given the firecrawl pool has keys A and B
//	When a pick is made with A in the exclusion set
//	Then B is returned
func TestPoolPickExcludesTriedKeys(t *testing.T) {
	pool, _ := newTestPool(t,
		config.SearchKey{Provider: "firecrawl", APIKey: "A"},
		config.SearchKey{Provider: "firecrawl", APIKey: "B"},
	)
	a := mustPick(t, pool, "firecrawl", nil)
	if a.APIKey != "A" {
		t.Fatalf("first pick = %s, want A", a.APIKey)
	}

	for i := 0; i < 2; i++ {
		if got := mustPick(t, pool, "firecrawl", map[string]bool{a.ID: true}).APIKey; got != "B" {
			t.Fatalf("pick %d with A excluded = %s, want B", i, got)
		}
	}
}

// Scenario: Cooled-down key is skipped until its cooldown expires
//
//	Given key A was cooled down for 60s at T0
//	When a pick is made at T0+30s
//	Then A is not returned
//	When a pick is made at T0+61s
//	Then A is eligible again
func TestPoolCooldownSkipsKeyUntilExpiry(t *testing.T) {
	pool, clock := newTestPool(t, config.SearchKey{Provider: "tavily", APIKey: "A"})
	a := mustPick(t, pool, "tavily", nil)
	pool.Cooldown(a.ID, 60*time.Second, 429)

	clock.Advance(30 * time.Second)
	if _, err := pool.Pick("tavily", nil); err == nil {
		t.Fatal("expected A to be cooling down at T0+30s")
	}

	clock.Advance(31 * time.Second)
	if got := mustPick(t, pool, "tavily", nil).APIKey; got != "A" {
		t.Fatalf("pick after expiry = %s, want A", got)
	}
}

// Scenario: All keys unavailable
//
//	Given every key in the tavily pool is disabled or cooling down
//	When a pick is made
//	Then it fails with a "no available key" error carrying the earliest recovery time
func TestPoolPickFailsWhenAllKeysUnavailable(t *testing.T) {
	pool, clock := newTestPool(t,
		config.SearchKey{Provider: "tavily", APIKey: "A", Disabled: true},
		config.SearchKey{Provider: "tavily", APIKey: "B"},
		config.SearchKey{Provider: "tavily", APIKey: "C"},
	)
	b := mustPick(t, pool, "tavily", nil)
	c := mustPick(t, pool, "tavily", nil)
	pool.Cooldown(b.ID, 10*time.Minute, 432)
	pool.Cooldown(c.ID, 2*time.Minute, 429)

	_, err := pool.Pick("tavily", nil)
	var noKey *NoAvailableKeyError
	if !errors.As(err, &noKey) {
		t.Fatalf("err = %v, want *NoAvailableKeyError", err)
	}
	if want := clock.Now().Add(2 * time.Minute); !noKey.RetryAt.Equal(want) {
		t.Fatalf("RetryAt = %v, want %v", noKey.RetryAt, want)
	}
}

// Scenario: Provider with no configured keys
//
//	Given no exa keys are configured
//	When a pick is made for exa
//	Then it fails with a "provider not configured" error
func TestPoolPickFailsForUnconfiguredProvider(t *testing.T) {
	pool, _ := newTestPool(t, config.SearchKey{Provider: "tavily", APIKey: "A"})

	if _, err := pool.Pick("exa", nil); !errors.Is(err, ErrProviderNotConfigured) {
		t.Fatalf("err = %v, want ErrProviderNotConfigured", err)
	}
	if pool.HasProvider("exa") {
		t.Fatal("HasProvider(exa) = true, want false")
	}
	if !pool.HasProvider("tavily") {
		t.Fatal("HasProvider(tavily) = false, want true")
	}
}

// Scenario: Hot reload keeps cooldown and cursor state for unchanged keys
//
//	Given key A is cooling down and the pool is rebuilt from a new config that still contains A plus a new key D
//	When a pick is made
//	Then A is still cooling down and D is eligible
func TestPoolReloadPreservesStateForUnchangedKeys(t *testing.T) {
	pool, _ := newTestPool(t, config.SearchKey{Provider: "tavily", APIKey: "A"})
	a := mustPick(t, pool, "tavily", nil)
	pool.Cooldown(a.ID, time.Hour, 432)

	pool.Update([]config.SearchKey{
		{Provider: "tavily", APIKey: "A", Label: "renamed"},
		{Provider: "tavily", APIKey: "D"},
	})

	for i := 0; i < 3; i++ {
		if got := mustPick(t, pool, "tavily", nil).APIKey; got != "D" {
			t.Fatalf("pick %d = %s, want D while A cools down", i, got)
		}
	}
	for _, st := range pool.Snapshot() {
		if st.ID == a.ID && st.Label != "renamed" {
			t.Fatalf("label = %q, want updated label", st.Label)
		}
	}
}

// Scenario: Hot reload drops state for removed keys
//
//	Given key B had a cooldown and B is removed from config
//	When the pool is rebuilt
//	Then B no longer appears in the status snapshot
func TestPoolReloadDropsRemovedKeys(t *testing.T) {
	pool, _ := newTestPool(t,
		config.SearchKey{Provider: "exa", APIKey: "A"},
		config.SearchKey{Provider: "exa", APIKey: "B"},
	)
	_ = mustPick(t, pool, "exa", nil)
	b := mustPick(t, pool, "exa", nil)
	pool.Cooldown(b.ID, time.Hour, 402)

	pool.Update([]config.SearchKey{{Provider: "exa", APIKey: "A"}})

	for _, st := range pool.Snapshot() {
		if st.ID == b.ID {
			t.Fatalf("removed key B still in snapshot: %#v", st)
		}
	}
	if _, ok := pool.Lookup(b.ID); ok {
		t.Fatal("Lookup(B) succeeded after removal")
	}
}

// Scenario: Manual cooldown reset
//
//	Given key A is cooling down
//	When the cooldown for A is reset
//	Then A is eligible immediately
func TestPoolResetCooldownMakesKeyEligible(t *testing.T) {
	pool, _ := newTestPool(t,
		config.SearchKey{Provider: "tavily", APIKey: "A"},
		config.SearchKey{Provider: "exa", APIKey: "E"},
	)
	a := mustPick(t, pool, "tavily", nil)
	e := mustPick(t, pool, "exa", nil)
	pool.Cooldown(a.ID, time.Hour, 432)
	pool.Cooldown(e.ID, time.Hour, 402)

	if n := pool.ResetCooldown("tavily", "A"); n != 1 {
		t.Fatalf("ResetCooldown(tavily, A) = %d, want 1", n)
	}
	if got := mustPick(t, pool, "tavily", nil).APIKey; got != "A" {
		t.Fatalf("pick = %s, want A", got)
	}
	if _, err := pool.Pick("exa", nil); err == nil {
		t.Fatal("exa key should still be cooling down")
	}

	if n := pool.ResetAllCooldowns(); n != 1 {
		t.Fatalf("ResetAllCooldowns = %d, want 1", n)
	}
	if got := mustPick(t, pool, "exa", nil).APIKey; got != "E" {
		t.Fatalf("pick = %s, want E", got)
	}
}

// Scenario: Status snapshot never exposes key material
//
//	Given keys with labels and secrets
//	When the status snapshot is taken
//	Then each entry has provider, label, masked key, cooldown-until, last status, request and failure counts
//	And no entry contains the full secret
func TestPoolStatusSnapshotMasksKeys(t *testing.T) {
	const secret = "tvly-dev-1234567890abcdef"
	pool, clock := newTestPool(t, config.SearchKey{Provider: "tavily", APIKey: secret, Label: "acc1"})
	a := mustPick(t, pool, "tavily", nil)
	pool.Report(a.ID, 200, false)
	pool.Report(a.ID, 432, true)
	pool.Cooldown(a.ID, time.Hour, 432)

	snap := pool.Snapshot()
	if len(snap) != 1 {
		t.Fatalf("snapshot len = %d, want 1", len(snap))
	}
	st := snap[0]
	if st.Provider != "tavily" || st.Label != "acc1" || st.LastStatus != 432 ||
		st.Requests != 2 || st.Failures != 1 || !st.CooldownUntil.Equal(clock.Now().Add(time.Hour)) {
		t.Fatalf("unexpected status: %#v", st)
	}
	if st.MaskedKey == "" || strings.Contains(st.MaskedKey, secret) || strings.Contains(st.ID, secret) {
		t.Fatalf("snapshot leaks key: %#v", st)
	}
}
