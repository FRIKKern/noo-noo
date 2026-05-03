package store

import (
	"fmt"
	"time"
)

// RepoIdleness is one row of repo_idleness.
type RepoIdleness struct {
	Path             string
	LastCommitAt     time.Time
	NodeModulesBytes int64
	LastScanAt       time.Time
}

// UpsertRepoIdleness inserts or updates the row keyed on Path.
func (s *Store) UpsertRepoIdleness(r RepoIdleness) error {
	_, err := s.db.Exec(
		`INSERT INTO repo_idleness(path, last_commit_at, node_modules_bytes, last_scan_at)
		 VALUES(?, ?, ?, ?)
		 ON CONFLICT(path) DO UPDATE SET
		   last_commit_at = excluded.last_commit_at,
		   node_modules_bytes = excluded.node_modules_bytes,
		   last_scan_at = excluded.last_scan_at`,
		r.Path, r.LastCommitAt.UTC(), r.NodeModulesBytes, r.LastScanAt.UTC(),
	)
	if err != nil {
		return fmt.Errorf("upsert repo_idleness: %w", err)
	}
	return nil
}

// ListIdleRepos returns repos whose last_commit_at is at least minIdleDays
// before now AND whose node_modules_bytes is at least minBytes.
func (s *Store) ListIdleRepos(minIdleDays int, minBytes int64, now time.Time) ([]RepoIdleness, error) {
	cutoff := now.UTC().AddDate(0, 0, -minIdleDays)
	rows, err := s.db.Query(
		`SELECT path, last_commit_at, node_modules_bytes, last_scan_at
		   FROM repo_idleness
		  WHERE last_commit_at IS NOT NULL
		    AND last_commit_at <= ?
		    AND node_modules_bytes >= ?
		  ORDER BY node_modules_bytes DESC`,
		cutoff, minBytes,
	)
	if err != nil {
		return nil, fmt.Errorf("query repo_idleness: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []RepoIdleness
	for rows.Next() {
		var r RepoIdleness
		if err := rows.Scan(&r.Path, &r.LastCommitAt, &r.NodeModulesBytes, &r.LastScanAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
