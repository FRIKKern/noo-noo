// Package config loads the noo-noo daemon configuration from a TOML file,
// merging it on top of compiled-in defaults. All fields are optional; missing
// keys keep their default value.
package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	toml "github.com/pelletier/go-toml/v2"
)

type Config struct {
	Daemon     DaemonCfg     `toml:"daemon"`
	Heuristics HeuristicsCfg `toml:"heuristics"`
	Notify     NotifyCfg     `toml:"notify"`
	Scan       ScanCfg       `toml:"scan"`
	Pressure   PressureCfg   `toml:"pressure"`
	AutoClean  AutoCleanCfg  `toml:"auto_clean"`
}

// PressureCfg controls the real-time pressure watcher (Phase 0.5). Sampling
// happens every SampleIntervalSeconds; a trigger fires only after sustained
// breach for DebounceSeconds. Memory is "high" if used/total >= MemHighRatio;
// disk is "high" if free space (GB) <= DiskLowGB.
type PressureCfg struct {
	SampleIntervalSeconds int     `toml:"sample_interval_seconds"`
	DebounceSeconds       int     `toml:"debounce_seconds"`
	MemHighRatio          float64 `toml:"mem_high_ratio"`
	DiskLowGB             int     `toml:"disk_low_gb"`
}

// AutoCleanCfg controls the opt-in cleanup engine (Phase 0.5).
// Default-disabled. First enable requires the CLI to set RiskAcknowledgedAt;
// the daemon refuses to act if Enabled=true but RiskAcknowledgedAt is empty.
type AutoCleanCfg struct {
	Enabled            bool     `toml:"enabled"`
	ModulesAllowed     []string `toml:"modules_allowed"`
	MinIdleDays        int      `toml:"min_idle_days"`
	MinSizeMB          int      `toml:"min_size_mb"`
	SizeCapPerTickGB   int      `toml:"size_cap_per_tick_gb"`
	RiskAcknowledgedAt string   `toml:"risk_acknowledged_at"`
}

type DaemonCfg struct {
	ScanHour   int    `toml:"scan_hour"`
	SocketPath string `toml:"socket_path"`
	StorePath  string `toml:"store_path"`
}

type HeuristicsCfg struct {
	IdleRepos     IdleReposCfg     `toml:"idle_repos"`
	CacheVelocity CacheVelocityCfg `toml:"cache_velocity"`
}

type IdleReposCfg struct {
	Enabled             bool  `toml:"enabled"`
	MinIdleDays         int   `toml:"min_idle_days"`
	MinNodeModulesBytes int64 `toml:"min_node_modules_bytes"`
}

type CacheVelocityCfg struct {
	Enabled          bool    `toml:"enabled"`
	GrowthMultiplier float64 `toml:"growth_multiplier"`
	WindowDays       int     `toml:"window_days"`
}

type NotifyCfg struct {
	Enabled     bool   `toml:"enabled"`
	MinSeverity string `toml:"min_severity"`
}

type ScanCfg struct {
	Roots []string `toml:"roots"`
}

// Defaults returns a Config populated with the compiled-in default values.
func Defaults() Config {
	home, _ := os.UserHomeDir()
	return Config{
		Daemon: DaemonCfg{
			ScanHour:   3,
			SocketPath: filepath.Join(home, "Library", "Application Support", "noo-noo", "noo-noo.sock"),
			StorePath:  filepath.Join(home, "Library", "Application Support", "noo-noo", "store.db"),
		},
		Heuristics: HeuristicsCfg{
			IdleRepos: IdleReposCfg{
				Enabled:             true,
				MinIdleDays:         30,
				MinNodeModulesBytes: 524_288_000,
			},
			CacheVelocity: CacheVelocityCfg{
				Enabled:          true,
				GrowthMultiplier: 2.0,
				WindowDays:       7,
			},
		},
		Notify: NotifyCfg{
			Enabled:     true,
			MinSeverity: "medium",
		},
		Scan: ScanCfg{
			Roots: []string{filepath.Join(home, "Documents", "GitHub")},
		},
		Pressure: PressureCfg{
			SampleIntervalSeconds: 15,
			DebounceSeconds:       60,
			MemHighRatio:          0.85,
			DiskLowGB:             10,
		},
		AutoClean: AutoCleanCfg{
			Enabled:          false,
			ModulesAllowed:   []string{"dev"},
			MinIdleDays:      90,
			MinSizeMB:        1024,
			SizeCapPerTickGB: 10,
			// RiskAcknowledgedAt deliberately empty; CLI sets on first enable.
		},
	}
}

// Load reads the config from path, overlaying user values on top of Defaults.
// A missing file is not an error — defaults are returned.
func Load(path string) (Config, error) {
	cfg := Defaults()
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return cfg, nil
		}
		return cfg, fmt.Errorf("read %q: %w", path, err)
	}
	if err := toml.Unmarshal(data, &cfg); err != nil {
		return cfg, fmt.Errorf("parse %q: %w", path, err)
	}
	cfg.Daemon.SocketPath = expandTilde(cfg.Daemon.SocketPath)
	cfg.Daemon.StorePath = expandTilde(cfg.Daemon.StorePath)
	for i, r := range cfg.Scan.Roots {
		cfg.Scan.Roots[i] = expandTilde(r)
	}
	return cfg, nil
}

func expandTilde(p string) string {
	if !strings.HasPrefix(p, "~/") {
		return p
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, strings.TrimPrefix(p, "~/"))
}
