package redisqueue

import (
	"context"
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

// UsageMonitorPersister stores the usage monitor snapshot in a durable backend
// that outlives the per-slot auth directory. A nil payload from
// LoadUsageMonitor means the backend has not been seeded yet.
type UsageMonitorPersister interface {
	LoadUsageMonitor(ctx context.Context) ([]byte, error)
	SaveUsageMonitor(ctx context.Context, data []byte) error
}

var (
	usageMonitorPersisterMu sync.RWMutex
	usageMonitorPersister   UsageMonitorPersister
)

// SetUsageMonitorPersister configures the durable backend used by subsequently
// configured usage monitor persistence. Passing nil keeps file-only behavior.
func SetUsageMonitorPersister(p UsageMonitorPersister) {
	usageMonitorPersisterMu.Lock()
	usageMonitorPersister = p
	usageMonitorPersisterMu.Unlock()
}

func currentUsageMonitorPersister() UsageMonitorPersister {
	usageMonitorPersisterMu.RLock()
	defer usageMonitorPersisterMu.RUnlock()
	return usageMonitorPersister
}

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

	// A durable backend keeps persistence active even without a local path.
	persister := currentUsageMonitorPersister()
	if path == "" && persister == nil {
		return nil
	}
	// The durable backend is authoritative; the local file stays as a mirror and
	// as the migration source for deployments that predate the backend.
	var fileSnapshot *persistedUsageMonitor
	if path != "" {
		var errLoad error
		fileSnapshot, errLoad = loadPersistedUsageMonitor(path)
		if errLoad != nil {
			log.WithError(errLoad).Warn("ignoring unreadable usage monitor snapshot")
			fileSnapshot = nil
		}
	}
	persisted := fileSnapshot
	if persister != nil {
		data, errBackend := persister.LoadUsageMonitor(context.Background())
		switch {
		case errBackend != nil:
			log.WithError(errBackend).Warn("failed to load usage monitor snapshot from backend")
		case len(data) == 0:
			// First run against this backend: seed it from the local snapshot.
			if fileSnapshot != nil {
				encoded, errEncode := json.Marshal(fileSnapshot)
				if errEncode != nil {
					log.WithError(errEncode).Warn("failed to encode usage monitor snapshot for backend seeding")
				} else if errSave := persister.SaveUsageMonitor(context.Background(), encoded); errSave != nil {
					log.WithError(errSave).Warn("failed to seed usage monitor snapshot into backend")
				}
			}
		default:
			backendSnapshot, errDecode := decodePersistedUsageMonitor(data)
			if errDecode != nil {
				log.WithError(errDecode).Warn("ignoring invalid usage monitor snapshot from backend")
			} else {
				persisted = backendSnapshot
			}
		}
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
	if !dirty {
		return nil
	}
	persister := currentUsageMonitorPersister()
	if path == "" && persister == nil {
		return nil
	}

	data, errMarshal := json.Marshal(snapshotPersistedUsageMonitor())
	if errMarshal != nil {
		markUsageMonitorPersistenceDirty()
		return fmt.Errorf("marshal usage monitor snapshot: %w", errMarshal)
	}
	// Persist to both targets: a failing backend must not cost us the local
	// mirror, and a failing mirror must not cost us the durable snapshot.
	var errPersist error
	if persister != nil {
		if errSave := persister.SaveUsageMonitor(context.Background(), data); errSave != nil {
			errPersist = fmt.Errorf("save usage monitor snapshot to backend: %w", errSave)
		}
	}
	if path != "" {
		if errWrite := writeUsageMonitorFileAtomic(path, data); errWrite != nil {
			if errPersist != nil {
				errPersist = fmt.Errorf("%w; mirror usage monitor snapshot: %v", errPersist, errWrite)
			} else {
				errPersist = errWrite
			}
		}
	}
	if errPersist != nil {
		markUsageMonitorPersistenceDirty()
		return errPersist
	}
	return nil
}

func scheduleUsageMonitorPersistence() {
	usageMonitorPersistence.mu.Lock()
	defer usageMonitorPersistence.mu.Unlock()
	if usageMonitorPersistence.path == "" && currentUsageMonitorPersister() == nil {
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
	if usageMonitorPersistence.path != "" || currentUsageMonitorPersister() != nil {
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
	return decodePersistedUsageMonitor(data)
}

// decodePersistedUsageMonitor validates a raw snapshot payload.
func decodePersistedUsageMonitor(data []byte) (*persistedUsageMonitor, error) {
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
