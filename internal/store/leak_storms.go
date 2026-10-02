package store

import (
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// LastLeakStormAlert returns when the daemon last fired a leak-storm
// notification for signature. ok=false means never.
func (s *Store) LastLeakStormAlert(signature string) (at time.Time, ok bool, err error) {
	err = s.db.QueryRow(
		`SELECT fired_at FROM leak_storm_alerts WHERE signature = ?`, signature,
	).Scan(&at)
	if errors.Is(err, sql.ErrNoRows) {
		return time.Time{}, false, nil
	}
	if err != nil {
		return time.Time{}, false, fmt.Errorf("read leak storm alert: %w", err)
	}
	return at, true, nil
}

// RecordLeakStormAlert upserts the per-signature ledger row: the alert for
// signature fired at `at` after counting `count` new instances.
func (s *Store) RecordLeakStormAlert(signature string, at time.Time, count int) error {
	_, err := s.db.Exec(
		`INSERT INTO leak_storm_alerts(signature, fired_at, count) VALUES(?, ?, ?)
		 ON CONFLICT(signature) DO UPDATE SET fired_at = excluded.fired_at, count = excluded.count`,
		signature, at.UTC(), count,
	)
	if err != nil {
		return fmt.Errorf("record leak storm alert: %w", err)
	}
	return nil
}
