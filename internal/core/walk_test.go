package core

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDirSize(t *testing.T) {
	dir := t.TempDir()
	mustWriteFile(t, filepath.Join(dir, "a.txt"), 100)
	mustWriteFile(t, filepath.Join(dir, "b.txt"), 250)
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	mustWriteFile(t, filepath.Join(dir, "sub", "c.txt"), 50)

	got, err := DirSize(dir)
	if err != nil {
		t.Fatalf("DirSize: %v", err)
	}
	if got != 400 {
		t.Errorf("DirSize = %d, want 400", got)
	}
}

func TestDirSizeMissing(t *testing.T) {
	_, err := DirSize("/nonexistent/path/xyz")
	if err == nil {
		t.Error("expected error for missing path")
	}
}

func TestDirSizeSkipsSymlinks(t *testing.T) {
	dir := t.TempDir()
	mustWriteFile(t, filepath.Join(dir, "real.txt"), 100)
	if err := os.Symlink("/etc/hosts", filepath.Join(dir, "link")); err != nil {
		t.Skip("symlink not supported")
	}
	got, err := DirSize(dir)
	if err != nil {
		t.Fatalf("DirSize: %v", err)
	}
	if got != 100 {
		t.Errorf("DirSize = %d, want 100 (symlink should be skipped)", got)
	}
}

func mustWriteFile(t *testing.T, path string, size int) {
	t.Helper()
	data := make([]byte, size)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}
