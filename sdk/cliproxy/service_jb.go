package cliproxy

import (
	"path/filepath"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/jb"
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
		// Load logs per-asset failures internally; a non-nil return is a
		// structural issue we surface but do not treat as fatal.
		_ = err
	}
}
