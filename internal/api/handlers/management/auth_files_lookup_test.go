package management

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

// Feature: auth file lookup tolerates a stale auth_index
//
//   auth_index is derived from the credential identity, so it moves whenever
//   that identity moves — the file-name form changed with the PostgreSQL
//   migration, and a blue/green slot switch rebuilds the spool. A caller holding
//   an index from before the change still names a unique file, so refresh must
//   resolve it instead of answering 404 "auth file not found".

func TestLookupAuthFileFallsBackToUniqueName(t *testing.T) {
	manager := coreauth.NewManager(nil, nil, nil)
	cfg := &config.Config{AuthDir: t.TempDir()}
	manager.SetConfig(cfg)
	registerAuthForLookupTest(t, manager, &coreauth.Auth{
		ID:       "devin-feifan638414.json",
		FileName: "devin-feifan638414.json",
		Provider: "devin",
		Index:    "fresh-index",
	})
	h := NewHandlerWithoutConfigFilePath(cfg, manager)

	if auth, ok := h.lookupAuthFileByUniqueName("devin-feifan638414.json", "fresh-index"); !ok || auth == nil {
		t.Fatal("matching index must resolve")
	}
	auth, ok := h.lookupAuthFileByUniqueName("devin-feifan638414.json", "stale-index")
	if !ok || auth == nil {
		t.Fatal("a unique file name must resolve even when the cached index is stale")
	}
	if auth.ID != "devin-feifan638414.json" {
		t.Fatalf("resolved %q, want devin-feifan638414.json", auth.ID)
	}
	if _, ok := h.lookupAuthFileByUniqueName("devin-feifan638414.json", ""); !ok {
		t.Fatal("an empty index must resolve by name")
	}
	if _, ok := h.lookupAuthFileByUniqueName("missing.json", "stale-index"); ok {
		t.Fatal("an unknown name must not resolve")
	}
	if _, ok := h.lookupAuthFile("devin-feifan638414.json", "stale-index"); ok {
		t.Fatal("the strict lookup must keep rejecting a stale index")
	}
}

func TestLookupAuthFileKeepsIndexDisambiguation(t *testing.T) {
	manager := coreauth.NewManager(nil, nil, nil)
	cfg := &config.Config{AuthDir: t.TempDir()}
	manager.SetConfig(cfg)
	for _, entry := range []struct{ id, index string }{
		{id: "shared-a", index: "index-a"},
		{id: "shared-b", index: "index-b"},
	} {
		registerAuthForLookupTest(t, manager, &coreauth.Auth{
			ID:       entry.id,
			FileName: "shared.json",
			Provider: "devin",
			Index:    entry.index,
		})
	}
	h := NewHandlerWithoutConfigFilePath(cfg, manager)

	auth, ok := h.lookupAuthFileByUniqueName("shared.json", "index-b")
	if !ok || auth == nil || auth.Index != "index-b" {
		t.Fatalf("index must select the matching entry: ok=%v auth=%+v", ok, auth)
	}
	if _, ok := h.lookupAuthFileByUniqueName("shared.json", "stale-index"); ok {
		t.Fatal("an ambiguous name must not resolve on a stale index")
	}
	if _, ok := h.lookupAuthFileByUniqueName("shared.json", ""); !ok {
		t.Fatal("an empty index must still resolve the first entry sharing the name")
	}
}
