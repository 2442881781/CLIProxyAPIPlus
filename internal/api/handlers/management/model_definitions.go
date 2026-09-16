package management

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
)

// GetStaticModelDefinitions returns static model metadata for a given channel.
// Channel is provided via path param (:channel) or query param (?channel=...).
func (h *Handler) GetStaticModelDefinitions(c *gin.Context) {
	channel := strings.TrimSpace(c.Param("channel"))
	if channel == "" {
		channel = strings.TrimSpace(c.Query("channel"))
	}
	if channel == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "channel is required"})
		return
	}

	models := registry.GetStaticModelDefinitionsByChannel(channel)
	if models == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "unknown channel", "channel": channel})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"channel": strings.ToLower(strings.TrimSpace(channel)),
		"models":  models,
	})
}

// GetProviderModels returns the current runtime model catalog for a provider.
// Unlike static model definitions, this includes dynamic plugin catalog entries.
func (h *Handler) GetProviderModels(c *gin.Context) {
	provider := strings.ToLower(strings.TrimSpace(c.Param("provider")))
	if provider == "" {
		provider = strings.ToLower(strings.TrimSpace(c.Query("provider")))
	}
	if provider == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "provider is required"})
		return
	}

	models := registry.GetGlobalRegistry().GetAvailableModelsByProvider(provider)
	if h.authManager != nil {
		for _, auth := range h.authManager.List() {
			if auth == nil || !strings.EqualFold(strings.TrimSpace(auth.Provider), provider) {
				continue
			}
			models = mergeProviderModels(models, registry.GetGlobalRegistry().GetModelsForClient(auth.ID))
		}
	}
	if h.pluginHost != nil {
		models = mergeProviderModels(models, h.pluginHost.ModelsForProvider(provider))
	}
	c.JSON(http.StatusOK, gin.H{
		"provider": provider,
		"models":   models,
	})
}

func mergeProviderModels(groups ...[]*registry.ModelInfo) []*registry.ModelInfo {
	seen := make(map[string]struct{})
	models := make([]*registry.ModelInfo, 0)
	for _, group := range groups {
		for _, model := range group {
			if model == nil {
				continue
			}
			id := strings.TrimSpace(model.ID)
			if id == "" {
				continue
			}
			key := strings.ToLower(id)
			if _, exists := seen[key]; exists {
				continue
			}
			seen[key] = struct{}{}
			models = append(models, model)
		}
	}
	return models
}
