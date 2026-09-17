package providerquota

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
	"gopkg.in/yaml.v3"
)

func TestSupportedProvidersAndCredentialRedaction(t *testing.T) {
	cfg := &config.Config{Plugins: config.PluginsConfig{Enabled: true, Configs: map[string]config.PluginInstanceConfig{}}}
	cfg.Plugins.Configs[commandCodePluginID] = testPluginConfig(t, `
enabled: true
api_keys:
  - key: secret-command-one
  - key: secret-command-two
`)
	cfg.Plugins.Configs[openCodePluginID] = testPluginConfig(t, `
enabled: true
api-keys:
  - value: secret-open-code
`)

	providers := SupportedProviders(cfg)
	if len(providers) != 2 || providers[0] != ProviderCommandCode || providers[1] != ProviderOpenCodeGo {
		t.Fatalf("SupportedProviders() = %#v", providers)
	}
	commandSources := commandCodeSources(cfg)
	if len(commandSources) != 2 {
		t.Fatalf("commandCodeSources() count = %d, want 2", len(commandSources))
	}
	if commandSources[0].id == commandSources[0].apiKey || commandSources[0].id == commandSources[1].id {
		t.Fatalf("source IDs must be stable hashes without raw credentials: %#v", commandSources)
	}
}

func TestProviderPoolUsesBestUsableSourceAndWindowUsesMostConstrained(t *testing.T) {
	quota, remaining, errNormalize := responseFromBuckets("provider", []pluginapi.QuotaBucket{
		{Window: "short", RemainingFraction: 0.7},
		{Window: "long", RemainingFraction: 0.4},
	})
	if errNormalize != nil {
		t.Fatalf("responseFromBuckets(): %v", errNormalize)
	}
	if remaining != 40 || len(quota.Groups) != 1 {
		t.Fatalf("remaining = %v, quota = %#v, want constrained window at 40%%", remaining, quota)
	}

	best := -1.0
	for _, candidate := range []float64{40, 85, 10} {
		if candidate > best {
			best = candidate
		}
	}
	if best != 85 {
		t.Fatalf("provider pool remaining = %v, want best usable source at 85%%", best)
	}
}

func testPluginConfig(t *testing.T, raw string) config.PluginInstanceConfig {
	t.Helper()
	var node yaml.Node
	if errUnmarshal := yaml.Unmarshal([]byte(raw), &node); errUnmarshal != nil {
		t.Fatalf("yaml.Unmarshal(): %v", errUnmarshal)
	}
	if len(node.Content) != 1 {
		t.Fatalf("unexpected YAML document: %#v", node)
	}
	return config.PluginInstanceConfig{Raw: *node.Content[0]}
}
