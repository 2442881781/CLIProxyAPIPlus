package storeaccess

import (
	"net/http"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

func TestResolveJB_DisabledAtGlobal(t *testing.T) {
	cfg := &config.SDKConfig{}
	snap := ResolveJB(cfg, map[string]string{"key_id": "k1"}, nil)
	if snap.JB || snap.NSFW || snap.RefusalRetry {
		t.Fatalf("expected all off when global disabled, got %+v", snap.JBEffective)
	}
}

func TestResolveJB_DefaultsOnly(t *testing.T) {
	cfg := &config.SDKConfig{
		JB: config.JBConfig{
			Enabled: new(true),
			Defaults: config.JBPrefs{
				JB:       new(true),
				Disambig: new(true),
			},
		},
	}
	snap := ResolveJB(cfg, nil, nil)
	if !snap.JB || snap.NSFW || snap.RefusalRetry || !snap.Disambig {
		t.Fatalf("unexpected defaults: %+v", snap.JBEffective)
	}
}

func TestResolveJB_LayeredCeiling(t *testing.T) {
	// Global on, group off, key inherits group (no override) → off.
	origEntry, origGroup := storeEntry, storeGroup
	defer func() { storeEntry, storeGroup = origEntry, origGroup }()

	cfg := &config.SDKConfig{
		JB: config.JBConfig{
			Enabled:  new(true),
			Defaults: config.JBPrefs{JB: new(true), NSFW: new(true)},
		},
	}
	storeEntry = func(id string) *AccessKey {
		return &AccessKey{ID: id, Group: "g1", JB: &config.JBPrefs{JB: new(false)}}
	}
	storeGroup = func(name string) *Group {
		return &Group{Name: name, JB: &config.JBPrefs{NSFW: new(false)}}
	}
	snap := ResolveJB(cfg, map[string]string{"key_id": "k1", "group": "g1"}, nil)
	if snap.JB || snap.NSFW {
		t.Fatalf("key+group ceiling should zero out, got %+v", snap.JBEffective)
	}
	if !snap.Disambig {
		t.Fatalf("disambig should default on")
	}
}

func TestResolveJB_HeaderNarrowing(t *testing.T) {
	cfg := &config.SDKConfig{
		JB: config.JBConfig{
			Enabled: new(true),
			Defaults: config.JBPrefs{
				JB:           new(true),
				NSFW:         new(true),
				RefusalRetry: new(true),
			},
		},
	}
	header := http.Header{"X-Jb": []string{"no-retry, no-nsfw"}}
	snap := ResolveJB(cfg, nil, header)
	if snap.RefusalRetry || snap.NSFW {
		t.Fatalf("header should narrow: %+v", snap.JBEffective)
	}
	if !snap.JB {
		t.Fatalf("jb should remain on")
	}
}

func TestResolveJB_HeaderNarrativeRejectedByCeiling(t *testing.T) {
	cfg := &config.SDKConfig{
		JB: config.JBConfig{
			Enabled: new(true),
			Defaults: config.JBPrefs{
				JB:   new(true),
				NSFW: new(false), // ceiling off
			},
		},
	}
	header := http.Header{"X-Jb": []string{"narrative"}}
	snap := ResolveJB(cfg, nil, header)
	if snap.Narrative {
		t.Fatalf("narrative must not elevate above nsfw=off ceiling")
	}
	if !snap.HeaderRejected {
		t.Fatalf("expected header-rejected flag")
	}
}

func TestResolveJB_HeaderNarrativeAllowedWhenNSFW(t *testing.T) {
	cfg := &config.SDKConfig{
		JB: config.JBConfig{
			Enabled: new(true),
			Defaults: config.JBPrefs{
				JB:   new(true),
				NSFW: new(true),
			},
		},
	}
	header := http.Header{"X-Jb": []string{"narrative"}}
	snap := ResolveJB(cfg, nil, header)
	if !snap.Narrative || snap.HeaderRejected {
		t.Fatalf("narrative should attach when nsfw=on, got %+v", snap)
	}
}

func TestResolveJB_NoSpecDisablesJBOnly(t *testing.T) {
	cfg := &config.SDKConfig{
		JB: config.JBConfig{
			Enabled: new(true),
			Defaults: config.JBPrefs{
				JB:           new(true),
				RefusalRetry: new(true),
			},
		},
	}
	header := http.Header{"X-Jb": []string{"no-spec"}}
	snap := ResolveJB(cfg, nil, header)
	if snap.JB {
		t.Fatalf("no-spec should turn off jb for this request")
	}
	if !snap.RefusalRetry {
		t.Fatalf("no-spec must not affect refusal retry")
	}
}

func TestJBSnapshot_RoundTripMetadata(t *testing.T) {
	orig := JBSnapshot{
		JBEffective: config.JBEffective{JB: true, NSFW: true, Disambig: true},
		KeyID:       "k1",
	}
	meta := orig.InjectMetadata(nil)
	got := SnapshotFromMetadata(meta)
	if !got.JB || !got.NSFW || got.KeyID != "k1" {
		t.Fatalf("snapshot lost fields: %+v", got)
	}
}
