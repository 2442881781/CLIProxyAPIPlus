package config

import (
	"reflect"
	"testing"
)

func TestNormalizeOAuthAllowedModels(t *testing.T) {
	got := NormalizeOAuthAllowedModels(map[string][]string{
		" OpenCode-Go ": {" gpt-* ", "GPT-*", "custom/model", ""},
		"empty":         {" "},
	})
	want := map[string][]string{"opencode-go": {"gpt-*", "custom/model"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("NormalizeOAuthAllowedModels() = %#v, want %#v", got, want)
	}
}
