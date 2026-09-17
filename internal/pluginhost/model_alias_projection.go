package pluginhost

import (
	"bytes"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"gopkg.in/yaml.v3"
)

type configuredPluginModelAlias struct {
	Alias string `yaml:"alias"`
	Name  string `yaml:"name"`
	Fork  bool   `yaml:"fork"`
}

func (h *Host) projectConfiguredModelAliases(pluginID, provider string, models []*registry.ModelInfo) []*registry.ModelInfo {
	if h == nil || len(models) == 0 {
		return models
	}
	configYAML := h.pluginConfigYAML(pluginID)
	return projectConfiguredPluginModelAliases(configYAML, provider, models)
}

func (h *Host) pluginConfigYAML(pluginID string) []byte {
	if h == nil {
		return nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	loaded := h.loaded[strings.TrimSpace(pluginID)]
	if loaded == nil {
		return nil
	}
	return bytes.Clone(loaded.configYAML)
}

func (h *Host) configuredPublicModelForPlugin(pluginID, requestedModel string) bool {
	requestedModel = strings.TrimSpace(requestedModel)
	if requestedModel == "" {
		return false
	}
	for _, entry := range configuredPluginModelAliases(h.pluginConfigYAML(pluginID)) {
		if strings.EqualFold(entry.Alias, requestedModel) {
			return true
		}
	}
	return false
}

func projectConfiguredPluginModelAliases(configYAML []byte, provider string, models []*registry.ModelInfo) []*registry.ModelInfo {
	aliases := configuredPluginModelAliases(configYAML)
	if len(aliases) == 0 || len(models) == 0 {
		return models
	}
	provider = strings.TrimSpace(provider)
	out := make([]*registry.ModelInfo, 0, len(models))
	seen := make(map[string]struct{}, len(models))
	add := func(model *registry.ModelInfo) {
		if model == nil {
			return
		}
		id := strings.TrimSpace(model.ID)
		if id == "" {
			return
		}
		key := strings.ToLower(id)
		if _, exists := seen[key]; exists {
			return
		}
		seen[key] = struct{}{}
		out = append(out, model)
	}

	for _, model := range models {
		if model == nil {
			continue
		}
		id := strings.TrimSpace(model.ID)
		matched := false
		keepOriginal := false
		for _, entry := range aliases {
			if !configuredPluginModelMatches(id, provider, entry.Name) {
				continue
			}
			matched = true
			keepOriginal = keepOriginal || entry.Fork
			clone := cloneRegistryModels([]*registry.ModelInfo{model})[0]
			clone.MetadataModelID = id
			clone.ID = strings.TrimSpace(entry.Alias)
			if clone.Name != "" {
				clone.Name = clone.ID
			}
			add(clone)
		}
		if !matched || keepOriginal {
			add(model)
		}
	}
	return out
}

func configuredPluginModelAliases(configYAML []byte) []configuredPluginModelAlias {
	if len(configYAML) == 0 {
		return nil
	}
	var document struct {
		Models []yaml.Node `yaml:"models"`
	}
	if err := yaml.Unmarshal(configYAML, &document); err != nil {
		return nil
	}
	out := make([]configuredPluginModelAlias, 0, len(document.Models))
	for index := range document.Models {
		node := &document.Models[index]
		if node.Kind != yaml.MappingNode {
			continue
		}
		var entry configuredPluginModelAlias
		if err := node.Decode(&entry); err != nil {
			continue
		}
		entry.Alias = strings.TrimSpace(entry.Alias)
		entry.Name = strings.TrimSpace(entry.Name)
		if entry.Alias == "" || entry.Name == "" {
			continue
		}
		out = append(out, entry)
	}
	return out
}

func configuredPluginModelMatches(modelID, provider, configuredName string) bool {
	modelID = strings.TrimSpace(modelID)
	configuredName = strings.TrimSpace(configuredName)
	if modelID == "" || configuredName == "" {
		return false
	}
	if strings.EqualFold(modelID, configuredName) {
		return true
	}
	provider = strings.Trim(strings.TrimSpace(provider), "/")
	return provider != "" && strings.EqualFold(modelID, provider+"/"+configuredName)
}
