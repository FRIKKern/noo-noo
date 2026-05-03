package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDefaultsWhenFileMissing(t *testing.T) {
	dir := t.TempDir()
	cfg, err := Load(filepath.Join(dir, "nope.toml"))
	if err != nil {
		t.Fatalf("Load missing file should not error, got %v", err)
	}
	if cfg.Daemon.ScanHour != 3 {
		t.Errorf("default ScanHour = %d, want 3", cfg.Daemon.ScanHour)
	}
	if !cfg.Heuristics.IdleRepos.Enabled {
		t.Errorf("default IdleRepos.Enabled should be true")
	}
	if cfg.Heuristics.IdleRepos.MinIdleDays != 30 {
		t.Errorf("default MinIdleDays = %d, want 30", cfg.Heuristics.IdleRepos.MinIdleDays)
	}
	if cfg.Heuristics.CacheVelocity.GrowthMultiplier != 2.0 {
		t.Errorf("default GrowthMultiplier = %v, want 2.0", cfg.Heuristics.CacheVelocity.GrowthMultiplier)
	}
	if cfg.Notify.MinSeverity != "medium" {
		t.Errorf("default MinSeverity = %q, want medium", cfg.Notify.MinSeverity)
	}
}

func TestUserOverridesDefaults(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	body := []byte(`
[daemon]
scan_hour = 7

[heuristics.idle_repos]
min_idle_days = 60
`)
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Daemon.ScanHour != 7 {
		t.Errorf("ScanHour = %d, want 7", cfg.Daemon.ScanHour)
	}
	if cfg.Heuristics.IdleRepos.MinIdleDays != 60 {
		t.Errorf("MinIdleDays = %d, want 60", cfg.Heuristics.IdleRepos.MinIdleDays)
	}
	// Untouched key keeps default.
	if cfg.Heuristics.IdleRepos.MinNodeModulesBytes != 524288000 {
		t.Errorf("MinNodeModulesBytes = %d, want default 524288000", cfg.Heuristics.IdleRepos.MinNodeModulesBytes)
	}
}

func TestExpandTilde(t *testing.T) {
	t.Setenv("HOME", "/tmp/fakehome")
	got := expandTilde("~/foo/bar")
	if got != "/tmp/fakehome/foo/bar" {
		t.Errorf("expandTilde = %q", got)
	}
}

func TestPressureDefaults(t *testing.T) {
	dir := t.TempDir()
	cfg, err := Load(filepath.Join(dir, "nope.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Pressure.SampleIntervalSeconds != 15 {
		t.Errorf("default sample interval = %d, want 15", cfg.Pressure.SampleIntervalSeconds)
	}
	if cfg.Pressure.DebounceSeconds != 60 {
		t.Errorf("default debounce = %d, want 60", cfg.Pressure.DebounceSeconds)
	}
	if cfg.Pressure.MemHighRatio != 0.85 {
		t.Errorf("default mem ratio = %v, want 0.85", cfg.Pressure.MemHighRatio)
	}
	if cfg.Pressure.DiskLowGB != 10 {
		t.Errorf("default disk_low_gb = %d, want 10", cfg.Pressure.DiskLowGB)
	}
}

func TestAutoCleanDefaults(t *testing.T) {
	dir := t.TempDir()
	cfg, err := Load(filepath.Join(dir, "nope.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AutoClean.Enabled {
		t.Fatal("auto-clean must default to disabled")
	}
	if len(cfg.AutoClean.ModulesAllowed) != 1 || cfg.AutoClean.ModulesAllowed[0] != "dev" {
		t.Errorf("default modules_allowed = %v, want [dev]", cfg.AutoClean.ModulesAllowed)
	}
	if cfg.AutoClean.MinIdleDays != 90 {
		t.Errorf("default min_idle_days = %d, want 90", cfg.AutoClean.MinIdleDays)
	}
	if cfg.AutoClean.MinSizeMB != 1024 {
		t.Errorf("default min_size_mb = %d, want 1024", cfg.AutoClean.MinSizeMB)
	}
	if cfg.AutoClean.SizeCapPerTickGB != 10 {
		t.Errorf("default size_cap_per_tick_gb = %d, want 10", cfg.AutoClean.SizeCapPerTickGB)
	}
	if cfg.AutoClean.RiskAcknowledgedAt != "" {
		t.Errorf("RiskAcknowledgedAt must default empty (CLI sets on enable)")
	}
}

func TestAutoCleanRiskAckRequired(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	body := []byte(`
[auto_clean]
enabled = true
`)
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.AutoClean.Enabled {
		t.Fatal("parse should not strip Enabled")
	}
	if cfg.AutoClean.RiskAcknowledgedAt != "" {
		t.Errorf("risk_ack should be empty until CLI sets it")
	}
	// Defaults should still be applied for unset fields.
	if cfg.AutoClean.MinIdleDays != 90 {
		t.Errorf("MinIdleDays should default to 90 when unset, got %d", cfg.AutoClean.MinIdleDays)
	}
	// The daemon enforces "enabled but no ack => refuse" in autoclean
	// EvaluateSuggestion (gate 1a); covered in autoclean tests.
}

func TestPressureUserOverride(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	body := []byte(`
[pressure]
mem_high_ratio = 0.95
`)
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Pressure.MemHighRatio != 0.95 {
		t.Errorf("override MemHighRatio = %v, want 0.95", cfg.Pressure.MemHighRatio)
	}
	// Untouched key keeps default.
	if cfg.Pressure.DebounceSeconds != 60 {
		t.Errorf("DebounceSeconds = %d, want default 60", cfg.Pressure.DebounceSeconds)
	}
}
