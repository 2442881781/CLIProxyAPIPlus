package management

import (
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	storeaccess "github.com/router-for-me/CLIProxyAPI/v7/internal/access/store_access"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/redisqueue"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
)

type usageQueueRecord []byte

func (r usageQueueRecord) MarshalJSON() ([]byte, error) {
	if json.Valid(r) {
		return append([]byte(nil), r...), nil
	}
	return json.Marshal(string(r))
}

// GetUsageQueue pops queued usage records from the usage queue.
func (h *Handler) GetUsageQueue(c *gin.Context) {
	if h == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "handler unavailable"})
		return
	}

	count, errCount := parseUsageQueueCount(c.Query("count"))
	if errCount != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": errCount.Error()})
		return
	}

	items := redisqueue.PopOldest(count)
	records := make([]usageQueueRecord, 0, len(items))
	for _, item := range items {
		records = append(records, usageQueueRecord(append([]byte(nil), item...)))
	}

	c.JSON(http.StatusOK, records)
}

func parseUsageQueueCount(value string) (int, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 1, nil
	}
	count, errCount := strconv.Atoi(value)
	if errCount != nil || count <= 0 {
		return 0, errors.New("count must be a positive integer")
	}
	return count, nil
}

// GetUsageMonitor returns non-destructive token usage aggregates grouped by provider and model.
func (h *Handler) GetUsageMonitor(c *gin.Context) {
	if h == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "handler unavailable"})
		return
	}
	c.JSON(http.StatusOK, redisqueue.GetUsageMonitorSnapshot())
}

// GetRates returns live throughput gauges over a sliding window: per provider,
// per upstream auth, and per access key. Rates are memory-only and always
// reflect the current pace, not accumulated history.
func (h *Handler) GetRates(c *gin.Context) {
	if h == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "handler unavailable"})
		return
	}
	snap := redisqueue.GetRateSnapshot()
	resp := gin.H{
		"enabled":        snap.Enabled,
		"window_seconds": snap.WindowSeconds,
		"providers":      snap.Providers,
		"auths":          snap.Auths,
		"keys":           []storeaccess.KeyRateRow{},
	}
	// The access-key store is optional; degrade to an empty list, not a 503.
	if store := storeaccess.DefaultStore(); store != nil {
		resp["keys"] = store.KeyRates()
	}
	c.JSON(http.StatusOK, resp)
}

// modelPressureRow is one client-facing model's pressure view: the exact
// in-flight gauge and 60s completed-request window from the tracker, plus the
// registry's authoritative supply breakdown.
type modelPressureRow struct {
	coreusage.ModelPressureRow
	ServingAuths       int      `json:"serving_auths"`
	SuspendedAuths     int      `json:"suspended_auths"`
	QuotaExceededAuths int      `json:"quota_exceeded_auths"`
	Providers          []string `json:"providers,omitempty"`
}

// GetModelPressure returns per-model pressure keyed by the client-facing
// model name. in_flight is exact; rate/error/latency fields are real event
// sums over a sliding 60s window; supply counts mirror the same registry
// projections the auth selector uses. Only models with recent activity are
// listed: registered models that nobody requested stay out of the table.
// Process-local, memory-only.
func (h *Handler) GetModelPressure(c *gin.Context) {
	if h == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "handler unavailable"})
		return
	}
	reg := registry.GetGlobalRegistry()
	supply := func(model string) registry.ModelSupplyStats {
		if reg == nil {
			return registry.ModelSupplyStats{}
		}
		return reg.GetModelSupplyStats(model)
	}

	// The tracker omits models whose in-flight gauge and sliding window are
	// both empty, so this loop only ever yields recently requested models.
	snapshot := coreusage.DefaultModelPressure().Snapshot()
	rows := make([]modelPressureRow, 0, len(snapshot))
	for _, snap := range snapshot {
		row := modelPressureRow{ModelPressureRow: snap}
		s := supply(snap.Model)
		row.SuspendedAuths = s.Suspended
		row.QuotaExceededAuths = s.QuotaExceeded
		row.ServingAuths = max(s.Registered-s.Suspended-s.QuotaExceeded, 0)
		row.Providers = s.Providers
		rows = append(rows, row)
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].InFlight != rows[j].InFlight {
			return rows[i].InFlight > rows[j].InFlight
		}
		if rows[i].TotalTokensPerSecond != rows[j].TotalTokensPerSecond {
			return rows[i].TotalTokensPerSecond > rows[j].TotalTokensPerSecond
		}
		return rows[i].Model < rows[j].Model
	})

	c.JSON(http.StatusOK, gin.H{
		"window_seconds": coreusage.RateWindowSeconds,
		"models":         rows,
	})
}

// ResetUsageMonitor clears all token usage aggregates, including the persisted snapshot.
func (h *Handler) ResetUsageMonitor(c *gin.Context) {
	if h == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "handler unavailable"})
		return
	}
	redisqueue.ResetUsageMonitor()
	c.JSON(http.StatusOK, gin.H{"ok": true})
}
