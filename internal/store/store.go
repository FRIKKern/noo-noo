// Package store wraps the noo-noo SQLite database. It owns the schema
// (embedded as schema.sql), applies migrations on Open, and exposes a thin
// *sql.DB to subpackages that implement table-specific queries.
package store

import (
	"database/sql"
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

//go:embed schema.sql
var schemaSQL string

// currentSchemaVersion is the version applied by schema.sql. Bump when adding
// new migration files; schema.sql then becomes the union of all of them.
const currentSchemaVersion = 1

// Store is the noo-noo SQLite handle. Safe for concurrent use; under the hood
// modernc.org/sqlite serializes writes via a single connection by default.
type Store struct {
	db *sql.DB
}

// Open opens (or creates) the SQLite database at path. Parent directories are
// created if missing. Migrations are applied idempotently.
func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("mkdir parent: %w", err)
	}
	db, err := sql.Open("sqlite", path+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping: %w", err)
	}
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

// DB exposes the underlying *sql.DB. Subpackages use this to issue queries.
func (s *Store) DB() *sql.DB { return s.db }

// Close flushes and closes the underlying handle.
func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

func (s *Store) migrate() error {
	// Bootstrap: apply schema.sql once. It contains CREATE IF NOT EXISTS
	// statements so re-running is harmless, but we still gate on
	// schema_migrations to avoid the work.
	if _, err := s.db.Exec(schemaSQL); err != nil {
		return fmt.Errorf("apply schema: %w", err)
	}
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE version = ?`, currentSchemaVersion).Scan(&n); err != nil {
		return fmt.Errorf("check migration version: %w", err)
	}
	if n == 0 {
		if _, err := s.db.Exec(`INSERT INTO schema_migrations(version, applied_at) VALUES(?, ?)`,
			currentSchemaVersion, time.Now().UTC()); err != nil {
			return fmt.Errorf("record migration: %w", err)
		}
	}
	return nil
}
