package management

import (
	"net/http"
	"sort"
	"strings"

	"github.com/gin-gonic/gin"
)

// ListProviderQuotas returns server-maintained provider-managed quota snapshots.
func (h *Handler) ListProviderQuotas(c *gin.Context) {
	if h == nil || h.authManager == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "provider quota manager unavailable"})
		return
	}
	snapshots := h.authManager.ProviderQuotas()
	sort.Slice(snapshots, func(i, j int) bool { return snapshots[i].Provider < snapshots[j].Provider })
	c.JSON(http.StatusOK, gin.H{"providers": snapshots})
}

// RefreshProviderQuota performs an immediate server-side refresh for one provider.
func (h *Handler) RefreshProviderQuota(c *gin.Context) {
	if h == nil || h.authManager == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "provider quota manager unavailable"})
		return
	}
	h.mu.Lock()
	refreshHook := h.quotaRefreshHook
	h.mu.Unlock()
	if refreshHook == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "provider quota manager unavailable"})
		return
	}
	provider := strings.ToLower(strings.TrimSpace(c.Param("provider")))
	if errRefresh := refreshHook(c.Request.Context(), provider); errRefresh != nil {
		if current, ok := h.authManager.ProviderQuota(provider); ok {
			c.JSON(http.StatusBadGateway, gin.H{"error": errRefresh.Error(), "snapshot": current})
			return
		}
		c.JSON(http.StatusBadGateway, gin.H{"error": errRefresh.Error()})
		return
	}
	snapshot, _ := h.authManager.ProviderQuota(provider)
	c.JSON(http.StatusOK, snapshot)
}
