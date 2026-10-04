package plugin

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func TestAuthProviderExpandsConfiguredCredentials(t *testing.T) {
	cfg := parseConfig([]byte(`api_keys:
  - key: user_first
    label: Primary
    priority: 9
    weight: 7
    proxy_url: http://127.0.0.1:18080
  - key: user_second
    disabled: true
`))
	resp, err := (&authProvider{cfg: cfg}).ParseAuth(context.Background(), pluginapi.AuthParseRequest{Provider: Provider})
	if err != nil {
		t.Fatal(err)
	}
	if !resp.Handled || len(resp.Auths) != 2 {
		t.Fatalf("response = %+v", resp)
	}
	first := resp.Auths[0]
	if first.Provider != Provider || first.Label != "Primary" || first.ProxyURL != "http://127.0.0.1:18080" ||
		first.Attributes["api_key"] != "user_first" || first.Attributes["priority"] != "9" || first.Attributes["weight"] != "7" || first.Attributes["auth_index_seed"] != first.ID {
		t.Fatalf("first auth = %+v", first)
	}
	if !resp.Auths[1].Disabled || resp.Auths[1].Attributes["api_key"] != "user_second" {
		t.Fatalf("second auth = %+v", resp.Auths[1])
	}
	if strings.Contains(first.ID, "user_first") || strings.Contains(first.Label, "user_first") {
		t.Fatalf("secret leaked in auth identity: %+v", first)
	}
}

func TestAuthProviderParsesMaterializedRecord(t *testing.T) {
	resp, err := (&authProvider{}).ParseAuth(context.Background(), pluginapi.AuthParseRequest{
		Provider: Provider,
		FileName: "commandcode-key.json",
		RawJSON:  []byte(`{"type":"commandcode","id":"stable","label":"Primary","api_key":"user_secret","priority":5,"weight":3,"proxy_url":"http://proxy.local"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !resp.Handled || len(resp.Auths) != 1 {
		t.Fatalf("response = %+v", resp)
	}
	auth := resp.Auths[0]
	if !strings.HasPrefix(auth.ID, "commandcode-key-") || auth.FileName != "commandcode-key.json" || auth.Attributes["api_key"] != "user_secret" || auth.Attributes["priority"] != "5" || auth.Attributes["weight"] != "3" || auth.ProxyURL != "http://proxy.local" {
		t.Fatalf("auth = %+v", auth)
	}
}

func TestMaterializeCommandCodeAuthsIsIdempotent(t *testing.T) {
	cfg := parseConfig([]byte("api_keys:\n  - key: user_a\n  - key: user_b\n"))
	files := map[string]struct{}{}
	var saves []pluginapi.HostAuthSaveRequest
	caller := func(method string, payload []byte) ([]byte, error) {
		var result any
		switch method {
		case pluginabi.MethodHostAuthList:
			entries := make([]pluginapi.HostAuthFileEntry, 0, len(files))
			for name := range files {
				entries = append(entries, pluginapi.HostAuthFileEntry{ID: strings.TrimSuffix(name, ".json"), Name: name, Type: Provider, Provider: Provider})
			}
			result = map[string]any{"files": entries}
		case pluginabi.MethodHostAuthSave:
			var req pluginapi.HostAuthSaveRequest
			if err := json.Unmarshal(payload, &req); err != nil {
				t.Fatal(err)
			}
			saves = append(saves, req)
			files[req.Name] = struct{}{}
			result = pluginapi.HostAuthSaveResponse{Name: req.Name}
		case "host.auth.delete":
			var req struct {
				Name string `json:"name"`
			}
			if err := json.Unmarshal(payload, &req); err != nil {
				t.Fatal(err)
			}
			delete(files, req.Name)
			result = map[string]string{"name": req.Name}
		default:
			t.Fatalf("unexpected method %s", method)
		}
		rawResult, _ := json.Marshal(result)
		return json.Marshal(pluginabi.Envelope{OK: true, Result: rawResult})
	}
	bridge := NewHostBridge(caller)
	if err := materializeCommandCodeAuths(context.Background(), bridge, cfg, true); err != nil {
		t.Fatal(err)
	}
	if err := materializeCommandCodeAuths(context.Background(), bridge, cfg, true); err != nil {
		t.Fatal(err)
	}
	if len(saves) != 4 {
		t.Fatalf("saves = %d, want 4 (two idempotent upserts)", len(saves))
	}
	for _, save := range saves {
		if strings.Contains(save.Name, "user_") {
			t.Fatalf("secret leaked in filename %q", save.Name)
		}
	}
	cfg = parseConfig([]byte("api_keys:\n  - key: user_b\n"))
	if err := materializeCommandCodeAuths(context.Background(), bridge, cfg, true); err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 {
		t.Fatalf("files after key removal = %d, want 1", len(files))
	}
}
