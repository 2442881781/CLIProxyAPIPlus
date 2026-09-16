package management

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
)

func TestMergeProviderModelsDeduplicatesCaseInsensitively(t *testing.T) {
	got := mergeProviderModels(
		[]*registry.ModelInfo{{ID: "model-a"}, nil},
		[]*registry.ModelInfo{{ID: "MODEL-A"}, {ID: "model-b"}, {ID: " "}},
	)
	if len(got) != 2 || got[0].ID != "model-a" || got[1].ID != "model-b" {
		t.Fatalf("mergeProviderModels() = %#v, want model-a and model-b", got)
	}
}
