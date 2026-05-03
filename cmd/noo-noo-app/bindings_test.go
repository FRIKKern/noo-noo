package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestBindings_Config_GetUsesPath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, []byte("[daemon]\nscan_hour = 9\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	b := &Bindings{configPath: path}
	cfg, err := b.GetConfig()
	if err != nil {
		t.Fatalf("GetConfig: %v", err)
	}
	if cfg.Daemon.ScanHour != 9 {
		t.Errorf("ScanHour = %d, want 9", cfg.Daemon.ScanHour)
	}
}

func TestBindings_Config_GetReturnsDefaultsWhenMissing(t *testing.T) {
	dir := t.TempDir()
	b := &Bindings{configPath: filepath.Join(dir, "nope.toml")}
	cfg, err := b.GetConfig()
	if err != nil {
		t.Fatalf("missing file should not error: %v", err)
	}
	if cfg.Daemon.ScanHour != 3 {
		t.Errorf("default ScanHour = %d, want 3", cfg.Daemon.ScanHour)
	}
}

func TestBindings_Save_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	b := &Bindings{configPath: filepath.Join(dir, "config.toml")}

	cfg, _ := b.GetConfig()
	cfg.Daemon.ScanHour = 11
	cfg.Heuristics.IdleRepos.MinIdleDays = 90
	if err := b.SaveConfig(cfg); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}

	got, err := b.GetConfig()
	if err != nil {
		t.Fatalf("GetConfig: %v", err)
	}
	if got.Daemon.ScanHour != 11 {
		t.Errorf("ScanHour = %d, want 11", got.Daemon.ScanHour)
	}
	if got.Heuristics.IdleRepos.MinIdleDays != 90 {
		t.Errorf("MinIdleDays = %d, want 90", got.Heuristics.IdleRepos.MinIdleDays)
	}
}

func TestBindings_Save_AtomicNoTemp(t *testing.T) {
	dir := t.TempDir()
	b := &Bindings{configPath: filepath.Join(dir, "config.toml")}
	cfg, _ := b.GetConfig()
	if err := b.SaveConfig(cfg); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(b.configPath + ".tmp"); !os.IsNotExist(err) {
		t.Errorf(".tmp file lingered after save: %v", err)
	}
}
