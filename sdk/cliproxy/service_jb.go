package cliproxy

import (
	"context"
	"path/filepath"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/jb"
	log "github.com/sirupsen/logrus"
)

// reloadJBEngine refreshes the shared JB engine under the latest config and
// rebinds it to every registered OpenAI-compat executor. Called from
// applyConfigRuntime so config reloads propagate corpus/spec/wordlist edits
// without restarting the server.
func (s *Service) reloadJBEngine(cfg *config.Config) {
	if s == nil || cfg == nil {
		return
	}
	// Lazily create the engine the first time JB is enabled.
	if s.jbEngine == nil {
		configDir := ""
		if s.configPath != "" {
			configDir = filepath.Dir(s.configPath)
		}
		s.jbEngine = jb.NewEngine(&cfg.SDKConfig, configDir)
	} else {
		s.jbEngine.UpdateConfig(&cfg.SDKConfig)
	}
	if err := s.jbEngine.Load(); err != nil {
		// Load logs each unusable asset and keeps the previous copy in effect;
		// the aggregated error names the misconfigured paths so operators can
		// see that JB is degraded to passthrough instead of silently no-op.
		log.WithError(err).Warn("jb: engine loaded with unavailable assets")
	}
}

// ensureJBEngine lazily creates the shared JB engine when executor
// registration runs before any config apply (boot, plugin commits, auth
// events). Without it, decorated executors registered before the first apply
// would capture a nil engine permanently. Callers must not hold
// configRuntimeMu: the config-apply path already created the engine and takes
// the fast path here.
func (s *Service) ensureJBEngine() {
	if s == nil || s.jbEngine != nil {
		return
	}
	s.configRuntimeMu.Lock()
	defer s.configRuntimeMu.Unlock()
	if s.jbEngine != nil {
		return
	}
	s.cfgMu.RLock()
	cfg := s.cfg
	s.cfgMu.RUnlock()
	if cfg != nil {
		s.reloadJBEngine(cfg)
	}
}

// jbAssetPollInterval bounds how quickly an edit to a corpus/spec/wordlist
// file takes effect. The config watcher only observes config.yaml and the
// auth directory, so asset-only edits need their own poller.
const jbAssetPollInterval = 3 * time.Second

// startJBAssetWatcher reloads the JB engine when a configured asset file
// changes on disk, so editing the corpus or a wordlist takes effect without
// touching config.yaml or restarting the process.
func (s *Service) startJBAssetWatcher(ctx context.Context) {
	if s == nil || ctx == nil {
		return
	}
	go func() {
		ticker := time.NewTicker(jbAssetPollInterval)
		defer ticker.Stop()
		var signature string
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
			next := s.jbAssetSignature()
			if next == "" {
				continue
			}
			if signature == "" {
				// First observation: the engine already loaded these assets
				// during the config apply, so record the baseline only.
				signature = next
				continue
			}
			if next == signature {
				continue
			}
			signature = next
			s.cfgMu.RLock()
			cfg := s.cfg
			s.cfgMu.RUnlock()
			if cfg == nil || !cfg.JB.IsEnabled() {
				continue
			}
			s.configRuntimeMu.Lock()
			s.reloadJBEngine(cfg)
			s.configRuntimeMu.Unlock()
			log.Info("jb: asset files changed, reloaded corpus/spec/wordlists")
		}
	}()
}

// jbAssetSignature reads the engine's asset signature under the runtime lock
// so it cannot race with a concurrent config apply.
func (s *Service) jbAssetSignature() string {
	if s == nil {
		return ""
	}
	s.configRuntimeMu.Lock()
	defer s.configRuntimeMu.Unlock()
	if s.jbEngine == nil {
		return ""
	}
	return s.jbEngine.AssetSignature()
}
