package diff

import (
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

// Scenario: Config diff logs changes without key material
//
//	Given the old and new configs differ in search-api-key count, disabled flag, or api-key
//	When the diff is built
//	Then the change lines mention counts / "updated" and never the key value
func TestConfigDiffSearchKeysHidesKeyMaterial(t *testing.T) {
	oldCfg := &config.Config{SearchKey: []config.SearchKey{
		{Provider: "tavily", APIKey: "tvly-old-secret", Label: "acc1"},
		{Provider: "exa", APIKey: "exa-secret"},
	}}
	sameCount := &config.Config{SearchKey: []config.SearchKey{
		{Provider: "tavily", APIKey: "tvly-new-secret", Label: "acc2", Disabled: true},
		{Provider: "exa", APIKey: "exa-secret", ProxyURL: "socks5://user:pass@proxy:1080", Budget: 10},
	}}

	changes := BuildConfigChangeDetails(oldCfg, sameCount)
	joined := strings.Join(changes, "\n")
	for _, want := range []string{
		"search-api-key[0].api-key: updated",
		"search-api-key[0].label: acc1 -> acc2",
		"search-api-key[0].disabled: false -> true",
		"search-api-key[1].proxy-url: updated",
		"search-api-key[1].budget: 0 -> 10",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("changes missing %q:\n%s", want, joined)
		}
	}
	for _, secret := range []string{"tvly-old-secret", "tvly-new-secret", "exa-secret", "user:pass"} {
		if strings.Contains(joined, secret) {
			t.Fatalf("changes leak %q:\n%s", secret, joined)
		}
	}

	countChanges := strings.Join(BuildConfigChangeDetails(oldCfg, &config.Config{}), "\n")
	if !strings.Contains(countChanges, "search-api-key count: 2 -> 0") {
		t.Fatalf("count change missing:\n%s", countChanges)
	}
}

// Scenario: search-mcp changes are logged
//
//	Given provider-order changes from [tavily, exa] to [exa] and expose-provider-tools becomes true
//	Then the diff mentions "search-mcp.provider-order: tavily,exa -> exa" and "search-mcp.expose-provider-tools: false -> true"
func TestConfigDiffSearchMCP(t *testing.T) {
	oldCfg := &config.Config{SearchMCP: config.SearchMCPConfig{ProviderOrder: []string{"tavily", "exa"}}}
	newCfg := &config.Config{SearchMCP: config.SearchMCPConfig{ProviderOrder: []string{"exa"}, ExposeProviderTools: true}}

	joined := strings.Join(BuildConfigChangeDetails(oldCfg, newCfg), "\n")

	for _, want := range []string{
		"search-mcp.provider-order: tavily,exa -> exa",
		"search-mcp.expose-provider-tools: false -> true",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("changes missing %q:\n%s", want, joined)
		}
	}
}
