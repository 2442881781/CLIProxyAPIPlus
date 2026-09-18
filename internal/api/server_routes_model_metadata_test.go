package api

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
)

// Feature: per-model metadata for /v0/usage/me
//
//   The self-service panel writes an agent config from the models this key may
//   call, so a model id served by several providers must expose the metadata any
//   of them published: the registration for the same id keeps only the last
//   provider's definition, and a provider that declares nothing used to hide
//   what a richer provider had already published.

func TestMergeModelMetadataKeepsFirstNonEmptyField(t *testing.T) {
	// Given the same id registered by a metadata-poor provider and a rich one
	// Then every field resolves to the value the rich provider published
	merged := mergeModelMetadata([]*registry.ModelInfo{
		{ID: "deepseek-v4.1-flash"},
		{
			ID:                       "deepseek-v4.1-flash",
			ContextLength:            1048576,
			MaxCompletionTokens:      128000,
			SupportedInputModalities: []string{"text"},
			Thinking:                 &registry.ThinkingSupport{Levels: []string{"high", "max"}},
		},
	})

	if merged.ContextLength != 1048576 {
		t.Fatalf("context length = %d, want 1048576", merged.ContextLength)
	}
	if merged.MaxCompletionTokens != 128000 {
		t.Fatalf("max completion tokens = %d, want 128000", merged.MaxCompletionTokens)
	}
	if len(merged.SupportedInputModalities) != 1 || merged.SupportedInputModalities[0] != "text" {
		t.Fatalf("input modalities = %v", merged.SupportedInputModalities)
	}
	if merged.Thinking == nil {
		t.Fatal("thinking support was dropped")
	}
}

func TestMergeModelMetadataPrefersTheFirstDeclaration(t *testing.T) {
	// Given two providers that both declare a value
	// Then the first one wins, so the result does not depend on map order noise
	merged := mergeModelMetadata([]*registry.ModelInfo{
		{ContextLength: 200000, MaxCompletionTokens: 32768},
		{ContextLength: 1000000, MaxCompletionTokens: 128000},
	})

	if merged.ContextLength != 200000 || merged.MaxCompletionTokens != 32768 {
		t.Fatalf("merged = %+v, want the first declaration", merged)
	}
}

func TestMergeModelMetadataHandlesEmptyInput(t *testing.T) {
	merged := mergeModelMetadata(nil)
	if merged == nil {
		t.Fatal("mergeModelMetadata(nil) = nil, want an empty model")
	}
	if merged.ContextLength != 0 || merged.Thinking != nil {
		t.Fatalf("merged = %+v, want zero value", merged)
	}
}
