package store

import (
	"encoding/json"
	"fmt"
	"time"
)

// StoredAction is one row of the actions table.
type StoredAction struct {
	ID        int64
	Ts        time.Time
	Module    string
	Op        string
	Target    string
	SizeBytes int64
	Evidence  map[string]string
	Outcome   string
}

// RecordAction appends one action to the audit table. Returns the new row ID.
func (s *Store) RecordAction(a StoredAction) (int64, error) {
	if a.Evidence == nil {
		a.Evidence = map[string]string{}
	}
	ev, err := json.Marshal(a.Evidence)
	if err != nil {
		return 0, fmt.Errorf("marshal evidence: %w", err)
	}
	res, err := s.db.Exec(
		`INSERT INTO actions(ts, module, op, target, size_bytes, evidence_json, outcome)
		 VALUES(?, ?, ?, ?, ?, ?, ?)`,
		a.Ts.UTC(), a.Module, a.Op, a.Target, a.SizeBytes, string(ev), a.Outcome,
	)
	if err != nil {
		return 0, fmt.Errorf("insert action: %w", err)
	}
	return res.LastInsertId()
}

// ListActions returns the most recent actions since the given time, capped at limit.
func (s *Store) ListActions(since time.Time, limit int) ([]StoredAction, error) {
	rows, err := s.db.Query(
		`SELECT id, ts, module, op, target, size_bytes, evidence_json, outcome
		   FROM actions
		  WHERE ts >= ?
		  ORDER BY ts DESC
		  LIMIT ?`,
		since.UTC(), limit,
	)
	if err != nil {
		return nil, fmt.Errorf("query actions: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []StoredAction
	for rows.Next() {
		var a StoredAction
		var ev string
		if err := rows.Scan(&a.ID, &a.Ts, &a.Module, &a.Op, &a.Target, &a.SizeBytes, &ev, &a.Outcome); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(ev), &a.Evidence)
		out = append(out, a)
	}
	return out, rows.Err()
}
