package config

import (
	"os"
	"testing"
)

func TestApplyRuntimeOverrides(t *testing.T) {
	tests := []struct {
		name        string
		port        string
		authDir     string
		wantPort    int
		wantAuthDir string
		wantErr     bool
	}{
		{name: "empty", wantPort: 18317, wantAuthDir: "/auths"},
		{name: "both", port: " 18318 ", authDir: " /slots/green/auths ", wantPort: 18318, wantAuthDir: "/slots/green/auths"},
		{name: "invalid port", port: "green", authDir: "/slots/green/auths", wantPort: 18317, wantAuthDir: "/slots/green/auths", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &Config{Port: 18317, AuthDir: "/auths"}
			err := ApplyRuntimeOverrides(cfg, tt.port, tt.authDir)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ApplyRuntimeOverrides() error = %v, wantErr %t", err, tt.wantErr)
			}
			if cfg.Port != tt.wantPort || cfg.AuthDir != tt.wantAuthDir {
				t.Fatalf("config = port %d auth-dir %q, want port %d auth-dir %q", cfg.Port, cfg.AuthDir, tt.wantPort, tt.wantAuthDir)
			}
		})
	}
}

func TestLoadConfigAppliesRuntimeOverrides(t *testing.T) {
	t.Setenv(PortOverrideEnv, "18318")
	t.Setenv(AuthDirOverrideEnv, "/slots/green/auths")
	path := t.TempDir() + "/config.yaml"
	if errWrite := os.WriteFile(path, []byte("port: 18317\nauth-dir: /auths\n"), 0o600); errWrite != nil {
		t.Fatal(errWrite)
	}
	cfg, errLoad := LoadConfig(path)
	if errLoad != nil {
		t.Fatalf("LoadConfig() error = %v", errLoad)
	}
	if cfg.Port != 18318 || cfg.AuthDir != "/slots/green/auths" {
		t.Fatalf("config = port %d auth-dir %q", cfg.Port, cfg.AuthDir)
	}
}

func TestLoadConfigRejectsInvalidPortOverride(t *testing.T) {
	t.Setenv(PortOverrideEnv, "green")
	path := t.TempDir() + "/config.yaml"
	if errWrite := os.WriteFile(path, []byte("port: 18317\n"), 0o600); errWrite != nil {
		t.Fatal(errWrite)
	}
	if _, errLoad := LoadConfig(path); errLoad == nil {
		t.Fatal("LoadConfig() succeeded with invalid port override")
	}
}
