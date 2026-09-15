package plugin

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// HostCaller performs one host callback round trip.
type HostCaller func(method string, payload []byte) ([]byte, error)

// HostBridge exposes the small host-auth callback surface used during plugin
// registration to materialize configured keys as independent auth records.
type HostBridge struct {
	call HostCaller
}

func NewHostBridge(call HostCaller) *HostBridge { return &HostBridge{call: call} }

func (b *HostBridge) AuthList(context.Context) ([]pluginapi.HostAuthFileEntry, error) {
	if b == nil || b.call == nil {
		return nil, fmt.Errorf("host auth list is unavailable")
	}
	payload, _ := json.Marshal(struct{}{})
	raw, err := b.call(pluginabi.MethodHostAuthList, payload)
	if err != nil {
		return nil, fmt.Errorf("host auth list failed")
	}
	var env pluginabi.Envelope
	if json.Unmarshal(raw, &env) != nil || !env.OK {
		return nil, fmt.Errorf("host auth list failed")
	}
	var response struct {
		Files []pluginapi.HostAuthFileEntry `json:"files"`
	}
	if len(env.Result) > 0 && json.Unmarshal(env.Result, &response) != nil {
		return nil, fmt.Errorf("host auth list failed")
	}
	return response.Files, nil
}

func (b *HostBridge) AuthSave(_ context.Context, req pluginapi.HostAuthSaveRequest) error {
	if b == nil || b.call == nil {
		return fmt.Errorf("host auth save is unavailable")
	}
	payload, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("host auth save failed")
	}
	raw, err := b.call(pluginabi.MethodHostAuthSave, payload)
	if err != nil {
		return fmt.Errorf("host auth save failed")
	}
	var env pluginabi.Envelope
	if json.Unmarshal(raw, &env) != nil || !env.OK {
		return fmt.Errorf("host auth save failed")
	}
	return nil
}

func (b *HostBridge) AuthDelete(_ context.Context, name string) error {
	if b == nil || b.call == nil {
		return fmt.Errorf("host auth delete is unavailable")
	}
	payload, err := json.Marshal(struct {
		Name string `json:"name"`
	}{Name: name})
	if err != nil {
		return fmt.Errorf("host auth delete failed")
	}
	raw, err := b.call("host.auth.delete", payload)
	if err != nil {
		return fmt.Errorf("host auth delete failed")
	}
	var env pluginabi.Envelope
	if json.Unmarshal(raw, &env) != nil || !env.OK {
		return fmt.Errorf("host auth delete failed")
	}
	return nil
}

func materializeCommandCodeAuths(ctx context.Context, bridge *HostBridge, cfg *pluginConfig, allowDelete bool) error {
	if bridge == nil || bridge.call == nil || cfg == nil {
		return nil
	}
	entries, err := bridge.AuthList(ctx)
	if err != nil {
		return err
	}
	existing := make(map[string]struct{}, len(entries)*2)
	managed := make(map[string]struct{})
	for _, entry := range entries {
		if id := strings.TrimSpace(entry.ID); id != "" {
			existing[id] = struct{}{}
		}
		if name := strings.TrimSpace(entry.Name); name != "" {
			existing[name] = struct{}{}
			if strings.HasPrefix(name, "commandcode-key-") && strings.HasSuffix(strings.ToLower(name), ".json") &&
				(strings.EqualFold(entry.Provider, Provider) || strings.EqualFold(entry.Type, Provider)) {
				managed[name] = struct{}{}
			}
		}
	}
	desired := make(map[string]struct{})
	for _, entry := range cfg.credentialEntries() {
		id, label := commandCodeCredentialIdentity(entry.Key)
		if custom := strings.TrimSpace(entry.Label); custom != "" {
			label = custom
		}
		name := id + ".json"
		desired[name] = struct{}{}
		record, errMarshal := json.Marshal(struct {
			Type     string `json:"type"`
			ID       string `json:"id"`
			Label    string `json:"label"`
			APIKey   string `json:"api_key"`
			Priority int    `json:"priority,omitempty"`
			Weight   int    `json:"weight,omitempty"`
			ProxyURL string `json:"proxy_url,omitempty"`
			Disabled bool   `json:"disabled,omitempty"`
		}{
			Type: Provider, ID: id, Label: label, APIKey: entry.Key, Priority: entry.Priority,
			Weight: entry.normWeight(), ProxyURL: entry.ProxyURL, Disabled: entry.Disabled,
		})
		if errMarshal != nil {
			return fmt.Errorf("commandcode auth record encoding failed")
		}
		if errSave := bridge.AuthSave(ctx, pluginapi.HostAuthSaveRequest{Name: name, JSON: record}); errSave != nil {
			return errSave
		}
		existing[id] = struct{}{}
		existing[name] = struct{}{}
	}
	if !allowDelete {
		return nil
	}
	for name := range managed {
		if _, ok := desired[name]; ok {
			continue
		}
		if errDelete := bridge.AuthDelete(ctx, name); errDelete != nil {
			return errDelete
		}
	}
	return nil
}
