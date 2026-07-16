package store

import (
	"errors"
	"testing"
)

// TestRelocationQueueTableInSchema proves the v3 DDL creates relocation_queue
// on a fresh Open via CREATE TABLE IF NOT EXISTS only — no migration runner,
// and re-opening the same database is harmless (D16).
func TestRelocationQueueTableInSchema(t *testing.T) {
	s := openTestStore(t)
	var name string
	err := s.DB().QueryRow(
		`SELECT name FROM sqlite_master WHERE type='table' AND name='relocation_queue'`,
	).Scan(&name)
	if err != nil {
		t.Fatalf("relocation_queue table missing from schema.sql: %v", err)
	}
}

// TestRelocationQueueReopenIdempotent proves an existing store with live rows
// survives a second migrate() pass (the daemon re-runs schema.sql on every
// Open — a non-idempotent statement would brick the store on the SECOND boot).
func TestRelocationQueueReopenIdempotent(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/test.db"
	s, err := Open(path)
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	id, err := s.EnqueueRelocation(RelocationQueueEntry{
		QueuedAtUnix: 100, TargetPath: "/home/x/asset", PlaybookAssetID: "asset",
		DestRoot: "/vol/off", DestVolumeUUID: "uuid-1", GateReason: "stop-gate: app",
	})
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	s2, err := Open(path) // second boot: schema.sql re-executes
	if err != nil {
		t.Fatalf("second Open (re-migrate) failed — schema.sql is not idempotent: %v", err)
	}
	t.Cleanup(func() { _ = s2.Close() })
	got, err := s2.GetRelocation(id)
	if err != nil {
		t.Fatalf("row lost across re-open: %v", err)
	}
	if got.TargetPath != "/home/x/asset" || got.Status != RelocQueued {
		t.Fatalf("row mangled across re-open: %+v", got)
	}
}

func mustEnqueue(t *testing.T, s *Store, e RelocationQueueEntry) int64 {
	t.Helper()
	id, err := s.EnqueueRelocation(e)
	if err != nil {
		t.Fatalf("EnqueueRelocation(%q): %v", e.TargetPath, err)
	}
	return id
}

// TestEnqueueListResolveRoundTrip walks the full two-phase lifecycle:
// queued → (attempt) → blocked with reason → (attempt) → applied with freed
// bytes, checking what ListPendingRelocations shows at every step.
func TestEnqueueListResolveRoundTrip(t *testing.T) {
	s := openTestStore(t)
	id := mustEnqueue(t, s, RelocationQueueEntry{
		QueuedAtUnix: 1000, TargetPath: "/home/u/.claude", PlaybookAssetID: "claude-config",
		DestRoot: "/Volumes/EXT/offload", DestVolumeUUID: "uuid-ext",
		GateReason: `stop-gate: "Claude" was running`,
	})
	if id <= 0 {
		t.Fatalf("want positive id, got %d", id)
	}

	pending, err := s.ListPendingRelocations()
	if err != nil || len(pending) != 1 {
		t.Fatalf("pending after enqueue = %v (err %v), want 1 row", pending, err)
	}
	e := pending[0]
	if e.Status != RelocQueued || e.GateReason == "" || e.ResolvedAtUnix != 0 || e.Attempts != 0 {
		t.Fatalf("fresh row wrong: %+v", e)
	}

	// Re-check 1: still blocked. Phase 1 then phase 2.
	if err := s.MarkRelocationAttempt(id, 2000); err != nil {
		t.Fatalf("mark attempt: %v", err)
	}
	if err := s.ResolveRelocation(id, RelocationResolution{
		Status: RelocBlocked, BlockedReason: `stop-gate: "Claude" still running`,
	}); err != nil {
		t.Fatalf("resolve blocked: %v", err)
	}
	pending, err = s.ListPendingRelocations()
	if err != nil || len(pending) != 1 {
		t.Fatalf("blocked row must STAY pending, got %v (err %v)", pending, err)
	}
	e = pending[0]
	if e.Status != RelocBlocked || e.BlockedReason == "" || e.Attempts != 1 || e.LastAttemptAtUnix != 2000 {
		t.Fatalf("blocked row wrong: %+v", e)
	}
	if e.ResolvedAtUnix != 0 {
		t.Fatalf("blocked is not terminal; resolved_at must stay NULL, got %d", e.ResolvedAtUnix)
	}

	// Re-check 2: the gate cleared; applied.
	if err := s.MarkRelocationAttempt(id, 3000); err != nil {
		t.Fatalf("mark attempt 2: %v", err)
	}
	if err := s.ResolveRelocation(id, RelocationResolution{
		Status: RelocApplied, FreedBytes: 4_000_000_000, ResolvedAtUnix: 3010,
	}); err != nil {
		t.Fatalf("resolve applied: %v", err)
	}
	pending, err = s.ListPendingRelocations()
	if err != nil || len(pending) != 0 {
		t.Fatalf("applied row must leave the pending list, got %v (err %v)", pending, err)
	}
	got, err := s.GetRelocation(id)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Status != RelocApplied || got.FreedBytes != 4_000_000_000 ||
		got.ResolvedAtUnix != 3010 || got.Attempts != 2 {
		t.Fatalf("applied row wrong: %+v", got)
	}
}

// TestEnqueueIdempotentPerPendingTarget: consenting twice to the same target
// must not fork the queue — the existing pending row's id comes back. Once
// that row reaches a terminal status, the target may be enqueued afresh.
func TestEnqueueIdempotentPerPendingTarget(t *testing.T) {
	s := openTestStore(t)
	e := RelocationQueueEntry{
		QueuedAtUnix: 100, TargetPath: "/home/u/bulky", PlaybookAssetID: "bulky",
		DestRoot: "/vol/off", DestVolumeUUID: "u1", GateReason: "stop-gate: app",
	}
	id1 := mustEnqueue(t, s, e)
	id2 := mustEnqueue(t, s, e)
	if id1 != id2 {
		t.Fatalf("duplicate enqueue forked the queue: %d vs %d", id1, id2)
	}

	// Idempotency holds across the blocked (re-opened) state too.
	if err := s.ResolveRelocation(id1, RelocationResolution{Status: RelocBlocked, BlockedReason: "still running"}); err != nil {
		t.Fatalf("resolve blocked: %v", err)
	}
	if id3 := mustEnqueue(t, s, e); id3 != id1 {
		t.Fatalf("enqueue while blocked forked the queue: %d vs %d", id3, id1)
	}

	// Terminal row → a fresh enqueue is a NEW row.
	if err := s.ResolveRelocation(id1, RelocationResolution{Status: RelocCancelled, ResolvedAtUnix: 200}); err != nil {
		t.Fatalf("resolve cancelled: %v", err)
	}
	if id4 := mustEnqueue(t, s, e); id4 == id1 {
		t.Fatalf("enqueue after terminal resolve returned the dead row %d", id4)
	}
}

// TestResolveGuardsTerminalRows: applied/cancelled rows refuse further
// transitions and attempts; an illegal status string is rejected outright.
func TestResolveGuardsTerminalRows(t *testing.T) {
	s := openTestStore(t)
	id := mustEnqueue(t, s, RelocationQueueEntry{
		QueuedAtUnix: 1, TargetPath: "/t", PlaybookAssetID: "t",
		DestRoot: "/d", DestVolumeUUID: "u",
	})

	if err := s.ResolveRelocation(id, RelocationResolution{Status: "exploded"}); err == nil {
		t.Fatal("illegal status accepted")
	}

	if err := s.ResolveRelocation(id, RelocationResolution{Status: RelocApplied, FreedBytes: 5, ResolvedAtUnix: 9}); err != nil {
		t.Fatalf("resolve applied: %v", err)
	}
	// Terminal: every further state change refuses with ErrRelocResolved.
	if err := s.ResolveRelocation(id, RelocationResolution{Status: RelocBlocked, BlockedReason: "x"}); !errors.Is(err, ErrRelocResolved) {
		t.Fatalf("terminal row accepted a transition: %v", err)
	}
	if err := s.ResolveRelocation(id, RelocationResolution{Status: RelocCancelled, ResolvedAtUnix: 10}); !errors.Is(err, ErrRelocResolved) {
		t.Fatalf("terminal row accepted cancel: %v", err)
	}
	if err := s.MarkRelocationAttempt(id, 11); !errors.Is(err, ErrRelocResolved) {
		t.Fatalf("terminal row accepted an attempt mark: %v", err)
	}

	// The terminal row is untouched by the refused writes.
	got, err := s.GetRelocation(id)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Status != RelocApplied || got.FreedBytes != 5 || got.Attempts != 0 {
		t.Fatalf("terminal row mutated by refused transitions: %+v", got)
	}
}

// TestListPendingOrdersOldestFirst: the daily budget of 1 must drain the
// queue fairly — oldest queued entry first.
func TestListPendingOrdersOldestFirst(t *testing.T) {
	s := openTestStore(t)
	mustEnqueue(t, s, RelocationQueueEntry{QueuedAtUnix: 300, TargetPath: "/c", PlaybookAssetID: "c", DestRoot: "/d", DestVolumeUUID: "u"})
	mustEnqueue(t, s, RelocationQueueEntry{QueuedAtUnix: 100, TargetPath: "/a", PlaybookAssetID: "a", DestRoot: "/d", DestVolumeUUID: "u"})
	mustEnqueue(t, s, RelocationQueueEntry{QueuedAtUnix: 200, TargetPath: "/b", PlaybookAssetID: "b", DestRoot: "/d", DestVolumeUUID: "u"})
	pending, err := s.ListPendingRelocations()
	if err != nil || len(pending) != 3 {
		t.Fatalf("pending = %v (err %v)", pending, err)
	}
	if pending[0].TargetPath != "/a" || pending[1].TargetPath != "/b" || pending[2].TargetPath != "/c" {
		t.Fatalf("order wrong: %q %q %q", pending[0].TargetPath, pending[1].TargetPath, pending[2].TargetPath)
	}
}
