package storeaccess

import (
	"net/http"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

func TestResolveJB_EagerRewriteDefaultsOff(t *testing.T) {
	cfg := &config.SDKConfig{
		JB: config.JBConfig{
			Enabled:  new(true),
			Defaults: config.JBPrefs{JB: new(true), Disambig: new(true)},
		},
	}
	snap := ResolveJB(cfg, nil, nil)
	if snap.EagerRewrite {
		t.Fatalf("eager rewrite must default off, it changes prompt bytes: %+v", snap.JBEffective)
	}
}

func TestResolveJB_EagerRewriteGlobalDefault(t *testing.T) {
	cfg := &config.SDKConfig{
		JB: config.JBConfig{
			Enabled:  new(true),
			Defaults: config.JBPrefs{JB: new(true), EagerRewrite: new(true)},
		},
	}
	snap := ResolveJB(cfg, nil, nil)
	if !snap.EagerRewrite {
		t.Fatalf("global default should turn eager rewrite on: %+v", snap.JBEffective)
	}
	narrowed := ResolveJB(cfg, nil, http.Header{"X-Jb": []string{"no-eager"}})
	if narrowed.EagerRewrite {
		t.Fatalf("no-eager must narrow the global default off: %+v", narrowed.JBEffective)
	}
	if !narrowed.JB {
		t.Fatalf("no-eager must not disable the rest of JB: %+v", narrowed.JBEffective)
	}
}
