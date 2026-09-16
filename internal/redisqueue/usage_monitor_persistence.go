package redisqueue

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
	log "github.com/sirupsen/logrus"
)

const (
	usageMonitorPersistenceVersion = 1
	usageMonitorPersistenceDelay   = time.Second
	usageMonitorPersistenceDir     = ".usage-monitor"
	usageMonitorPersistenceFile    = "usage-monitor.json"
)

type persistedUsageMonitor struct {
	Version   int                        `json:"version"`
	Since     time.Time                  `json:"since"`
	UpdatedAt time.Time                  `json:"updated_at,omitempty"`
	Truncated bool                       `json:"truncated"`
	Requests  int64                      `json:"requests"`
	Success   int64                      `json:"success"`
	Failed    int64                      `json:"failed"`
	Tokens    UsageTokenTotals           `json:"tokens"`
	Rows      []persistedUsageModelStats `json:"rows"`
}

type persistedUsageModelStats struct {
	Provider       string           `json:"provider"`
	Model          string           `json:"model"`
	Requests       int64            `json:"requests"`
	Success        int64            `json:"success"`
	Failed         int64            `json:"failed"`
	LatencyTotalMS int64            `json:"latency_total_ms"`
	LastUsedAt     time.Time        `json:"last_used_at"`
	Tokens         UsageTokenTotals `json:"tokens"`
}

type usageMonitorPersistenceState struct {
	mu      sync.Mutex
	writeMu sync.Mutex
	path    string
	timer   *time.Timer
	dirty   bool
}

var usageMonitorPersistence usageMonitorPersistenceState

// ConfigureUsageMonitorPersistence loads and stores usage snapshots under the auth directory.
func ConfigureUsageMonitorPersistence(authDir string) error {
	authDir = strings.TrimSpace(authDir)
	path := ""
	if authDir != "" {
		path = filepath.Join(authDir, usageMonitorPersistenceDir, usageMonitorPersistenceFile)
	}

	usageMonitorPersistence.mu.Lock()
	if usageMonitorPersistence.path == path {
		usageMonitorPersistence.mu.Unlock()
		return nil
	}
	previousPath := usageMonitorPersistence.path
	usageMonitorPersistence.mu.Unlock()

	if errFlush := FlushUsageMonitorPersistence(); errFlush != nil {
		return fmt.Errorf("flush previous usage monitor snapshot: %w", errFlush)
	}

	usageMonitorPersistence.writeMu.Lock()
	defer usageMonitorPersistence.writeMu.Unlock()
	usageMonitorPersistence.mu.Lock()
	if usageMonitorPersistence.timer != nil {
		usageMonitorPersistence.timer.Stop()
		usageMonitorPersistence.timer = nil
	}
	usageMonitorPersistence.path = path
	usageMonitorPersistence.dirty = false
	usageMonitorPersistence.mu.Unlock()

	if path == "" {
		return nil
	}
	persisted, errLoad := loadPersistedUsageMonitor(path)
	if errLoad != nil {
		return errLoad
	}
	if persisted == nil {
		if previousPath != "" {
			resetUsageMonitorInMemory(time.Now())
		}
		return nil
	}
	return restorePersistedUsageMonitor(*persisted)
}

// FlushUsageMonitorPersistence atomically writes the latest accumulated usage to disk.
func FlushUsageMonitorPersistence() error {
	usageMonitorPersistence.writeMu.Lock()
	defer usageMonitorPersistence.writeMu.Unlock()

	usageMonitorPersistence.mu.Lock()
	if usageMonitorPersistence.timer != nil {
		usageMonitorPersistence.timer.Stop()
		usageMonitorPersistence.timer = nil
	}
	path := usageMonitorPersistence.path
	dirty := usageMonitorPersistence.dirty
	usageMonitorPersistence.dirty = false
	usageMonitorPersistence.mu.Unlock()
	if path == "" || !dirty {
		return nil
	}

	data, errMarshal := json.Marshal(snapshotPersistedUsageMonitor())
	if errMarshal != nil {
		markUsageMonitorPersistenceDirty()
		return fmt.Errorf("marshal usage monitor snapshot: %w", errMarshal)
	}
	if errWrite := writeUsageMonitorFileAtomic(path, data); errWrite != nil {
		markUsageMonitorPersistenceDirty()
		return errWrite
	}
	return nil
}

func scheduleUsageMonitorPersistence() {
	usageMonitorPersistence.mu.Lock()
	defer usageMonitorPersistence.mu.Unlock()
	if usageMonitorPersistence.path == "" {
		return
	}
	usageMonitorPersistence.dirty = true
	if usageMonitorPersistence.timer != nil {
		return
	}
	usageMonitorPersistence.timer = time.AfterFunc(usageMonitorPersistenceDelay, func() {
		if errFlush := FlushUsageMonitorPersistence(); errFlush != nil {
			log.WithError(errFlush).Warn("failed to persist usage monitor snapshot")
		}
	})
}

func markUsageMonitorPersistenceDirty() {
	usageMonitorPersistence.mu.Lock()
	if usageMonitorPersistence.path != "" {
		usageMonitorPersistence.dirty = true
	}
	usageMonitorPersistence.mu.Unlock()
}

func persistUsageMonitorImmediately() {
	scheduleUsageMonitorPersistence()
	if errFlush := FlushUsageMonitorPersistence(); errFlush != nil {
		log.WithError(errFlush).Warn("failed to persist usage monitor snapshot")
	}
}

func snapshotPersistedUsageMonitor() persistedUsageMonitor {
	usageMonitor.mu.RLock()
	defer usageMonitor.mu.RUnlock()

	rows := make([]persistedUsageModelStats, 0, len(usageMonitor.rows))
	for key, row := range usageMonitor.rows {
		if row == nil {
			continue
		}
		provider, _, _ := strings.Cut(key, "\x00")
		rows = append(rows, persistedUsageModelStats{
			Provider:       provider,
			Model:          row.Model,
			Requests:       row.Requests,
			Success:        row.Success,
			Failed:         row.Failed,
			LatencyTotalMS: row.latencyTotalMS,
			LastUsedAt:     row.LastUsedAt,
			Tokens:         row.Tokens,
		})
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Provider != rows[j].Provider {
			return rows[i].Provider < rows[j].Provider
		}
		return rows[i].Model < rows[j].Model
	})
	return persistedUsageMonitor{
		Version:   usageMonitorPersistenceVersion,
		Since:     usageMonitor.since,
		UpdatedAt: usageMonitor.updatedAt,
		Truncated: usageMonitor.truncated,
		Requests:  usageMonitor.requests,
		Success:   usageMonitor.success,
		Failed:    usageMonitor.failed,
		Tokens:    usageMonitor.tokens,
		Rows:      rows,
	}
}

func loadPersistedUsageMonitor(path string) (*persistedUsageMonitor, error) {
	data, errRead := os.ReadFile(path)
	if errors.Is(errRead, os.ErrNotExist) {
		return nil, nil
	}
	if errRead != nil {
		return nil, fmt.Errorf("read usage monitor snapshot: %w", errRead)
	}
	var persisted persistedUsageMonitor
	if errUnmarshal := json.Unmarshal(data, &persisted); errUnmarshal != nil {
		return nil, fmt.Errorf("decode usage monitor snapshot: %w", errUnmarshal)
	}
	if persisted.Version != usageMonitorPersistenceVersion {
		return nil, fmt.Errorf("unsupported usage monitor snapshot version %d", persisted.Version)
	}
	return &persisted, nil
}

func restorePersistedUsageMonitor(persisted persistedUsageMonitor) error {
	if persisted.Since.IsZero() {
		return errors.New("usage monitor snapshot has no start time")
	}
	if persisted.Requests < 0 || persisted.Success < 0 || persisted.Failed < 0 || len(persisted.Rows) > maxUsageMonitorRows {
		return errors.New("usage monitor snapshot contains invalid counters")
	}
	rows := make(map[string]*UsageModelStats, len(persisted.Rows))
	for _, stored := range persisted.Rows {
		if stored.Requests < 0 || stored.Success < 0 || stored.Failed < 0 || stored.LatencyTotalMS < 0 {
			return errors.New("usage monitor snapshot contains an invalid row")
		}
		provider := normalizeUsageDimension(stored.Provider, "unknown")
		model := normalizeUsageDimension(stored.Model, "unknown")
		key := provider + "\x00" + model
		if _, exists := rows[key]; exists {
			return fmt.Errorf("usage monitor snapshot contains duplicate row %q", provider+"/"+model)
		}
		averageLatencyMS := int64(0)
		if stored.Requests > 0 {
			averageLatencyMS = stored.LatencyTotalMS / stored.Requests
		}
		rows[key] = &UsageModelStats{
			Model:            model,
			Requests:         stored.Requests,
			Success:          stored.Success,
			Failed:           stored.Failed,
			AverageLatencyMS: averageLatencyMS,
			LastUsedAt:       stored.LastUsedAt,
			Tokens:           stored.Tokens,
			latencyTotalMS:   stored.LatencyTotalMS,
		}
	}

	usageMonitor.mu.Lock()
	usageMonitor.since = persisted.Since
	usageMonitor.updatedAt = persisted.UpdatedAt
	usageMonitor.truncated = persisted.Truncated
	usageMonitor.requests = persisted.Requests
	usageMonitor.success = persisted.Success
	usageMonitor.failed = persisted.Failed
	usageMonitor.tokens = persisted.Tokens
	usageMonitor.rows = rows
	usageMonitor.providerRate = make(map[string]*coreusage.RateWindow)
	usageMonitor.authRate = make(map[string]*coreusage.RateWindow)
	usageMonitor.mu.Unlock()
	return nil
}

func resetUsageMonitorInMemory(since time.Time) {
	usageMonitor.mu.Lock()
	usageMonitor.since = since
	usageMonitor.updatedAt = time.Time{}
	usageMonitor.truncated = false
	usageMonitor.requests = 0
	usageMonitor.success = 0
	usageMonitor.failed = 0
	usageMonitor.tokens = UsageTokenTotals{}
	usageMonitor.rows = make(map[string]*UsageModelStats)
	usageMonitor.providerRate = make(map[string]*coreusage.RateWindow)
	usageMonitor.authRate = make(map[string]*coreusage.RateWindow)
	usageMonitor.mu.Unlock()
}

func writeUsageMonitorFileAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	if errMkdir := os.MkdirAll(dir, 0o700); errMkdir != nil {
		return fmt.Errorf("create usage monitor directory: %w", errMkdir)
	}
	temp, errCreate := os.CreateTemp(dir, ".usage-monitor-*.tmp")
	if errCreate != nil {
		return fmt.Errorf("create usage monitor temp file: %w", errCreate)
	}
	tempPath := temp.Name()
	removeTemp := true
	closed := false
	defer func() {
		if !closed {
			_ = temp.Close()
		}
		if removeTemp {
			_ = os.Remove(tempPath)
		}
	}()
	if errChmod := temp.Chmod(0o600); errChmod != nil {
		return fmt.Errorf("secure usage monitor temp file: %w", errChmod)
	}
	if _, errWrite := temp.Write(data); errWrite != nil {
		return fmt.Errorf("write usage monitor temp file: %w", errWrite)
	}
	if errSync := temp.Sync(); errSync != nil {
		return fmt.Errorf("sync usage monitor temp file: %w", errSync)
	}
	if errClose := temp.Close(); errClose != nil {
		return fmt.Errorf("close usage monitor temp file: %w", errClose)
	}
	closed = true
	if errRename := os.Rename(tempPath, path); errRename != nil {
		if runtime.GOOS != "windows" {
			return fmt.Errorf("replace usage monitor snapshot: %w", errRename)
		}
		if errRemove := os.Remove(path); errRemove != nil && !errors.Is(errRemove, os.ErrNotExist) {
			return fmt.Errorf("remove previous usage monitor snapshot: %w", errRemove)
		}
		if errRenameRetry := os.Rename(tempPath, path); errRenameRetry != nil {
			return fmt.Errorf("replace usage monitor snapshot: %w", errRenameRetry)
		}
	}
	removeTemp = false
	return nil
}
