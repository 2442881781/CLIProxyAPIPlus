package cliproxy

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func TestBuildConfigModelsPropagatesMaxContextLength(t *testing.T) {
	const want = 1048576

	tests := []struct {
		name string
		got  func() *ModelInfo
	}{
		{
			name: "codex",
			got: func() *ModelInfo {
				return buildCodexConfigModels(&config.CodexKey{
					Models: []config.CodexModel{{
						Name: "codex-upstream", Alias: "codex-alias", MaxContextLength: want,
					}},
				})[0]
			},
		},
		{
			name: "claude",
			got: func() *ModelInfo {
				return buildClaudeConfigModels(&config.ClaudeKey{
					Models: []config.ClaudeModel{{
						Name: "claude-upstream", Alias: "claude-alias", MaxContextLength: want,
					}},
				})[0]
			},
		},
		{
			name: "gemini",
			got: func() *ModelInfo {
				return buildGeminiConfigModels(&config.GeminiKey{
					Models: []config.GeminiModel{{
						Name: "gemini-upstream", Alias: "gemini-alias", MaxContextLength: want,
					}},
				})[0]
			},
		},
		{
			name: "interactions",
			got: func() *ModelInfo {
				return buildGeminiConfigModels(&config.GeminiKey{
					Models: []config.GeminiModel{{
						Name: "interactions-upstream", Alias: "interactions-alias", MaxContextLength: want,
					}},
				})[0]
			},
		},
		{
			name: "xai",
			got: func() *ModelInfo {
				return buildXAIConfigModels(&config.XAIKey{
					Models: []config.XAIModel{{
						Name: "xai-upstream", Alias: "xai-alias", MaxContextLength: want,
					}},
				})[0]
			},
		},
		{
			name: "openai compatibility",
			got: func() *ModelInfo {
				return buildOpenAICompatibilityConfigModels(&config.OpenAICompatibility{
					Models: []config.OpenAICompatibilityModel{{
						Name: "compat-upstream", Alias: "compat-alias", MaxContextLength: want,
					}},
				})[0]
			},
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			model := testCase.got()
			if model == nil {
				t.Fatal("model = nil")
			}
			if model.ContextLength != want {
				t.Errorf("context length = %d, want %d", model.ContextLength, want)
			}
			if model.MaxContextLength != want {
				t.Errorf("max context length = %d, want %d", model.MaxContextLength, want)
			}
		})
	}
}

func TestOpenAICompatibilityModelPropagatesMaxTokens(t *testing.T) {
	// Given a compatibility model that declares its completion token cap
	// Then the registered model carries it, so catalogs can advertise maxTokens
	const wantContext = 1000000
	const wantTokens = 128000

	model := buildOpenAICompatibilityConfigModels(&config.OpenAICompatibility{
		Models: []config.OpenAICompatibilityModel{{
			Name:             "glm-5.3",
			Alias:            "glm-5.3",
			MaxContextLength: wantContext,
			MaxTokens:        wantTokens,
		}},
	})[0]
	if model == nil {
		t.Fatal("model = nil")
	}
	if model.MaxCompletionTokens != wantTokens {
		t.Fatalf("max completion tokens = %d, want %d", model.MaxCompletionTokens, wantTokens)
	}
	if model.ContextLength != wantContext {
		t.Fatalf("context length = %d, want %d", model.ContextLength, wantContext)
	}
}

func TestOpenAICompatibilityModelWithoutMaxTokensKeepsZero(t *testing.T) {
	// Given a model that declares no cap
	// Then nothing is invented for it
	model := buildOpenAICompatibilityConfigModels(&config.OpenAICompatibility{
		Models: []config.OpenAICompatibilityModel{{Name: "glm-5", Alias: "glm-5"}},
	})[0]
	if model == nil {
		t.Fatal("model = nil")
	}
	if model.MaxCompletionTokens != 0 {
		t.Fatalf("max completion tokens = %d, want 0", model.MaxCompletionTokens)
	}
}
