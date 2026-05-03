package autoclean

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/FRIKKern/noo-noo/internal/core"

	_ "modernc.org/sqlite"
)

// memEventStore is a SQLite-backed EventStore used in tests. The schema
// it loads is the same migration the daemon ships in production
// (internal/store/migrations/0005_auto_clean_events.sql), so the tests
// also serve as a smoke check that the migration parses.
type memEventStore struct {
	db *sql.DB
}

func newMemEventStore(t *testing.T) *memEventStore {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open mem sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	migPath := filepath.Join("..", "store", "migrations", "0005_auto_clean_events.sql")
	mig, err := os.ReadFile(migPath)
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	if _, err := db.Exec(string(mig)); err != nil {
		t.Fatalf("apply migration: %v", err)
	}
	return &memEventStore{db: db}
}

func (m *memEventStore) RecordAutoCleanEvent(e AutoCleanEvent) (int64, error) {
	res, err := m.db.Exec(`INSERT INTO auto_clean_events
		(started_at_unix, ended_at_unix, trigger, outcome, skip_reason, target_path, module,
		 target_size_bytes, freed_bytes, idle_days_at_decision, suggestion_id, error_msg)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		e.StartedAtUnix, nullableInt(e.EndedAtUnix), e.Trigger, e.Outcome, e.SkipReason,
		e.TargetPath, e.Module, e.TargetSizeBytes, e.FreedBytes, e.IdleDaysAtDecision,
		e.SuggestionID, e.ErrorMsg)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (m *memEventStore) UpdateAutoCleanEvent(id int64, u AutoCleanEventUpdate) error {
	_, err := m.db.Exec(`UPDATE auto_clean_events
		SET ended_at_unix = ?, outcome = ?, freed_bytes = ?, error_msg = ?
		WHERE id = ?`,
		u.EndedAtUnix, u.Outcome, u.FreedBytes, u.ErrorMsg, id)
	return err
}

// rowCount returns (count_total, count_in_progress, count_deleted, count_skipped).
func (m *memEventStore) counts(t *testing.T) (total, inProg, deleted, skipped int) {
	t.Helper()
	q := func(sql string) int {
		var n int
		if err := m.db.QueryRow(sql).Scan(&n); err != nil {
			t.Fatalf("count query %q: %v", sql, err)
		}
		return n
	}
	return q(`SELECT COUNT(*) FROM auto_clean_events`),
		q(`SELECT COUNT(*) FROM auto_clean_events WHERE outcome='in_progress'`),
		q(`SELECT COUNT(*) FROM auto_clean_events WHERE outcome='deleted'`),
		q(`SELECT COUNT(*) FROM auto_clean_events WHERE outcome='skipped'`)
}

func nullableInt(v int64) any {
	if v == 0 {
		return nil
	}
	return v
}

// stubDeleter returns a Deleter that always claims to free `bytes` and
// records every path it was called with into `seen`.
func stubDeleter(bytes int64, seen *[]string) Deleter {
	return func(_ context.Context, p string) (int64, error) {
		*seen = append(*seen, p)
		return bytes, nil
	}
}

func failingDeleter(err error) Deleter {
	return func(_ context.Context, _ string) (int64, error) {
		return 0, err
	}
}

func newEngineForTest(t *testing.T, root string) (*Engine, *memEventStore, *[]string) {
	t.Helper()
	st := newMemEventStore(t)
	safety := core.NewSafety([]string{root}, []string{".git"})
	seen := &[]string{}
	cfg := Config{
		Enabled:            true,
		ModulesAllowed:     []string{"dev"},
		MinIdleDays:        90,
		MinSizeMB:          1024,
		SizeCapPerTickGB:   10,
		RiskAcknowledgedAt: "2026-05-01T00:00:00Z",
	}
	e := New(st, cfg, []string{root}, safety, map[string]Deleter{
		"dev": stubDeleter(2048, seen),
	})
	return e, st, seen
}

func TestApplyHappyPath_AuditBeforeAndAfter(t *testing.T) {
	tmp := t.TempDir()
	target := filepath.Join(tmp, "proj", "node_modules")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	e, st, seen := newEngineForTest(t, tmp)

	res, err := e.Apply(context.Background(), Action{
		SuggestionID: 7,
		Module:       "dev",
		TargetPath:   target,
		SizeBytes:    1500 * 1024 * 1024,
		IdleDays:     200,
	}, "daily")
	if err != nil {
		t.Fatalf("happy-path Apply errored: %v", err)
	}
	if res.FreedBytes != 2048 {
		t.Errorf("FreedBytes = %d, want 2048", res.FreedBytes)
	}
	if len(*seen) != 1 || (*seen)[0] != target {
		t.Errorf("deleter saw %v, want [%q]", *seen, target)
	}

	total, inProg, deleted, skipped := st.counts(t)
	if total != 1 {
		t.Errorf("audit row total = %d, want 1 (in_progress upgraded to deleted)", total)
	}
	if inProg != 0 {
		t.Errorf("in_progress rows = %d, want 0 (must be upgraded)", inProg)
	}
	if deleted != 1 {
		t.Errorf("deleted rows = %d, want 1", deleted)
	}
	if skipped != 0 {
		t.Errorf("skipped rows = %d, want 0", skipped)
	}
}

func TestApply_RejectsNonDailyTrigger(t *testing.T) {
	// Pressure-triggered deletes are forbidden by design. The check
	// fires before any audit row is written so the audit table stays
	// silent for "shouldn't have been called" cases.
	tmp := t.TempDir()
	e, st, seen := newEngineForTest(t, tmp)
	for _, trig := range []string{"pressure", "manual", "", "DAILY", "Daily"} {
		_, err := e.Apply(context.Background(), Action{
			Module: "dev", TargetPath: filepath.Join(tmp, "x"),
		}, trig)
		if !errors.Is(err, errPressureTrigger) {
			t.Errorf("trigger=%q: want errPressureTrigger, got %v", trig, err)
		}
	}
	if len(*seen) != 0 {
		t.Errorf("deleter must not be called for rejected triggers; saw %v", *seen)
	}
	if total, _, _, _ := st.counts(t); total != 0 {
		t.Errorf("rejected trigger must not write audit rows; total=%d", total)
	}
}

func TestApply_SafetyGuardWritesSkippedRow(t *testing.T) {
	tmp := t.TempDir()
	e, st, seen := newEngineForTest(t, tmp)
	// Path outside the configured root.
	_, err := e.Apply(context.Background(), Action{
		SuggestionID: 1,
		Module:       "dev",
		TargetPath:   "/tmp/elsewhere",
	}, "daily")
	if err == nil {
		t.Fatal("guard must reject out-of-root path")
	}
	if len(*seen) != 0 {
		t.Errorf("deleter must NOT run when guard rejects; saw %v", *seen)
	}
	total, _, deleted, skipped := st.counts(t)
	if total != 1 || skipped != 1 || deleted != 0 {
		t.Errorf("guard rejection must produce exactly one skipped row (total=%d skipped=%d deleted=%d)",
			total, skipped, deleted)
	}
}

func TestApply_SafetyGuardRejectsBadModule(t *testing.T) {
	tmp := t.TempDir()
	e, _, _ := newEngineForTest(t, tmp)
	target := filepath.Join(tmp, "proj")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	_, err := e.Apply(context.Background(), Action{
		Module: "caches", TargetPath: target,
	}, "daily")
	if err == nil {
		t.Fatal("guard must reject non-allowlisted module")
	}
}

func TestApply_SafetyGuardCallsCoreSafety(t *testing.T) {
	// /System/Library is in core.Safety's always-blocked list. Even if
	// roots and module pass, core.Safety must veto the delete.
	tmp := t.TempDir()
	st := newMemEventStore(t)
	safety := core.NewSafety([]string{"/"}, nil) // root = /, deliberately wide
	seen := &[]string{}
	e := New(st, Config{
		Enabled: true, ModulesAllowed: []string{"dev"},
		MinIdleDays: 90, MinSizeMB: 1024, SizeCapPerTickGB: 10,
		RiskAcknowledgedAt: "2026-05-01T00:00:00Z",
	}, []string{tmp}, safety, map[string]Deleter{
		"dev": stubDeleter(0, seen),
	})

	_, err := e.Apply(context.Background(), Action{
		Module: "dev", TargetPath: "/System/Library/Foo",
	}, "daily")
	if err == nil {
		t.Fatal("core.Safety must veto /System/Library paths")
	}
	if len(*seen) != 0 {
		t.Errorf("deleter must NOT run after Safety rejection; saw %v", *seen)
	}
}

func TestApply_DeleterErrorWritesErroredRow(t *testing.T) {
	tmp := t.TempDir()
	target := filepath.Join(tmp, "proj", "node_modules")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	st := newMemEventStore(t)
	safety := core.NewSafety([]string{tmp}, []string{".git"})
	want := errors.New("disk-was-on-fire")
	e := New(st, Config{
		Enabled: true, ModulesAllowed: []string{"dev"},
		MinIdleDays: 90, MinSizeMB: 1024, SizeCapPerTickGB: 10,
		RiskAcknowledgedAt: "2026-05-01T00:00:00Z",
	}, []string{tmp}, safety, map[string]Deleter{
		"dev": failingDeleter(want),
	})

	_, err := e.Apply(context.Background(), Action{
		Module: "dev", TargetPath: target, SizeBytes: 1024,
	}, "daily")
	if err == nil || err.Error() != "disk-was-on-fire" {
		t.Fatalf("Apply must surface deleter err; got %v", err)
	}
	// Row must exist and have outcome=errored (not in_progress).
	var outcome, errMsg string
	if err := st.db.QueryRow(`SELECT outcome, error_msg FROM auto_clean_events`).Scan(&outcome, &errMsg); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if outcome != "errored" {
		t.Errorf("outcome = %q, want 'errored'", outcome)
	}
	if errMsg != "disk-was-on-fire" {
		t.Errorf("error_msg = %q, want 'disk-was-on-fire'", errMsg)
	}
}
