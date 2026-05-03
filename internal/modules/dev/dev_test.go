package dev

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/frikkjarl/noo-noo/internal/core"
)

func TestScanFindsArtifacts(t *testing.T) {
	root := t.TempDir()
	mustMkdir(t, filepath.Join(root, "repo-a", "node_modules"))
	mustWrite(t, filepath.Join(root, "repo-a", "node_modules", "x.js"), 1000)
	mustMkdir(t, filepath.Join(root, "repo-a", ".next"))
	mustWrite(t, filepath.Join(root, "repo-a", ".next", "y.json"), 500)
	mustMkdir(t, filepath.Join(root, "repo-b", "node_modules"))
	mustWrite(t, filepath.Join(root, "repo-b", "node_modules", "z.js"), 200)

	safety := core.NewSafety([]string{root}, []string{".git"})
	m := New([]string{root}, safety)
	rep, err := m.Scan(context.Background())
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(rep.Items) != 3 {
		t.Errorf("expected 3 items, got %d: %+v", len(rep.Items), rep.Items)
	}
	if rep.Total != 1700 {
		t.Errorf("expected total 1700, got %d", int64(rep.Total))
	}
}

func TestScanPrunesNested(t *testing.T) {
	root := t.TempDir()
	// node_modules with a node_modules inside should not be double-counted.
	outer := filepath.Join(root, "repo", "node_modules")
	mustMkdir(t, outer)
	mustWrite(t, filepath.Join(outer, "outer.js"), 100)
	mustMkdir(t, filepath.Join(outer, "pkg", "node_modules"))
	mustWrite(t, filepath.Join(outer, "pkg", "node_modules", "inner.js"), 200)

	safety := core.NewSafety([]string{root}, nil)
	m := New([]string{root}, safety)
	rep, _ := m.Scan(context.Background())
	if len(rep.Items) != 1 {
		t.Errorf("expected 1 item (nested pruned), got %d", len(rep.Items))
	}
	if rep.Total != 300 {
		t.Errorf("expected 300 (sum of both files via outer), got %d", int64(rep.Total))
	}
}

func TestScanIgnoresUnknownDirs(t *testing.T) {
	root := t.TempDir()
	mustMkdir(t, filepath.Join(root, "repo", "src"))
	mustWrite(t, filepath.Join(root, "repo", "src", "a.go"), 100)

	safety := core.NewSafety([]string{root}, nil)
	m := New([]string{root}, safety)
	rep, _ := m.Scan(context.Background())
	if len(rep.Items) != 0 {
		t.Errorf("expected 0 items, got %d", len(rep.Items))
	}
}

func mustMkdir(t *testing.T, p string) {
	t.Helper()
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatal(err)
	}
}

func mustWrite(t *testing.T, p string, size int) {
	t.Helper()
	if err := os.WriteFile(p, make([]byte, size), 0o644); err != nil {
		t.Fatal(err)
	}
}
