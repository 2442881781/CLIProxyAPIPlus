package config

import "strings"

// Search provider identifiers accepted in search-api-key entries.
const (
	SearchProviderTavily    = "tavily"
	SearchProviderExa       = "exa"
	SearchProviderFirecrawl = "firecrawl"
)

// SearchProviders lists the supported search providers in display order.
var SearchProviders = []string{SearchProviderTavily, SearchProviderExa, SearchProviderFirecrawl}

// SearchKey is one upstream API key for a web search provider (Tavily, Exa, Firecrawl).
// Keys of the same provider form a pool that /search/{provider} rotates through.
type SearchKey struct {
	// Provider is one of tavily, exa, firecrawl.
	Provider string `yaml:"provider" json:"provider"`
	// APIKey is the upstream provider key.
	APIKey string `yaml:"api-key" json:"api-key"`
	// Label is an optional human-readable name shown in management views.
	Label string `yaml:"label,omitempty" json:"label,omitempty"`
	// BaseURL overrides the provider default API base URL (e.g. self-hosted Firecrawl).
	BaseURL string `yaml:"base-url,omitempty" json:"base-url,omitempty"`
	// ProxyURL overrides the global proxy-url for this key.
	ProxyURL string `yaml:"proxy-url,omitempty" json:"proxy-url,omitempty"`
	// Disabled removes the key from rotation without deleting it.
	Disabled bool `yaml:"disabled,omitempty" json:"disabled,omitempty"`
	// Budget is a monthly USD spend limit tracked locally; it applies to Exa, which has no balance API.
	// Zero means no budget.
	Budget float64 `yaml:"budget,omitempty" json:"budget,omitempty"`
}

// IsSearchProvider reports whether provider is a supported search provider name.
func IsSearchProvider(provider string) bool {
	for _, p := range SearchProviders {
		if p == provider {
			return true
		}
	}
	return false
}

// NormalizeSearchKey trims fields and lower-cases the provider.
// It returns false when the entry is unusable (unknown provider or empty key).
func NormalizeSearchKey(entry SearchKey) (SearchKey, bool) {
	entry.Provider = strings.ToLower(strings.TrimSpace(entry.Provider))
	entry.APIKey = strings.TrimSpace(entry.APIKey)
	entry.Label = strings.TrimSpace(entry.Label)
	entry.BaseURL = strings.TrimRight(strings.TrimSpace(entry.BaseURL), "/")
	entry.ProxyURL = strings.TrimSpace(entry.ProxyURL)
	if entry.Budget < 0 {
		entry.Budget = 0
	}
	if !IsSearchProvider(entry.Provider) || entry.APIKey == "" {
		return entry, false
	}
	return entry, true
}

// SanitizeSearchKeys normalizes search-api-key entries, dropping invalid ones and
// duplicates of the same (provider, api-key) pair. The first occurrence wins.
func (cfg *Config) SanitizeSearchKeys() {
	if cfg == nil || len(cfg.SearchKey) == 0 {
		return
	}
	seen := make(map[string]struct{}, len(cfg.SearchKey))
	out := make([]SearchKey, 0, len(cfg.SearchKey))
	for _, entry := range cfg.SearchKey {
		normalized, ok := NormalizeSearchKey(entry)
		if !ok {
			continue
		}
		id := normalized.Provider + "\x00" + normalized.APIKey
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, normalized)
	}
	cfg.SearchKey = out
}

// SearchMCPConfig controls the /search/mcp tool surface.
type SearchMCPConfig struct {
	// ProviderOrder is the priority order used by the provider-agnostic tools (web_search, web_fetch).
	// Only listed providers are used; empty means tavily, exa, firecrawl.
	ProviderOrder []string `yaml:"provider-order,omitempty" json:"provider-order,omitempty"`
	// ExposeProviderTools also lists the per-provider tools (tavily_*, exa_*, firecrawl_*).
	ExposeProviderTools bool `yaml:"expose-provider-tools,omitempty" json:"expose-provider-tools,omitempty"`
}

// NormalizeSearchProviderOrder lower-cases the order and drops unknown or duplicate providers.
// It returns the default order when nothing valid remains.
func NormalizeSearchProviderOrder(order []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(order))
	for _, raw := range order {
		provider := strings.ToLower(strings.TrimSpace(raw))
		if !IsSearchProvider(provider) || seen[provider] {
			continue
		}
		seen[provider] = true
		out = append(out, provider)
	}
	if len(out) == 0 {
		return append([]string(nil), SearchProviders...)
	}
	return out
}

// SanitizeSearchMCP normalizes the search MCP settings.
func (cfg *Config) SanitizeSearchMCP() {
	if cfg == nil {
		return
	}
	cfg.SearchMCP.ProviderOrder = NormalizeSearchProviderOrder(cfg.SearchMCP.ProviderOrder)
}
