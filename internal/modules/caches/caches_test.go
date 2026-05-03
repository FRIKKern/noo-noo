package caches

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/frikkjarl/noo-noo/internal/core"
	"github.com/frikkjarl/noo-noo/internal/modules"
)

func TestScanReportsExistingTargets(t *testing.T) {
	home := t.TempDir()
	yarn := filepath.Join(home, "Library", "Caches", "Yarn")
	pnpm := filepath.Join(home, "Library", "Caches", "pnpm")
	if err := os.MkdirAll(yarn, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(pnpm, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(yarn, "a"), make([]byte, 500), 0o644); err != nil {
		t.Fatal(err)
	}

	safety := core.NewSafety([]string{filepath.Join(home, "Library", "Caches")}, nil)
	m := New([]string{yarn, pnpm, filepath.Join(home, "missing")}, safety)
	rep, err := m.Scan(context.Background())
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(rep.Items) != 2 {
		t.Errorf("expected 2 items (Yarn + pnpm), got %d", len(rep.Items))
	}
	if rep.Total != 500 {
		t.Errorf("expected total 500, got %d", int64(rep.Total))
	}
}

func TestApplyClearsContents(t *testing.T) {
	home := t.TempDir()
	cache := filepath.Join(home, "Library", "Caches", "X")
	if err := os.MkdirAll(filepath.Join(cache, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cache, "sub", "f"), make([]byte, 100), 0o644); err != nil {
		t.Fatal(err)
	}

	safety := core.NewSafety([]string{filepath.Join(home, "Library", "Caches")}, nil)
	m := New([]string{cache}, safety)
	res, err := m.Apply(context.Background(), modules.Action{
		Module: "caches", Op: "clear", Target: cache,
	})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if res.BytesFreed != 100 {
		t.Errorf("BytesFreed = %d, want 100", int64(res.BytesFreed))
	}
	// directory itself should still exist
	if _, err := os.Stat(cache); err != nil {
		t.Errorf("cache dir should still exist: %v", err)
	}
	// contents should be gone
	entries, _ := os.ReadDir(cache)
	if len(entries) != 0 {
		t.Errorf("cache should be empty, got %d entries", len(entries))
	}
}
