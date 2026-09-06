package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// writeConfig writes contents to a temp config.yml and returns its path.
func writeConfig(t *testing.T, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yml")
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("writing test config: %v", err)
	}
	return path
}

func TestLoadConfigLiveDeploymentShape(t *testing.T) {
	// Mirrors the actual /srv/pingexporter/config.yml this exporter must be
	// a drop-in replacement for (PING_EXPORTER_REQUIREMENTS.md section 3).
	path := writeConfig(t, `
targets:
  - 46.229.202.209
  - 8.8.4.4
  - 8.8.8.8
  - 1.1.1.1
  - 1.0.0.1
  - 192.168.1.1

dns:
  refresh: 2m15s
  nameserver: 1.1.1.1

ping:
  interval: 2s
  timeout: 3s
  history-size: 42
  size: 120

options:
  disableIPv6: false
`)

	cfg, err := loadConfig(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(cfg.Targets) != 6 {
		t.Fatalf("len(Targets) = %d, want 6", len(cfg.Targets))
	}
	if cfg.Targets[0] != "46.229.202.209" || cfg.Targets[5] != "192.168.1.1" {
		t.Errorf("Targets = %v, want the configured order preserved", cfg.Targets)
	}
	if cfg.DNS.Refresh.Duration() != 2*time.Minute+15*time.Second {
		t.Errorf("DNS.Refresh = %v, want 2m15s", cfg.DNS.Refresh.Duration())
	}
	if cfg.DNS.Nameserver != "1.1.1.1" {
		t.Errorf("DNS.Nameserver = %q, want 1.1.1.1", cfg.DNS.Nameserver)
	}
	if cfg.Ping.Interval.Duration() != 2*time.Second {
		t.Errorf("Ping.Interval = %v, want 2s", cfg.Ping.Interval.Duration())
	}
	if cfg.Ping.Timeout.Duration() != 3*time.Second {
		t.Errorf("Ping.Timeout = %v, want 3s", cfg.Ping.Timeout.Duration())
	}
	if cfg.Ping.HistorySize != 42 {
		t.Errorf("Ping.HistorySize = %d, want 42", cfg.Ping.HistorySize)
	}
	if cfg.Ping.Size != 120 {
		t.Errorf("Ping.Size = %d, want 120", cfg.Ping.Size)
	}
	if cfg.Options.DisableIPv6 {
		t.Error("Options.DisableIPv6 = true, want false")
	}
}

func TestLoadConfigMissingFile(t *testing.T) {
	if _, err := loadConfig(filepath.Join(t.TempDir(), "does-not-exist.yml")); err == nil {
		t.Fatal("expected an error for a missing config file")
	}
}

func TestLoadConfigBadYAML(t *testing.T) {
	path := writeConfig(t, "targets: [this is not: valid yaml")
	if _, err := loadConfig(path); err == nil {
		t.Fatal("expected an error for malformed YAML")
	}
}

func TestLoadConfigNoTargets(t *testing.T) {
	path := writeConfig(t, "targets: []\n")
	if _, err := loadConfig(path); err == nil {
		t.Fatal("expected an error when targets is empty")
	}
}

func TestLoadConfigInvalidDuration(t *testing.T) {
	path := writeConfig(t, `
targets:
  - 1.1.1.1
ping:
  interval: not-a-duration
`)
	if _, err := loadConfig(path); err == nil {
		t.Fatal("expected an error for an invalid ping.interval")
	}
}

func TestLoadConfigAppliesDefaults(t *testing.T) {
	path := writeConfig(t, "targets:\n  - 1.1.1.1\n")

	cfg, err := loadConfig(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.DNS.Refresh.Duration() != defaultDNSRefresh {
		t.Errorf("DNS.Refresh = %v, want default %v", cfg.DNS.Refresh.Duration(), defaultDNSRefresh)
	}
	if cfg.Ping.Interval.Duration() != defaultPingInterval {
		t.Errorf("Ping.Interval = %v, want default %v", cfg.Ping.Interval.Duration(), defaultPingInterval)
	}
	if cfg.Ping.Timeout.Duration() != defaultPingTimeout {
		t.Errorf("Ping.Timeout = %v, want default %v", cfg.Ping.Timeout.Duration(), defaultPingTimeout)
	}
	if cfg.Ping.HistorySize != defaultHistorySize {
		t.Errorf("Ping.HistorySize = %d, want default %d", cfg.Ping.HistorySize, defaultHistorySize)
	}
	if cfg.Ping.Size != defaultPingSize {
		t.Errorf("Ping.Size = %d, want default %d", cfg.Ping.Size, defaultPingSize)
	}
}

func TestEnvOr(t *testing.T) {
	t.Setenv("PING_EXPORTER_TEST_VAR", "")
	if got := envOr("PING_EXPORTER_TEST_VAR", "fallback"); got != "fallback" {
		t.Errorf("envOr with unset var = %q, want %q", got, "fallback")
	}
	t.Setenv("PING_EXPORTER_TEST_VAR", "set")
	if got := envOr("PING_EXPORTER_TEST_VAR", "fallback"); got != "set" {
		t.Errorf("envOr with set var = %q, want %q", got, "set")
	}
}
