package plugin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

type authProvider struct {
	cfg *pluginConfig
}

var _ pluginapi.AuthProvider = (*authProvider)(nil)

func (p *authProvider) Identifier() string { return Provider }

func (p *authProvider) ParseAuth(_ context.Context, req pluginapi.AuthParseRequest) (pluginapi.AuthParseResponse, error) {
	trimmedRaw := strings.TrimSpace(string(req.RawJSON))
	if trimmedRaw == "" {
		if p != nil && p.cfg != nil && (strings.EqualFold(strings.TrimSpace(req.Provider), Provider) || strings.TrimSpace(req.Provider) == "") {
			return authResponseForEntries(p.cfg.credentialEntries(), req.FileName)
		}
		return pluginapi.AuthParseResponse{}, nil
	}
	var probe struct {
		Type     string `json:"type"`
		Provider string `json:"provider"`
	}
	if err := json.Unmarshal(req.RawJSON, &probe); err != nil {
		if strings.EqualFold(strings.TrimSpace(req.Provider), Provider) {
			return pluginapi.AuthParseResponse{}, fmt.Errorf("commandcode auth record has invalid JSON")
		}
		return pluginapi.AuthParseResponse{}, nil
	}
	provider := strings.ToLower(strings.TrimSpace(req.Provider))
	if provider == "" {
		provider = strings.ToLower(strings.TrimSpace(probe.Type))
		if provider == "" {
			provider = strings.ToLower(strings.TrimSpace(probe.Provider))
		}
	}
	if provider != Provider {
		return pluginapi.AuthParseResponse{}, nil
	}

	var source struct {
		Type     string        `json:"type"`
		Provider string        `json:"provider"`
		Label    string        `json:"label"`
		APIKey   string        `json:"api_key"`
		Key      string        `json:"key"`
		Priority int           `json:"priority"`
		Weight   int           `json:"weight"`
		ProxyURL string        `json:"proxy_url"`
		Disabled bool          `json:"disabled"`
		APIKeys  []APIKeyEntry `json:"api_keys"`
	}
	if err := json.Unmarshal(req.RawJSON, &source); err != nil {
		return pluginapi.AuthParseResponse{}, fmt.Errorf("commandcode auth record has invalid JSON")
	}

	entries := source.APIKeys
	if len(entries) == 0 {
		key := strings.TrimSpace(source.APIKey)
		if key == "" {
			key = strings.TrimSpace(source.Key)
		}
		if key != "" {
			entries = []APIKeyEntry{{
				Key: key, Label: source.Label, Priority: source.Priority, Weight: source.Weight,
				ProxyURL: source.ProxyURL, Disabled: source.Disabled,
			}}
		}
	}
	if len(entries) == 0 && p != nil && p.cfg != nil {
		entries = p.cfg.credentialEntries()
	}

	return authResponseForEntries(entries, req.FileName)
}

func authResponseForEntries(entries []APIKeyEntry, fileName string) (pluginapi.AuthParseResponse, error) {
	auths := make([]pluginapi.AuthData, 0, len(entries))
	for _, entry := range entries {
		entry.Key = strings.TrimSpace(entry.Key)
		if entry.Key == "" {
			continue
		}
		id, defaultLabel := commandCodeCredentialIdentity(entry.Key)
		label := strings.TrimSpace(entry.Label)
		if label == "" {
			label = defaultLabel
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
		}{
			Type: Provider, ID: id, Label: label, APIKey: entry.Key, Priority: entry.Priority,
			Weight: entry.normWeight(), ProxyURL: strings.TrimSpace(entry.ProxyURL), Disabled: entry.Disabled,
		})
		if err != nil {
			return pluginapi.AuthParseResponse{}, fmt.Errorf("commandcode auth record encoding failed")
		}
		attributes := entry.attributes()
		attributes["auth_index_seed"] = id
		auths = append(auths, pluginapi.AuthData{
			Provider: Provider, ID: id, FileName: fileName, Label: label,
			ProxyURL: strings.TrimSpace(entry.ProxyURL), Disabled: entry.Disabled,
			StorageJSON: record, Attributes: attributes,
		})
	}
	if len(auths) == 0 {
		return pluginapi.AuthParseResponse{}, fmt.Errorf("commandcode auth record has no api key")
	}
	return pluginapi.AuthParseResponse{Handled: true, Auths: auths}, nil
}

func (*authProvider) StartLogin(context.Context, pluginapi.AuthLoginStartRequest) (pluginapi.AuthLoginStartResponse, error) {
	return pluginapi.AuthLoginStartResponse{}, fmt.Errorf("commandcode login is unsupported; configure an api key")
}

func (*authProvider) PollLogin(context.Context, pluginapi.AuthLoginPollRequest) (pluginapi.AuthLoginPollResponse, error) {
	return pluginapi.AuthLoginPollResponse{}, fmt.Errorf("commandcode login is unsupported; configure an api key")
}

func (*authProvider) RefreshAuth(_ context.Context, req pluginapi.AuthRefreshRequest) (pluginapi.AuthRefreshResponse, error) {
	return pluginapi.AuthRefreshResponse{Auth: pluginapi.AuthData{
		Provider: req.AuthProvider, ID: req.AuthID, StorageJSON: req.StorageJSON,
		Metadata: req.Metadata, Attributes: req.Attributes,
	}}, nil
}

func commandCodeCredentialIdentity(key string) (id, label string) {
	digest := sha256.Sum256([]byte(strings.TrimSpace(key)))
	hash := hex.EncodeToString(digest[:])
	return "commandcode-key-" + hash, "CommandCode credential " + hash[:12]
}
