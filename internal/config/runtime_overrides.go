package config

import (
	"fmt"
	"strconv"
	"strings"
)

const (
	PortOverrideEnv    = "CLIPROXY_PORT_OVERRIDE"
	AuthDirOverrideEnv = "CLIPROXY_AUTH_DIR_OVERRIDE"
)

// ApplyRuntimeOverrides applies deployment-scoped values that must survive
// config hot reloads without rewriting the shared configuration file.
func ApplyRuntimeOverrides(cfg *Config, portOverride, authDirOverride string) error {
	if cfg == nil {
		return fmt.Errorf("cannot apply runtime overrides without a config")
	}
	if authDir := strings.TrimSpace(authDirOverride); authDir != "" {
		cfg.AuthDir = authDir
	}
	portOverride = strings.TrimSpace(portOverride)
	if portOverride == "" {
		return nil
	}
	port, errPort := strconv.Atoi(portOverride)
	if errPort != nil || port < 1 || port > 65535 {
		return fmt.Errorf("invalid %s %q", PortOverrideEnv, portOverride)
	}
	cfg.Port = port
	return nil
}
