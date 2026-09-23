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
