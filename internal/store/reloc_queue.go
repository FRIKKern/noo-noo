package store

// This file owns the relocation_queue table (schema v3, charter D24): the
// deferred-offload queue that remembers relocations a stop-gate refused so
// they can run at a later, safe moment. Like auto_clean_events, the row
// types live here — internal/modules/offload consumes them through a narrow
// interface it declares itself, so offload never has to import the store's
// migration machinery into its tests.
//
// Lifecycle (two-phase, modeled on auto_clean_events):
//
//	Enqueue            -> status 'queued'            (the pivot row)
//	MarkRelocationAttempt (phase 1 of every apply)   -> attempts++, timestamp
//	ResolveRelocation  (phase 2)                     -> 'applied' | 'blocked' | 'cancelled'
//
// 'applied' and 'cancelled' are terminal. 'blocked' is NOT: a blocked row
// stays pending and the next re-check (CLI `offload run-pending` or the
// daemon's gated daily tick) re-opens it by simply trying again — resolving
// a blocked row to 'blocked' with a fresh reason is legal and idempotent.

import (
	"database/sql"
	"errors"
	"fmt"
)

// Relocation queue status values. Pending = {queued, blocked};
// terminal = {applied, cancelled}.
const (
	RelocQueued    = "queued"
	RelocApplied   = "applied"
	RelocBlocked   = "blocked"
	RelocCancelled = "cancelled"
)

// ErrRelocResolved is returned when a state change is attempted on a row
// that already reached a terminal status (applied / cancelled).
var ErrRelocResolved = errors.New("relocation queue row already resolved")

// RelocationQueueEntry is one row in relocation_queue.
type RelocationQueueEntry struct {
	ID              int64
	QueuedAtUnix    int64
	TargetPath      string
	PlaybookAssetID string
	DestRoot        string
	DestVolumeUUID  string
	// GateReason records WHY the relocation was deferred at enqueue time
	// (e.g. `stop-gate: "Claude" was running`).
	GateReason string
	Status     string
	// BlockedReason is the latest re-check refusal for a 'blocked' row.
	BlockedReason     string
	Attempts          int
	LastAttemptAtUnix int64 // 0 = never attempted
	ResolvedAtUnix    int64 // 0 = still pending
	FreedBytes        int64
}

// RelocationResolution carries the phase-2 outcome patched into a pending row.
type RelocationResolution struct {
	Status         string // 'applied' | 'blocked' | 'cancelled'
	BlockedReason  string // set when Status == 'blocked'
	FreedBytes     int64  // set when Status == 'applied'
	ResolvedAtUnix int64  // terminal timestamp; ignored for 'blocked'
}

// EnqueueRelocation inserts a queued row and returns its id. Idempotent per
// pending target: if a pending (queued/blocked) row for the same target_path
// already exists, its id is returned unchanged instead of inserting a
// duplicate — re-consenting to an already-queued relocation must not fork
// the queue. A target whose previous row reached a terminal status may be
// enqueued again (a fresh row).
func (s *Store) EnqueueRelocation(e RelocationQueueEntry) (int64, error) {
	var existing int64
	err := s.db.QueryRow(`SELECT id FROM relocation_queue
		WHERE target_path = ? AND status IN (?, ?)
		ORDER BY id LIMIT 1`,
		e.TargetPath, RelocQueued, RelocBlocked).Scan(&existing)
	switch {
	case err == nil:
		return existing, nil
	case !errors.Is(err, sql.ErrNoRows):
		return 0, fmt.Errorf("enqueue dedupe check: %w", err)
	}
	res, err := s.db.Exec(`INSERT INTO relocation_queue
		(queued_at_unix, target_path, playbook_asset_id, dest_root,
		 dest_volume_uuid, gate_reason, status)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		e.QueuedAtUnix, e.TargetPath, e.PlaybookAssetID, e.DestRoot,
		e.DestVolumeUUID, e.GateReason, RelocQueued)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// ListPendingRelocations returns every row that has not reached a terminal
// status — 'queued' rows awaiting their first check AND 'blocked' rows whose
// last re-check refused (they stay eligible; the ~/.claude case only ever
// clears after the app finally quits). Oldest first.
func (s *Store) ListPendingRelocations() ([]RelocationQueueEntry, error) {
	rows, err := s.db.Query(`SELECT id, queued_at_unix, target_path,
			playbook_asset_id, dest_root, dest_volume_uuid, gate_reason, status,
			COALESCE(blocked_reason, ''), attempts,
			COALESCE(last_attempt_at_unix, 0), COALESCE(resolved_at_unix, 0),
			freed_bytes
		FROM relocation_queue
		WHERE status IN (?, ?)
		ORDER BY queued_at_unix, id`, RelocQueued, RelocBlocked)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []RelocationQueueEntry
	for rows.Next() {
		var e RelocationQueueEntry
		if err := rows.Scan(&e.ID, &e.QueuedAtUnix, &e.TargetPath,
			&e.PlaybookAssetID, &e.DestRoot, &e.DestVolumeUUID, &e.GateReason,
			&e.Status, &e.BlockedReason, &e.Attempts, &e.LastAttemptAtUnix,
			&e.ResolvedAtUnix, &e.FreedBytes); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// MarkRelocationAttempt is phase 1 of the two-phase apply: it stamps
// last_attempt_at_unix and increments attempts on a still-pending row BEFORE
// the relocation runs, so a crash mid-apply leaves a forensic pivot (a fresh
// attempt timestamp on a row that never reached phase 2).
func (s *Store) MarkRelocationAttempt(id int64, atUnix int64) error {
	res, err := s.db.Exec(`UPDATE relocation_queue
		SET last_attempt_at_unix = ?, attempts = attempts + 1
		WHERE id = ? AND status IN (?, ?)`,
		atUnix, id, RelocQueued, RelocBlocked)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("mark attempt on row %d: %w", id, ErrRelocResolved)
	}
	return nil
}

// ResolveRelocation is phase 2: it patches a pending row with its outcome.
// Legal target statuses are 'applied', 'blocked', and 'cancelled'. Only
// pending rows may be resolved — a terminal row refuses further transitions
// (ErrRelocResolved). Resolving a blocked row to 'blocked' again with a
// fresh reason is legal (idempotent re-open: blocked rows stay pending).
func (s *Store) ResolveRelocation(id int64, r RelocationResolution) error {
	switch r.Status {
	case RelocApplied, RelocBlocked, RelocCancelled:
	default:
		return fmt.Errorf("resolve relocation %d: illegal status %q", id, r.Status)
	}
	var blockedReason any
	var resolvedAt any
	if r.Status == RelocBlocked {
		blockedReason = r.BlockedReason
		resolvedAt = nil // blocked rows are still pending, not resolved
	} else {
		if r.BlockedReason != "" {
			blockedReason = r.BlockedReason
		}
		resolvedAt = nullableUnix(r.ResolvedAtUnix)
	}
	res, err := s.db.Exec(`UPDATE relocation_queue
		SET status = ?, blocked_reason = ?, resolved_at_unix = ?, freed_bytes = ?
		WHERE id = ? AND status IN (?, ?)`,
		r.Status, blockedReason, resolvedAt, r.FreedBytes,
		id, RelocQueued, RelocBlocked)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("resolve relocation row %d to %q: %w", id, r.Status, ErrRelocResolved)
	}
	return nil
}

// GetRelocation returns one row by id, pending or terminal.
func (s *Store) GetRelocation(id int64) (RelocationQueueEntry, error) {
	var e RelocationQueueEntry
	err := s.db.QueryRow(`SELECT id, queued_at_unix, target_path,
			playbook_asset_id, dest_root, dest_volume_uuid, gate_reason, status,
			COALESCE(blocked_reason, ''), attempts,
			COALESCE(last_attempt_at_unix, 0), COALESCE(resolved_at_unix, 0),
			freed_bytes
		FROM relocation_queue WHERE id = ?`, id).Scan(
		&e.ID, &e.QueuedAtUnix, &e.TargetPath, &e.PlaybookAssetID, &e.DestRoot,
		&e.DestVolumeUUID, &e.GateReason, &e.Status, &e.BlockedReason,
		&e.Attempts, &e.LastAttemptAtUnix, &e.ResolvedAtUnix, &e.FreedBytes)
	if err != nil {
		return RelocationQueueEntry{}, err
	}
	return e, nil
}
