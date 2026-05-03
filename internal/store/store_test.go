package store

import (
	"path/filepath"
	"testing"
)

func TestOpenCreatesDatabase(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	s, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = s.Close() }()

	var v int
	if err := s.DB().QueryRow(`SELECT MAX(version) FROM schema_migrations`).Scan(&v); err != nil {
		t.Fatalf("query schema_migrations: %v", err)
	}
	if v < 1 {
		t.Errorf("expected migration version >= 1, got %d", v)
	}
}

func TestOpenIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	s1, err := Open(dbPath)
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	_ = s1.Close()
	s2, err := Open(dbPath)
	if err != nil {
		t.Fatalf("second Open: %v", err)
	}
	defer func() { _ = s2.Close() }()
	var n int
	if err := s2.DB().QueryRow(`SELECT COUNT(*) FROM schema_migrations`).Scan(&n); err != nil {
		t.Fatalf("count migrations: %v", err)
	}
	if n != 1 {
		t.Errorf("expected 1 migration row after re-open, got %d", n)
	}
}

func TestOpenCreatesParentDir(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "nested", "sub", "test.db")
	s, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open with nested path: %v", err)
	}
	defer func() { _ = s.Close() }()
}
