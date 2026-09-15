package plugin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"

	"opencode-go-cliproxyapi/internal/config"
)

type authProvider struct{}

var _ pluginapi.AuthProvider = authProvider{}

func (authProvider) Identifier() string { return ProviderID }

func (authProvider) ParseAuth(_ context.Context, req pluginapi.AuthParseRequest) (pluginapi.AuthParseResponse, error) {
	debugTrace("auth parse request provider=%s file=%s raw_bytes=%d", req.Provider, req.FileName, len(req.RawJSON))
	var source struct {
		Type     string          `json:"type"`
		Provider string          `json:"provider"`
		ID       string          `json:"id"`
		Label    string          `json:"label"`
		APIKey   string          `json:"api_key"`
		Value    string          `json:"value"`
		Priority int             `json:"priority"`
		Weight   int             `json:"weight"`
		ProxyURL string          `json:"proxy_url"`
		Disabled bool            `json:"disabled"`
		APIKeys  []config.APIKey `json:"api_keys"`
	}
	if err := json.Unmarshal(req.RawJSON, &source); err != nil {
		if req.Provider == ProviderID {
			return pluginapi.AuthParseResponse{}, fmt.Errorf("opencode-go auth record has invalid JSON")
		}
		return pluginapi.AuthParseResponse{}, nil
	}
	provider := strings.TrimSpace(req.Provider)
	if provider == "" {
		provider = strings.TrimSpace(source.Type)
		if provider == "" {
			provider = strings.TrimSpace(source.Provider)
		}
	}
	if provider != ProviderID {
		return pluginapi.AuthParseResponse{Handled: false}, nil
	}
	entries := source.APIKeys
	if len(entries) == 0 {
		key := strings.TrimSpace(source.APIKey)
		if key == "" {
			key = strings.TrimSpace(source.Value)
		}
		if key != "" {
			entries = []config.APIKey{{Value: key, Label: source.Label, Priority: source.Priority, Weight: source.Weight, ProxyURL: source.ProxyURL, Disabled: source.Disabled}}
		}
	}
	if len(entries) == 0 {
		return pluginapi.AuthParseResponse{}, fmt.Errorf("opencode-go auth record has no api key")
	}
	auths := make([]pluginapi.AuthData, 0, len(entries))
	for _, entry := range entries {
		entry.Value = strings.TrimSpace(entry.Value)
		if entry.Value == "" {
			continue
		}
		id, label := quotaIdentity(entry.Value)
		if custom := strings.TrimSpace(entry.Label); custom != "" {
			label = custom
		}
		if len(entries) == 1 && strings.TrimSpace(source.ID) != "" {
			id = strings.TrimSpace(source.ID)
		}
		weight := entry.Weight
		if weight <= 0 {
			weight = 1
		}
		attrs := map[string]string{
			"api_key": entry.Value, "auth_kind": "apikey", "weight": strconv.Itoa(weight), "auth_index_seed": id,
		}
		if entry.Priority != 0 {
			attrs["priority"] = strconv.Itoa(entry.Priority)
		}
		record, err := json.Marshal(struct {
			Type     string `json:"type"`
			ID       string `json:"id"`
			Label    string `json:"label"`
			APIKey   string `json:"api_key"`
			Priority int    `json:"priority,omitempty"`
			Weight   int    `json:"weight,omitempty"`
			ProxyURL string `json:"proxy_url,omitempty"`
			Disabled bool   `json:"disabled,omitempty"`
		}{ProviderID, id, label, entry.Value, entry.Priority, weight, strings.TrimSpace(entry.ProxyURL), entry.Disabled})
		if err != nil {
			return pluginapi.AuthParseResponse{}, fmt.Errorf("opencode-go auth record encoding failed")
		}
		auths = append(auths, pluginapi.AuthData{
			Provider: ProviderID, ID: id, FileName: req.FileName, Label: label, ProxyURL: strings.TrimSpace(entry.ProxyURL),
			Disabled: entry.Disabled, StorageJSON: record, Attributes: attrs,
		})
	}
	if len(auths) == 0 {
		return pluginapi.AuthParseResponse{}, fmt.Errorf("opencode-go auth record has no api key")
	}
	if len(auths) == 1 {
		return pluginapi.AuthParseResponse{Handled: true, Auth: auths[0], Auths: auths}, nil
	}
	return pluginapi.AuthParseResponse{Handled: true, Auths: auths}, nil
}

func (authProvider) StartLogin(context.Context, pluginapi.AuthLoginStartRequest) (pluginapi.AuthLoginStartResponse, error) {
	return pluginapi.AuthLoginStartResponse{}, fmt.Errorf("opencode-go login is unsupported; configure a manual api key")
}

func (authProvider) PollLogin(context.Context, pluginapi.AuthLoginPollRequest) (pluginapi.AuthLoginPollResponse, error) {
	return pluginapi.AuthLoginPollResponse{}, fmt.Errorf("opencode-go login is unsupported; configure a manual api key")
}

func (authProvider) RefreshAuth(_ context.Context, req pluginapi.AuthRefreshRequest) (pluginapi.AuthRefreshResponse, error) {
	debugTrace("auth refresh request provider=%s id=%s storage_json_bytes=%d attr_names=%v metadata_names=%v", req.AuthProvider, req.AuthID, len(req.StorageJSON), mapKeys(req.Attributes), mapKeys(req.Metadata))
	return pluginapi.AuthRefreshResponse{Auth: pluginapi.AuthData{Provider: req.AuthProvider, ID: req.AuthID, StorageJSON: req.StorageJSON, Metadata: req.Metadata, Attributes: req.Attributes}}, nil
}

func credentialIdentity(key string) (id, label string) {
	digest := sha256.Sum256([]byte(strings.TrimSpace(key)))
	hash := hex.EncodeToString(digest[:])
	return "opencode-go-key-" + hash, "OpenCode Go credential " + hash[:12]
}

func mapKeys[T any](values map[string]T) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	return keys
}
