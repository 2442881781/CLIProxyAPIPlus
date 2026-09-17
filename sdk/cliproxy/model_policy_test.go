package cliproxy

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/pluginhost"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func TestApplyAllowedModelsSupportsWildcards(t *testing.T) {
	models := []*ModelInfo{{ID: "gpt-5"}, {ID: "claude-opus-4"}, {ID: "custom/model"}}
	filtered := applyAllowedModels(models, []string{"GPT-*", "custom/model"})
	if len(filtered) != 2 || filtered[0].ID != "gpt-5" || filtered[1].ID != "custom/model" {
		t.Fatalf("unexpected filtered models: %#v", filtered)
	}
}

func TestOAuthModelPolicyAppliesToPluginAPIKeys(t *testing.T) {
	const provider = "test-plugin-model-policy"
	oldHasAuthProvider := pluginHostHasAuthProvider
	pluginHostHasAuthProvider = func(host *pluginhost.Host, candidate string) bool {
		return candidate == provider
	}
	t.Cleanup(func() { pluginHostHasAuthProvider = oldHasAuthProvider })

	svc := &Service{
		pluginHost: pluginhost.New(),
		cfg: &config.Config{
			OAuthAllowedModels:  map[string][]string{provider: {"allowed-*"}},
			OAuthExcludedModels: map[string][]string{provider: {"allowed-secret"}},
		},
	}
	if got := svc.oauthAllowedModels(provider, coreauth.AuthKindAPIKey); len(got) != 1 || got[0] != "allowed-*" {
		t.Fatalf("plugin API-key allowlist missing: %#v", got)
	}
	if got := svc.oauthExcludedModels(provider, coreauth.AuthKindAPIKey); len(got) != 1 || got[0] != "allowed-secret" {
		t.Fatalf("plugin API-key exclusions missing: %#v", got)
	}
	if got := svc.oauthAllowedModels("claude", coreauth.AuthKindAPIKey); got != nil {
		t.Fatalf("built-in API-key provider must ignore OAuth policy, got %#v", got)
	}
}

func TestApplyOAuthModelAliasForPluginAPIKey(t *testing.T) {
	cfg := &config.Config{OAuthModelAlias: map[string][]config.OAuthModelAlias{
		"opencode-go": {{Name: "upstream/model", Alias: "friendly", Fork: true}},
	}}
	attributes := map[string]string{coreauth.AttributePluginOwned: "true"}
	models := []*ModelInfo{{ID: "upstream/model"}}
	got := applyOAuthModelAliasForAuth(cfg, "opencode-go", coreauth.AuthKindAPIKey, attributes, models)
	if len(got) != 2 || got[0].ID != "upstream/model" || got[1].ID != "friendly" {
		t.Fatalf("plugin API-key aliases not applied: %#v", got)
	}
}

func TestApplyPluginAllowedModelsNormalizesProviderPrefix(t *testing.T) {
	models := []*ModelInfo{
		{ID: "opencode-go/deepseek-v4.1-flash"},
		{ID: "opencode-go/glm-5.3-flash"},
	}
	filtered := applyPluginAllowedModels(models, []string{"deepseek-v4.1-flash"}, "opencode-go")
	if len(filtered) != 1 {
		t.Fatalf("filtered models len = %d, want 1: %#v", len(filtered), filtered)
	}
	if filtered[0].ID != "deepseek-v4.1-flash" {
		t.Fatalf("public model ID = %q, want canonical ID", filtered[0].ID)
	}
	if filtered[0].MetadataModelID != "opencode-go/deepseek-v4.1-flash" {
		t.Fatalf("metadata model ID = %q, want prefixed upstream ID", filtered[0].MetadataModelID)
	}
}

func TestApplyPluginAllowedModelsKeepsExactCatalogMatch(t *testing.T) {
	models := []*ModelInfo{{ID: "deepseek-v4.1-flash"}}
	filtered := applyPluginAllowedModels(models, []string{"deepseek-*"}, "opencode-go")
	if len(filtered) != 1 || filtered[0] != models[0] {
		t.Fatalf("exact catalog match changed: %#v", filtered)
	}
}

func TestApplyPluginAllowedModelsCombinesExactAndCanonicalMatches(t *testing.T) {
	models := []*ModelInfo{
		{ID: "native-model"},
		{ID: "opencode-go/deepseek-v4.1-flash"},
	}
	filtered := applyPluginAllowedModels(models, []string{"native-model", "deepseek-v4.1-flash"}, "opencode-go")
	if len(filtered) != 2 || filtered[0].ID != "native-model" || filtered[1].ID != "deepseek-v4.1-flash" {
		t.Fatalf("combined plugin allowlist matches = %#v", filtered)
	}
}
