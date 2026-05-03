package store

import (
	"encoding/json"
	"fmt"
	"time"
)

// StoredSuggestion is one row of the suggestions table.
type StoredSuggestion struct {
	ID          int64
	Ts          time.Time
	Module      string
	Target      string
	Reason      string
	Evidence    map[string]string
	Severity    string // "low" | "medium" | "high"
	DismissedAt *time.Time
}

// InsertSuggestion appends a new suggestion. Returns the row ID.
func (s *Store) InsertSuggestion(sg StoredSuggestion) (int64, error) {
	if sg.Evidence == nil {
		sg.Evidence = map[string]string{}
	}
	ev, err := json.Marshal(sg.Evidence)
	if err != nil {
		return 0, err
	}
	res, err := s.db.Exec(
		`INSERT INTO suggestions(ts, module, target, reason, evidence_json, severity)
		 VALUES(?, ?, ?, ?, ?, ?)`,
		sg.Ts.UTC(), sg.Module, sg.Target, sg.Reason, string(ev), sg.Severity,
	)
	if err != nil {
		return 0, fmt.Errorf("insert suggestion: %w", err)
	}
	return res.LastInsertId()
}

// ListOpenSuggestions returns all suggestions where dismissed_at IS NULL,
// newest first.
func (s *Store) ListOpenSuggestions() ([]StoredSuggestion, error) {
	rows, err := s.db.Query(
		`SELECT id, ts, module, target, reason, evidence_json, severity, dismissed_at
		   FROM suggestions
		  WHERE dismissed_at IS NULL
		  ORDER BY ts DESC`,
	)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []StoredSuggestion
	for rows.Next() {
		var sg StoredSuggestion
		var ev string
		var dismissed *time.Time
		if err := rows.Scan(&sg.ID, &sg.Ts, &sg.Module, &sg.Target, &sg.Reason, &ev, &sg.Severity, &dismissed); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(ev), &sg.Evidence)
		sg.DismissedAt = dismissed
		out = append(out, sg)
	}
	return out, rows.Err()
}

// DismissSuggestion sets dismissed_at on the given ID.
func (s *Store) DismissSuggestion(id int64, at time.Time) error {
	_, err := s.db.Exec(
		`UPDATE suggestions SET dismissed_at = ? WHERE id = ?`,
		at.UTC(), id,
	)
	return err
}

// HasOpenSuggestion reports whether an undismissed suggestion exists for the
// given (module, target). Used to dedupe nightly inserts.
func (s *Store) HasOpenSuggestion(module, target string) (bool, error) {
	var n int
	err := s.db.QueryRow(
		`SELECT COUNT(*) FROM suggestions
		  WHERE module = ? AND target = ? AND dismissed_at IS NULL`,
		module, target,
	).Scan(&n)
	if err != nil {
		return false, err
	}
	return n > 0, nil
}
