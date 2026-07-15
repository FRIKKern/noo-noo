package store

import (
	"path/filepath"
	"testing"
)

func openTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// TestAutoCleanTableInSchema proves the DDL folded into schema.sql actually
// creates auto_clean_events on a fresh Open — no external migration runner.
func TestAutoCleanTableInSchema(t *testing.T) {
	s := openTestStore(t)
	var name string
	err := s.DB().QueryRow(
		`SELECT name FROM sqlite_master WHERE type='table' AND name='auto_clean_events'`,
	).Scan(&name)
	if err != nil {
		t.Fatalf("auto_clean_events table missing from schema.sql: %v", err)
	}
	if name != "auto_clean_events" {
		t.Fatalf("got table %q, want auto_clean_events", name)
	}
}

// TestRecordThenUpdate walks the crash-safe write pattern: an in_progress row
// written first, then upgraded to deleted with freed bytes.
func TestRecordThenUpdate(t *testing.T) {
	s := openTestStore(t)
	id, err := s.RecordAutoCleanEvent(AutoCleanEvent{
		StartedAtUnix:      1000,
		Trigger:            "daily",
		Outcome:            "in_progress",
		TargetPath:         "/repo/node_modules",
		Module:             "dev",
		TargetSizeBytes:    2048,
		IdleDaysAtDecision: 200,
		SuggestionID:       "7",
	})
	if err != nil {
		t.Fatalf("RecordAutoCleanEvent: %v", err)
	}
	if id <= 0 {
		t.Fatalf("want positive row id, got %d", id)
	}

	// The in-flight row must have a NULL ended_at_unix, not the epoch.
	var ended any
	if err := s.DB().QueryRow(
		`SELECT ended_at_unix FROM auto_clean_events WHERE id = ?`, id,
	).Scan(&ended); err != nil {
		t.Fatalf("scan ended_at_unix: %v", err)
	}
	if ended != nil {
		t.Errorf("in_progress row ended_at_unix = %v, want NULL", ended)
	}

	if err := s.UpdateAutoCleanEvent(id, AutoCleanEventUpdate{
		EndedAtUnix: 1005,
		Outcome:     "deleted",
		FreedBytes:  2048,
	}); err != nil {
		t.Fatalf("UpdateAutoCleanEvent: %v", err)
	}

	var outcome string
	var freed int64
	if err := s.DB().QueryRow(
		`SELECT outcome, freed_bytes FROM auto_clean_events WHERE id = ?`, id,
	).Scan(&outcome, &freed); err != nil {
		t.Fatalf("scan after update: %v", err)
	}
	if outcome != "deleted" || freed != 2048 {
		t.Errorf("after update outcome=%q freed=%d, want deleted/2048", outcome, freed)
	}
}

// TestAutoCleanStatsSince proves the rollup counts only 'deleted' rows at or
// after the cutoff and sums their freed bytes — the exact contract the IPC
// AutoClean.Status 7-day window depends on.
func TestAutoCleanStatsSince(t *testing.T) {
	s := openTestStore(t)

	mustRecord := func(e AutoCleanEvent) {
		t.Helper()
		if _, err := s.RecordAutoCleanEvent(e); err != nil {
			t.Fatalf("record: %v", err)
		}
	}
	// Two deleted rows inside the window.
	mustRecord(AutoCleanEvent{StartedAtUnix: 2000, Trigger: "daily", Outcome: "deleted",
		TargetPath: "/a", Module: "dev", TargetSizeBytes: 100, FreedBytes: 100, SuggestionID: "1"})
	mustRecord(AutoCleanEvent{StartedAtUnix: 3000, Trigger: "daily", Outcome: "deleted",
		TargetPath: "/b", Module: "dev", TargetSizeBytes: 250, FreedBytes: 250, SuggestionID: "2"})
	// A deleted row BEFORE the cutoff — excluded.
	mustRecord(AutoCleanEvent{StartedAtUnix: 500, Trigger: "daily", Outcome: "deleted",
		TargetPath: "/old", Module: "dev", TargetSizeBytes: 999, FreedBytes: 999, SuggestionID: "3"})
	// Non-deleted rows inside the window — never counted, never summed.
	mustRecord(AutoCleanEvent{StartedAtUnix: 2500, Trigger: "daily", Outcome: "skipped",
		SkipReason: "idle_too_short", TargetPath: "/c", Module: "dev", TargetSizeBytes: 400, SuggestionID: "4"})
	mustRecord(AutoCleanEvent{StartedAtUnix: 2600, Trigger: "daily", Outcome: "in_progress",
		TargetPath: "/d", Module: "dev", TargetSizeBytes: 700, SuggestionID: "5"})

	count, freed, err := s.AutoCleanStatsSince(1000)
	if err != nil {
		t.Fatalf("AutoCleanStatsSince: %v", err)
	}
	if count != 2 {
		t.Errorf("count = %d, want 2 (only deleted rows >= cutoff)", count)
	}
	if freed != 350 {
		t.Errorf("freed = %d, want 350 (100 + 250)", freed)
	}
}

// TestAutoCleanStatsSinceEmpty proves the zero state is honest: no rows means
// (0, 0, nil), not a scan error on SUM(NULL).
func TestAutoCleanStatsSinceEmpty(t *testing.T) {
	s := openTestStore(t)
	count, freed, err := s.AutoCleanStatsSince(0)
	if err != nil {
		t.Fatalf("AutoCleanStatsSince on empty table: %v", err)
	}
	if count != 0 || freed != 0 {
		t.Errorf("empty table stats = (%d, %d), want (0, 0)", count, freed)
	}
}
