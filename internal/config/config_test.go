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
