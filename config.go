package main

import (
	"fmt"
	"os"
	"time"

	"go.yaml.in/yaml/v3"
)

// Config mirrors czerwonk/ping_exporter's config.yml schema exactly (targets,
// dns, ping, options), so this exporter is a drop-in replacement: same file,
// no changes needed. See PING_EXPORTER_REQUIREMENTS.md section 3.
type Config struct {
	Targets []string      `yaml:"targets"`
	DNS     DNSConfig     `yaml:"dns"`
	Ping    PingConfig    `yaml:"ping"`
	Options OptionsConfig `yaml:"options"`
}

type DNSConfig struct {
	Refresh    Duration `yaml:"refresh"`
	Nameserver string   `yaml:"nameserver"`
}

type PingConfig struct {
	Interval    Duration `yaml:"interval"`
	Timeout     Duration `yaml:"timeout"`
	HistorySize int      `yaml:"history-size"`
	Size        int      `yaml:"size"`
}

type OptionsConfig struct {
	DisableIPv6 bool `yaml:"disableIPv6"`
}

// Duration parses YAML's plain duration strings ("2m15s") into a
// time.Duration; the stdlib time.Duration has no UnmarshalYAML of its own.
type Duration time.Duration

func (d *Duration) UnmarshalYAML(value *yaml.Node) error {
	var s string
	if err := value.Decode(&s); err != nil {
		return err
	}
	parsed, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("invalid duration %q: %w", s, err)
	}
	*d = Duration(parsed)
	return nil
}

func (d Duration) Duration() time.Duration { return time.Duration(d) }

// Defaults applied to whatever the config file leaves unset. The live
// deployment's config.yml (see requirements doc) sets every one of these
// explicitly, so these only matter for a partial/minimal config.
const (
	defaultDNSRefresh   = 5 * time.Minute
	defaultPingInterval = 2 * time.Second
	defaultPingTimeout  = 3 * time.Second
	defaultHistorySize  = 50
	defaultPingSize     = 56
	defaultListenAddr   = ":9427"
	defaultConfigFile   = "/config/config.yml"
)

// loadConfig reads and validates the YAML config file at path. It fails
// fast (non-nil error) on bad YAML or an empty target list, so the caller
// can exit non-zero at startup rather than run with nothing to probe.
func loadConfig(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("reading config: %w", err)
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return Config{}, fmt.Errorf("parsing config: %w", err)
	}

	if len(cfg.Targets) == 0 {
		return Config{}, fmt.Errorf("no targets configured")
	}

	if cfg.DNS.Refresh <= 0 {
		cfg.DNS.Refresh = Duration(defaultDNSRefresh)
	}
	if cfg.Ping.Interval <= 0 {
		cfg.Ping.Interval = Duration(defaultPingInterval)
	}
	if cfg.Ping.Timeout <= 0 {
		cfg.Ping.Timeout = Duration(defaultPingTimeout)
	}
	if cfg.Ping.HistorySize <= 0 {
		cfg.Ping.HistorySize = defaultHistorySize
	}
	if cfg.Ping.Size <= 0 {
		cfg.Ping.Size = defaultPingSize
	}

	return cfg, nil
}

func envOr(name, def string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return def
}
