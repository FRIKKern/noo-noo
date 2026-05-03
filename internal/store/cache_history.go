package store

import (
	"fmt"
	"time"
)

// CacheSample is one row of cache_size_history.
type CacheSample struct {
	TargetPath string
	Bytes      int64
	At         time.Time
}

// RecordCacheSize appends a sample to cache_size_history.
func (s *Store) RecordCacheSize(target string, bytes int64, at time.Time) error {
	_, err := s.db.Exec(
		`INSERT INTO cache_size_history(target_path, bytes, recorded_at) VALUES(?, ?, ?)`,
		target, bytes, at.UTC(),
	)
	if err != nil {
		return fmt.Errorf("insert cache_size_history: %w", err)
	}
	return nil
}

// CacheSizeSeries returns all samples for target with recorded_at >= since,
// in chronological order.
func (s *Store) CacheSizeSeries(target string, since time.Time) ([]CacheSample, error) {
	rows, err := s.db.Query(
		`SELECT target_path, bytes, recorded_at
		   FROM cache_size_history
		  WHERE target_path = ? AND recorded_at >= ?
		  ORDER BY recorded_at ASC`,
		target, since.UTC(),
	)
	if err != nil {
		return nil, fmt.Errorf("query cache_size_history: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []CacheSample
	for rows.Next() {
		var c CacheSample
		if err := rows.Scan(&c.TargetPath, &c.Bytes, &c.At); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
