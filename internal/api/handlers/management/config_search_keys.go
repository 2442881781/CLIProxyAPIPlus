package management

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/searchproxy"
)

// SetSearchService wires the runtime search proxy used by status, quota and cooldown endpoints.
func (h *Handler) SetSearchService(svc *searchproxy.Service) { h.searchService = svc }

// search-api-key: []SearchKey
func (h *Handler) GetSearchKeys(c *gin.Context) {
	h.mu.Lock()
	keys := append([]config.SearchKey{}, h.cfg.SearchKey...)
	h.mu.Unlock()
	c.JSON(http.StatusOK, gin.H{"search-api-key": keys})
}

func (h *Handler) PutSearchKeys(c *gin.Context) {
	data, errRead := c.GetRawData()
	if errRead != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "failed to read body"})
		return
	}
	var entries []config.SearchKey
	if errArray := json.Unmarshal(data, &entries); errArray != nil {
		var obj struct {
			Items []config.SearchKey `json:"items"`
		}
		if errObject := json.Unmarshal(data, &obj); errObject != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
			return
		}
		entries = obj.Items
	}
	for i := range entries {
		normalized, ok := config.NormalizeSearchKey(entries[i])
		if !ok {
			c.JSON(http.StatusBadRequest, gin.H{"error": invalidSearchKeyMessage(i, normalized)})
			return
		}
		entries[i] = normalized
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	h.cfg.SearchKey = entries
	h.cfg.SanitizeSearchKeys()
	h.persistLocked(c)
}

func (h *Handler) PatchSearchKey(c *gin.Context) {
	type searchKeyPatch struct {
		Provider *string  `json:"provider"`
		APIKey   *string  `json:"api-key"`
		Label    *string  `json:"label"`
		BaseURL  *string  `json:"base-url"`
		ProxyURL *string  `json:"proxy-url"`
		Disabled *bool    `json:"disabled"`
		Budget   *float64 `json:"budget"`
	}
	var body struct {
		Index *int            `json:"index"`
		Match *string         `json:"match"`
		Value *searchKeyPatch `json:"value"`
	}
	if errBind := c.ShouldBindJSON(&body); errBind != nil || body.Value == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
		return
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	target := -1
	if body.Index != nil && *body.Index >= 0 && *body.Index < len(h.cfg.SearchKey) {
		target = *body.Index
	}
	if target == -1 && body.Match != nil {
		match := strings.TrimSpace(*body.Match)
		for i := range h.cfg.SearchKey {
			if h.cfg.SearchKey[i].APIKey == match {
				target = i
				break
			}
		}
	}
	if target == -1 {
		c.JSON(http.StatusNotFound, gin.H{"error": "item not found"})
		return
	}

	entry := h.cfg.SearchKey[target]
	patch := body.Value
	if patch.Provider != nil {
		entry.Provider = *patch.Provider
	}
	if patch.APIKey != nil {
		entry.APIKey = *patch.APIKey
	}
	if patch.Label != nil {
		entry.Label = *patch.Label
	}
	if patch.BaseURL != nil {
		entry.BaseURL = *patch.BaseURL
	}
	if patch.ProxyURL != nil {
		entry.ProxyURL = *patch.ProxyURL
	}
	if patch.Disabled != nil {
		entry.Disabled = *patch.Disabled
	}
	if patch.Budget != nil {
		entry.Budget = *patch.Budget
	}
	normalized, ok := config.NormalizeSearchKey(entry)
	if !ok {
		c.JSON(http.StatusBadRequest, gin.H{"error": invalidSearchKeyMessage(target, normalized)})
		return
	}
	h.cfg.SearchKey[target] = normalized
	h.cfg.SanitizeSearchKeys()
	h.persistLocked(c)
}

func (h *Handler) DeleteSearchKey(c *gin.Context) {
	h.mu.Lock()
	defer h.mu.Unlock()

	target := -1
	if apiKey := strings.TrimSpace(c.Query("api-key")); apiKey != "" {
		provider := strings.ToLower(strings.TrimSpace(c.Query("provider")))
		for i, entry := range h.cfg.SearchKey {
			if entry.APIKey == apiKey && (provider == "" || entry.Provider == provider) {
				target = i
				break
			}
		}
	} else if raw := c.Query("index"); raw != "" {
		idx, errParse := strconv.Atoi(raw)
		if errParse != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid index"})
			return
		}
		if idx >= 0 && idx < len(h.cfg.SearchKey) {
			target = idx
		}
	} else {
		c.JSON(http.StatusBadRequest, gin.H{"error": "missing api-key or index"})
		return
	}
	if target == -1 {
		c.JSON(http.StatusNotFound, gin.H{"error": "item not found"})
		return
	}
	h.cfg.SearchKey = append(h.cfg.SearchKey[:target], h.cfg.SearchKey[target+1:]...)
	h.persistLocked(c)
}

// searchKeyStatusView adds the search-api-key config index so clients can join status to entries.
type searchKeyStatusView struct {
	searchproxy.KeyStatus
	Index int `json:"index"`
}

// GetSearchKeyStatus returns runtime rotation and quota state for every search key; key material is masked.
func (h *Handler) GetSearchKeyStatus(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"keys": h.searchKeyStatusViews()})
}

func (h *Handler) searchKeyStatusViews() []searchKeyStatusView {
	h.mu.Lock()
	indexByID := make(map[string]int, len(h.cfg.SearchKey))
	for i, entry := range h.cfg.SearchKey {
		indexByID[searchproxy.KeyID(entry.Provider, entry.APIKey)] = i
	}
	h.mu.Unlock()

	views := []searchKeyStatusView{}
	if h.searchService == nil {
		return views
	}
	for _, st := range h.searchService.Pool().Snapshot() {
		index, ok := indexByID[st.ID]
		if !ok {
			// The pool has not caught up with a config change yet.
			continue
		}
		views = append(views, searchKeyStatusView{KeyStatus: st, Index: index})
	}
	return views
}

// searchKeyTarget is the request body shared by per-key search actions.
type searchKeyTarget struct {
	Provider string `json:"provider"`
	APIKey   string `json:"api-key"`
	All      bool   `json:"all"`
}

func bindSearchKeyTarget(c *gin.Context) (searchKeyTarget, bool) {
	var body searchKeyTarget
	if errBind := c.ShouldBindJSON(&body); errBind != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
		return body, false
	}
	body.Provider = strings.ToLower(strings.TrimSpace(body.Provider))
	body.APIKey = strings.TrimSpace(body.APIKey)
	if !body.All && (body.Provider == "" || body.APIKey == "") {
		c.JSON(http.StatusBadRequest, gin.H{"error": "provide provider and api-key, or all=true"})
		return body, false
	}
	return body, true
}

// RefreshSearchKeyQuota queries remote quota for one key or all keys and returns the updated status.
func (h *Handler) RefreshSearchKeyQuota(c *gin.Context) {
	target, ok := bindSearchKeyTarget(c)
	if !ok {
		return
	}
	if h.searchService != nil {
		var ids []string
		if !target.All {
			ids = []string{searchproxy.KeyID(target.Provider, target.APIKey)}
		}
		h.searchService.RefreshQuotas(c.Request.Context(), ids)
	}
	c.JSON(http.StatusOK, gin.H{"keys": h.searchKeyStatusViews()})
}

// ResetSearchKeySpend clears the locally tracked month-to-date spend of one key.
func (h *Handler) ResetSearchKeySpend(c *gin.Context) {
	target, ok := bindSearchKeyTarget(c)
	if !ok {
		return
	}
	if target.All || h.searchService == nil || !h.searchService.ResetSpend(target.Provider, target.APIKey) {
		c.JSON(http.StatusNotFound, gin.H{"error": "item not found"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

// ResetSearchKeyCooldown clears cooldowns for one key ({"provider","api-key"}) or all keys ({"all":true}).
func (h *Handler) ResetSearchKeyCooldown(c *gin.Context) {
	target, ok := bindSearchKeyTarget(c)
	if !ok {
		return
	}
	reset := 0
	if h.searchService != nil {
		if target.All {
			reset = h.searchService.Pool().ResetAllCooldowns()
		} else {
			reset = h.searchService.Pool().ResetCooldown(target.Provider, target.APIKey)
		}
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok", "reset": reset})
}

func invalidSearchKeyMessage(index int, entry config.SearchKey) string {
	if !config.IsSearchProvider(entry.Provider) {
		return fmt.Sprintf("search-api-key[%d].provider must be one of %s", index, strings.Join(config.SearchProviders, ", "))
	}
	return fmt.Sprintf("search-api-key[%d].api-key is required", index)
}
