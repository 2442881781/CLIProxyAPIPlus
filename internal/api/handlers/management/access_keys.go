package management

import (
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	storeaccess "github.com/router-for-me/CLIProxyAPI/v7/internal/access/store_access"
)

func (h *Handler) accessKeyStore(c *gin.Context) *storeaccess.Store {
	store := storeaccess.DefaultStore()
	if store == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "access key store unavailable"})
		return nil
	}
	return store
}

// accessKeyResponse masks the stored material so list/get responses never
// reveal the key itself; the plaintext key is only returned once on create or
// rotate via the "key" field.
func accessKeyResponse(entry *storeaccess.AccessKey) gin.H {
	if entry == nil {
		return nil
	}
	return gin.H{
		"id":             entry.ID,
		"key_prefix":     entry.KeyPrefix,
		"name":           entry.Name,
		"notes":          entry.Notes,
		"group":          entry.Group,
		"disabled":       entry.Disabled,
		"expires_at":     entry.ExpiresAt,
		"allowed_models": entry.AllowedModels,
		"quota":          entry.Quota,
		"rate_limit":     entry.RateLimit,
		"usage":          entry.Usage,
		"created_at":     entry.CreatedAt,
		"updated_at":     entry.UpdatedAt,
	}
}

func validExpiry(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" {
		return true
	}
	_, err := time.Parse(time.RFC3339, value)
	return err == nil
}

func validPeriod(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "daily", "monthly":
		return true
	}
	return false
}

// ListAccessKeys returns all issued access keys.
func (h *Handler) ListAccessKeys(c *gin.Context) {
	store := h.accessKeyStore(c)
	if store == nil {
		return
	}
	list := store.List()
	out := make([]gin.H, 0, len(list))
	for i := range list {
		out = append(out, accessKeyResponse(&list[i]))
	}
	c.JSON(http.StatusOK, gin.H{"access-keys": out})
}

// CreateAccessKey issues a new key. The plaintext key is returned once.
func (h *Handler) CreateAccessKey(c *gin.Context) {
	store := h.accessKeyStore(c)
	if store == nil {
		return
	}
	var body struct {
		Key           string            `json:"key"`
		Name          string            `json:"name"`
		Notes         string            `json:"notes"`
		Group         string            `json:"group"`
		Disabled      bool              `json:"disabled"`
		ExpiresAt     string            `json:"expires_at"`
		AllowedModels []string          `json:"allowed_models"`
		Quota         storeaccess.Quota `json:"quota"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
		return
	}
	if !validExpiry(body.ExpiresAt) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "expires_at must be RFC3339 or empty"})
		return
	}
	if !validPeriod(body.Quota.Period) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "quota.period must be daily, monthly or empty"})
		return
	}
	plaintext := strings.TrimSpace(body.Key)
	if plaintext == "" {
		var err error
		plaintext, err = storeaccess.GenerateKey()
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to generate key"})
			return
		}
	}
	entry, err := store.Create(plaintext, storeaccess.AccessKey{
		Name:          body.Name,
		Notes:         body.Notes,
		Group:         body.Group,
		Disabled:      body.Disabled,
		ExpiresAt:     body.ExpiresAt,
		AllowedModels: body.AllowedModels,
		Quota:         body.Quota,
	})
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	resp := accessKeyResponse(entry)
	resp["key"] = plaintext
	c.JSON(http.StatusOK, resp)
}

// PatchAccessKey updates an existing key entry addressed by ?id=.
func (h *Handler) PatchAccessKey(c *gin.Context) {
	store := h.accessKeyStore(c)
	if store == nil {
		return
	}
	id := strings.TrimSpace(c.Query("id"))
	if id == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "missing id"})
		return
	}
	var patch storeaccess.AccessKeyPatch
	if err := c.ShouldBindJSON(&patch); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
		return
	}
	if patch.ExpiresAt != nil && !validExpiry(*patch.ExpiresAt) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "expires_at must be RFC3339 or empty"})
		return
	}
	if patch.Quota != nil && !validPeriod(patch.Quota.Period) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "quota.period must be daily, monthly or empty"})
		return
	}
	entry, err := store.Update(id, patch)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, accessKeyResponse(entry))
}

// DeleteAccessKey removes an entry addressed by ?id=.
func (h *Handler) DeleteAccessKey(c *gin.Context) {
	store := h.accessKeyStore(c)
	if store == nil {
		return
	}
	id := strings.TrimSpace(c.Query("id"))
	if id == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "missing id"})
		return
	}
	if err := store.Delete(id); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"deleted": id})
}

// RotateAccessKey replaces the material of an existing key (?id=). The new
// plaintext is returned once.
func (h *Handler) RotateAccessKey(c *gin.Context) {
	store := h.accessKeyStore(c)
	if store == nil {
		return
	}
	id := strings.TrimSpace(c.Query("id"))
	if id == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "missing id"})
		return
	}
	var body struct {
		Key string `json:"key"`
	}
	_ = c.ShouldBindJSON(&body)
	entry, plaintext, err := store.Rotate(id, body.Key)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	resp := accessKeyResponse(entry)
	resp["key"] = plaintext
	c.JSON(http.StatusOK, resp)
}

// ListAccessGroups returns all key groups with their usage counters.
func (h *Handler) ListAccessGroups(c *gin.Context) {
	store := h.accessKeyStore(c)
	if store == nil {
		return
	}
	c.JSON(http.StatusOK, gin.H{"groups": store.ListGroups()})
}

// PutAccessGroup creates or updates a group addressed by its name field.
func (h *Handler) PutAccessGroup(c *gin.Context) {
	store := h.accessKeyStore(c)
	if store == nil {
		return
	}
	var body storeaccess.Group
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
		return
	}
	grp, err := store.UpsertGroup(body)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, grp)
}

// DeleteAccessGroup removes a group addressed by ?name=.
func (h *Handler) DeleteAccessGroup(c *gin.Context) {
	store := h.accessKeyStore(c)
	if store == nil {
		return
	}
	name := strings.TrimSpace(c.Query("name"))
	if name == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "missing name"})
		return
	}
	if err := store.DeleteGroup(name); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"deleted": name})
}

// ListAccessAuths returns the upstream credentials that groups can reference
// in their allowed_auths list.
func (h *Handler) ListAccessAuths(c *gin.Context) {
	if h.authManager == nil {
		c.JSON(http.StatusOK, gin.H{"auths": []gin.H{}})
		return
	}
	auths := h.authManager.List()
	out := make([]gin.H, 0, len(auths))
	for _, a := range auths {
		if a == nil || a.Provider == "" || strings.EqualFold(a.Provider, "unknown") {
			continue
		}
		out = append(out, gin.H{
			"id":       a.ID,
			"label":    a.Label,
			"provider": a.Provider,
			"prefix":   a.Prefix,
			"file":     a.FileName,
			"disabled": a.Disabled,
		})
	}
	c.JSON(http.StatusOK, gin.H{"auths": out})
}
