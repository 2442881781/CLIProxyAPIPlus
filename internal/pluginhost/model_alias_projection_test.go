package pluginhost

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
)

func TestProjectConfiguredPluginModelAliasesUsesPublicAlias(t *testing.T) {
	models := []*registry.ModelInfo{{
		ID:          "commandcode/deepseek/deepseek-v4.1-flash",
		Name:        "commandcode/deepseek/deepseek-v4.1-flash",
		DisplayName: "DeepSeek via CommandCode",
	}}
	got := projectConfiguredPluginModelAliases([]byte(`models:
  - alias: deepseek-v4.1-flash
    name: deepseek/deepseek-v4.1-flash
`), "commandcode", models)
	if len(got) != 1 {
		t.Fatalf("len(models) = %d, want 1", len(got))
	}
	if got[0].ID != "deepseek-v4.1-flash" {
		t.Fatalf("ID = %q, want public alias", got[0].ID)
	}
	if got[0].MetadataModelID != "commandcode/deepseek/deepseek-v4.1-flash" {
		t.Fatalf("MetadataModelID = %q, want internal model", got[0].MetadataModelID)
	}
}

func TestProjectConfiguredPluginModelAliasesHonorsFork(t *testing.T) {
	models := []*registry.ModelInfo{{ID: "provider/internal-model"}}
	got := projectConfiguredPluginModelAliases([]byte(`models:
  - alias: public-model
    name: internal-model
    fork: true
`), "provider", models)
	if len(got) != 2 || got[0].ID != "public-model" || got[1].ID != "provider/internal-model" {
		t.Fatalf("models = %#v, want original and public alias", got)
	}
}

func TestProjectConfiguredPluginModelAliasesProjectsAllAliases(t *testing.T) {
	models := []*registry.ModelInfo{{ID: "provider/internal-model"}}
	got := projectConfiguredPluginModelAliases([]byte(`models:
  - alias: public-model
    name: internal-model
  - alias: public-vision-model
    name: internal-model
`), "provider", models)
	if len(got) != 2 || got[0].ID != "public-model" || got[1].ID != "public-vision-model" {
		t.Fatalf("models = %#v, want both public aliases", got)
	}
}
