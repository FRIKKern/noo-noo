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
