package store

// This file owns the auto_clean_events audit table: the row-shape structs and
// the Store methods that write and read them. The autoclean engine drives the
// writes through the internal/autoclean.EventStore interface (which aliases
// AutoCleanEvent / AutoCleanEventUpdate to the types below), and the IPC
// AutoCleanService reads the 7-day rollup through AutoCleanStatsSince. Keeping
// the types here — rather than in internal/autoclean — is deliberate: store
// cannot import autoclean (that would cycle through heuristics), but autoclean
// already depends on store, so store is the natural owner of the row shape.

// AutoCleanEvent is one row in the auto_clean_events table. The autoclean
// engine writes one of these BEFORE the os.RemoveAll (outcome=in_progress)
// and updates it after (outcome=deleted | skipped | errored). The pre-row is
// the crash pivot: a daemon crash mid-delete still leaves a forensic trail.
type AutoCleanEvent struct {
	StartedAtUnix      int64
	EndedAtUnix        int64
	Trigger            string // 'daily' | 'manual'
	Outcome            string // 'in_progress' | 'deleted' | 'skipped' | 'errored'
	SkipReason         string
	TargetPath         string
	Module             string
	TargetSizeBytes    int64
	FreedBytes         int64
	IdleDaysAtDecision int
	SuggestionID       string
	ErrorMsg           string
}

// AutoCleanEventUpdate carries the post-delete fields written into an
// already-recorded row.
type AutoCleanEventUpdate struct {
	EndedAtUnix int64
	Outcome     string
	FreedBytes  int64
	ErrorMsg    string
}

// RecordAutoCleanEvent inserts a new audit row and returns its id. A zero
// EndedAtUnix is stored as NULL (the row is still in flight); callers pass a
// non-zero value only for terminal rows written in a single shot (e.g. an
// up-front safety skip).
func (s *Store) RecordAutoCleanEvent(e AutoCleanEvent) (int64, error) {
	res, err := s.db.Exec(`INSERT INTO auto_clean_events
		(started_at_unix, ended_at_unix, trigger, outcome, skip_reason, target_path, module,
		 target_size_bytes, freed_bytes, idle_days_at_decision, suggestion_id, error_msg)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		e.StartedAtUnix, nullableUnix(e.EndedAtUnix), e.Trigger, e.Outcome, e.SkipReason,
		e.TargetPath, e.Module, e.TargetSizeBytes, e.FreedBytes, e.IdleDaysAtDecision,
		e.SuggestionID, e.ErrorMsg)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// UpdateAutoCleanEvent patches the in-progress row (id from
// RecordAutoCleanEvent) with its terminal outcome and freed-bytes tally.
func (s *Store) UpdateAutoCleanEvent(id int64, u AutoCleanEventUpdate) error {
	_, err := s.db.Exec(`UPDATE auto_clean_events
		SET ended_at_unix = ?, outcome = ?, freed_bytes = ?, error_msg = ?
		WHERE id = ?`,
		u.EndedAtUnix, u.Outcome, u.FreedBytes, u.ErrorMsg, id)
	return err
}

// AutoCleanStatsSince returns (count, freedBytes) across successful 'deleted'
// rows whose started_at_unix >= sinceUnix. It is the read side of the audit
// ledger that the IPC AutoClean.Status method surfaces as the 7-day rollup.
// In-progress / skipped / errored rows never contribute freed bytes.
func (s *Store) AutoCleanStatsSince(sinceUnix int64) (int, int64, error) {
	var count int
	var freed int64
	err := s.db.QueryRow(`SELECT COUNT(*), COALESCE(SUM(freed_bytes), 0)
		FROM auto_clean_events
		WHERE outcome = 'deleted' AND started_at_unix >= ?`, sinceUnix).Scan(&count, &freed)
	if err != nil {
		return 0, 0, err
	}
	return count, freed, nil
}

// nullableUnix maps a zero timestamp to a SQL NULL so an in-flight row's
// ended_at_unix is genuinely absent rather than the epoch.
func nullableUnix(v int64) any {
	if v == 0 {
		return nil
	}
	return v
}
