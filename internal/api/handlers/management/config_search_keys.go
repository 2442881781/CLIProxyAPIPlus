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

// SetSearchPool wires the runtime search key pool used by status and cooldown endpoints.
func (h *Handler) SetSearchPool(pool *searchproxy.Pool) { h.searchPool = pool }

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
		Provider *string `json:"provider"`
		APIKey   *string `json:"api-key"`
		Label    *string `json:"label"`
		BaseURL  *string `json:"base-url"`
		ProxyURL *string `json:"proxy-url"`
		Disabled *bool   `json:"disabled"`
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

// GetSearchKeyStatus returns runtime rotation state for every search key; key material is masked.
func (h *Handler) GetSearchKeyStatus(c *gin.Context) {
	h.mu.Lock()
	indexByID := make(map[string]int, len(h.cfg.SearchKey))
	for i, entry := range h.cfg.SearchKey {
		indexByID[searchproxy.KeyID(entry.Provider, entry.APIKey)] = i
	}
	h.mu.Unlock()

	views := []searchKeyStatusView{}
	if h.searchPool != nil {
		for _, st := range h.searchPool.Snapshot() {
			index, ok := indexByID[st.ID]
			if !ok {
				// The pool has not caught up with a config change yet.
				continue
			}
			views = append(views, searchKeyStatusView{KeyStatus: st, Index: index})
		}
	}
	c.JSON(http.StatusOK, gin.H{"keys": views})
}

// ResetSearchKeyCooldown clears cooldowns for one key ({"provider","api-key"}) or all keys ({"all":true}).
func (h *Handler) ResetSearchKeyCooldown(c *gin.Context) {
	var body struct {
		Provider string `json:"provider"`
		APIKey   string `json:"api-key"`
		All      bool   `json:"all"`
	}
	if errBind := c.ShouldBindJSON(&body); errBind != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
		return
	}
	provider := strings.ToLower(strings.TrimSpace(body.Provider))
	apiKey := strings.TrimSpace(body.APIKey)
	if !body.All && (provider == "" || apiKey == "") {
		c.JSON(http.StatusBadRequest, gin.H{"error": "provide provider and api-key, or all=true"})
		return
	}
	if h.searchPool == nil {
		c.JSON(http.StatusOK, gin.H{"status": "ok", "reset": 0})
		return
	}
	reset := 0
	if body.All {
		reset = h.searchPool.ResetAllCooldowns()
	} else {
		reset = h.searchPool.ResetCooldown(provider, apiKey)
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok", "reset": reset})
}

func invalidSearchKeyMessage(index int, entry config.SearchKey) string {
	if !config.IsSearchProvider(entry.Provider) {
		return fmt.Sprintf("search-api-key[%d].provider must be one of %s", index, strings.Join(config.SearchProviders, ", "))
	}
	return fmt.Sprintf("search-api-key[%d].api-key is required", index)
}
