package diff

import "testing"

func TestDiffOAuthAllowedModelChanges(t *testing.T) {
	changes, affected := DiffOAuthAllowedModelChanges(
		map[string][]string{"provider-a": {"model-a"}},
		map[string][]string{"provider-a": {"model-b"}, "provider-b": {"model-c"}},
	)
	if len(changes) != 2 {
		t.Fatalf("changes = %#v, want two entries", changes)
	}
	if len(affected) != 2 || affected[0] != "provider-a" || affected[1] != "provider-b" {
		t.Fatalf("affected = %#v, want provider-a and provider-b", affected)
	}
}
