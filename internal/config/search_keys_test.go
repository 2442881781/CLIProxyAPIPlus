package config

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// Feature: search-api-key config section
//   search-api-key:
//     - provider: tavily        # tavily | exa | firecrawl
//       api-key: tvly-xxx
//       label: acc1             # optional
//       base-url: ""            # optional, provider default when empty
//       proxy-url: ""           # optional, falls back to global proxy-url
//       disabled: false

// Scenario: Sanitize normalizes and de-duplicates entries
//
//	Given entries with mixed-case provider, whitespace around keys, and a duplicate (provider, api-key)
//	When config is loaded
//	Then providers are lower-cased, keys trimmed, and the duplicate dropped (first wins)
func TestSanitizeSearchKeysNormalizesAndDedupes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	raw := `search-api-key:
  - provider: " Tavily "
    api-key: " tvly-a "
    label: " acc1 "
    base-url: " https://tavily.example.com/ "
  - provider: tavily
    api-key: tvly-a
    label: duplicate
  - provider: EXA
    api-key: exa-a
    disabled: true
  - provider: tavily
    api-key: tvly-b
`
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}

	want := []SearchKey{
		{Provider: "tavily", APIKey: "tvly-a", Label: "acc1", BaseURL: "https://tavily.example.com"},
		{Provider: "exa", APIKey: "exa-a", Disabled: true},
		{Provider: "tavily", APIKey: "tvly-b"},
	}
	if !reflect.DeepEqual(cfg.SearchKey, want) {
		t.Fatalf("search keys = %#v, want %#v", cfg.SearchKey, want)
	}
}

// Scenario: Sanitize drops unusable entries
//
//	Given entries with an unknown provider or an empty api-key
//	When config is loaded
//	Then those entries are dropped
func TestSanitizeSearchKeysDropsInvalidEntries(t *testing.T) {
	cfg := &Config{SearchKey: []SearchKey{
		{Provider: "bing", APIKey: "k1"},
		{Provider: "firecrawl", APIKey: "   "},
		{Provider: "", APIKey: "k2"},
		{Provider: "firecrawl", APIKey: "fc-1"},
	}}

	cfg.SanitizeSearchKeys()

	want := []SearchKey{{Provider: "firecrawl", APIKey: "fc-1"}}
	if !reflect.DeepEqual(cfg.SearchKey, want) {
		t.Fatalf("search keys = %#v, want %#v", cfg.SearchKey, want)
	}
}

// Scenario: Budget is normalized
//
//	Given search keys with budget -5 and budget 10.5
//	When config is sanitized
//	Then the negative budget becomes 0 (no budget) and 10.5 is kept
func TestSanitizeSearchKeysNormalizesBudget(t *testing.T) {
	cfg := &Config{SearchKey: []SearchKey{
		{Provider: "exa", APIKey: "a", Budget: -5},
		{Provider: "exa", APIKey: "b", Budget: 10.5},
	}}

	cfg.SanitizeSearchKeys()

	if cfg.SearchKey[0].Budget != 0 || cfg.SearchKey[1].Budget != 10.5 {
		t.Fatalf("budgets = %v, %v", cfg.SearchKey[0].Budget, cfg.SearchKey[1].Budget)
	}
}

// Scenario: search-mcp settings are normalized
//
//	Given provider-order [" Exa ", "bing", "exa", "TAVILY"]
//	When config is sanitized
//	Then provider-order is [exa, tavily] (lower-cased, unknown and duplicate entries dropped)
//	And an empty or fully invalid order yields the default [tavily, exa, firecrawl]
func TestSanitizeSearchMCPProviderOrder(t *testing.T) {
	cfg := &Config{SearchMCP: SearchMCPConfig{ProviderOrder: []string{" Exa ", "bing", "exa", "TAVILY"}, ExposeProviderTools: true}}
	cfg.SanitizeSearchMCP()
	if !reflect.DeepEqual(cfg.SearchMCP.ProviderOrder, []string{"exa", "tavily"}) || !cfg.SearchMCP.ExposeProviderTools {
		t.Fatalf("search-mcp = %#v", cfg.SearchMCP)
	}

	for _, order := range [][]string{nil, {"bing", " "}} {
		cfg = &Config{SearchMCP: SearchMCPConfig{ProviderOrder: order}}
		cfg.SanitizeSearchMCP()
		if !reflect.DeepEqual(cfg.SearchMCP.ProviderOrder, []string{"tavily", "exa", "firecrawl"}) {
			t.Fatalf("order %v -> %v, want default", order, cfg.SearchMCP.ProviderOrder)
		}
	}

	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("search-mcp:\n  provider-order: [firecrawl, Tavily]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadConfig(path)
	if err != nil || !reflect.DeepEqual(loaded.SearchMCP.ProviderOrder, []string{"firecrawl", "tavily"}) {
		t.Fatalf("loaded = %#v err=%v", loaded.SearchMCP, err)
	}
}
