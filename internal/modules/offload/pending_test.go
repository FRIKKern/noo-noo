package offload

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/FRIKKern/noo-noo/internal/modules"
	"github.com/FRIKKern/noo-noo/internal/store"
)

func openQueueStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "queue.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// enqueueViaGate drives the REAL enqueue path: an Apply refused by the
// stop-gate, then EnqueueDeferred with the gate reason — exactly what the
// CLI does on consent.
func enqueueViaGate(t *testing.T, m *Module, qs QueueStore, asset, destRoot string) int64 {
	t.Helper()
	_, err := m.Apply(context.Background(), modules.Action{
		Module: "offload", Op: "relocate", Target: asset,
		Destination: filepath.Join(destRoot, "asset"),
	})
	if !errors.Is(err, ErrStopGate) {
		t.Fatalf("want ErrStopGate from gated Apply, got %v", err)
	}
	id, err := m.EnqueueDeferred(qs, modules.Action{Module: "offload", Op: "relocate", Target: asset},
		err.Error(), time.Unix(1000, 0))
	if err != nil {
		t.Fatalf("EnqueueDeferred: %v", err)
	}
	return id
}

// TestApplyStopGateIsMatchable proves the stop-gate refusal is a matchable
// sentinel — the CLI's queue-offer branch depends on errors.Is, not string
// grubbing.
func TestApplyStopGateIsMatchable(t *testing.T) {
	home, asset, destRoot, cfg, pbs := fixture(t, "Claude", false)
	_ = home
	procs := &fakeProcs{running: map[string]bool{"Claude": true}}
	m := New(cfg, pbs, Deps{Home: home, Guard: &fakeGuard{}, Procs: procs, Copy: goCopy})
	_, err := m.Apply(context.Background(), modules.Action{
		Module: "offload", Op: "relocate", Target: asset,
		Destination: filepath.Join(destRoot, "asset"),
	})
	if !errors.Is(err, ErrStopGate) {
		t.Fatalf("stop-gate refusal not matchable via errors.Is(ErrStopGate): %v", err)
	}
	if !strings.Contains(err.Error(), "stop-gate") {
		t.Fatalf("refusal lost its human-readable stop-gate naming: %v", err)
	}
}

// TestEnqueueDeferredRefusesWhatApplyRefuses: only legal relocate actions can
// be queued — consent to defer is consent to the same action.
func TestEnqueueDeferredRefusesWhatApplyRefuses(t *testing.T) {
	home := t.TempDir()
	pn := filepath.Join(home, "Library", "pnpm")
	if err := os.MkdirAll(pn, 0o755); err != nil {
		t.Fatal(err)
	}
	qs := openQueueStore(t)
	now := time.Unix(1, 0)

	// Native-config asset: refused.
	cfg := Config{DestRoot: t.TempDir(), DestVolumeUUID: "u"}
	m := New(cfg, nil, Deps{Home: home, Guard: &fakeGuard{}, Procs: &fakeProcs{}, Copy: goCopy})
	if _, err := m.EnqueueDeferred(qs, modules.Action{Op: "relocate", Target: pn}, "gate", now); err == nil {
		t.Fatal("queued a native-config asset")
	}
	// Unknown target: refused.
	if _, err := m.EnqueueDeferred(qs, modules.Action{Op: "relocate", Target: "/not/a/playbook"}, "gate", now); err == nil {
		t.Fatal("queued an unknown target")
	}
	// Unconfigured offload: refused.
	m2 := New(Config{}, nil, Deps{Home: home, Guard: &fakeGuard{}, Procs: &fakeProcs{}, Copy: goCopy})
	if _, err := m2.EnqueueDeferred(qs, modules.Action{Op: "relocate", Target: pn}, "gate", now); err == nil {
		t.Fatal("queued with offload unconfigured")
	}
	if pending, _ := qs.ListPendingRelocations(); len(pending) != 0 {
		t.Fatalf("refused enqueues still wrote rows: %v", pending)
	}
}

// TestRunPendingAppliesWhenGateClears is the end-to-end happy path of the
// whole slice: stop-gate refusal → consented queue → app quits → run-pending
// re-checks fresh and applies → symlink live, row terminal with freed bytes.
func TestRunPendingAppliesWhenGateClears(t *testing.T) {
	home, asset, destRoot, cfg, pbs := fixture(t, "Claude", false)
	procs := &fakeProcs{running: map[string]bool{"Claude": true}}
	guard := &fakeGuard{}
	m := New(cfg, pbs, Deps{Home: home, Guard: guard, Procs: procs, Copy: goCopy})
	qs := openQueueStore(t)
	id := enqueueViaGate(t, m, qs, asset, destRoot)

	// Gate still closed: run-pending marks blocked, source untouched.
	results, err := m.RunPending(context.Background(), qs, TriggerPendingManual, -1, nil)
	if err != nil || len(results) != 1 {
		t.Fatalf("RunPending: %v (results %v)", err, results)
	}
	if results[0].Outcome != PendingBlocked || !strings.Contains(results[0].Reason, "stop-gate") {
		t.Fatalf("want blocked-on-stop-gate, got %+v", results[0])
	}
	if fi, err := os.Lstat(asset); err != nil || !fi.IsDir() {
		t.Fatalf("source touched by a blocked re-check: %v %v", fi, err)
	}

	// The app quits; the next re-check applies.
	procs.running["Claude"] = false
	results, err = m.RunPending(context.Background(), qs, TriggerPendingManual, -1, func() time.Time { return time.Unix(2000, 0) })
	if err != nil || len(results) != 1 {
		t.Fatalf("RunPending 2: %v (results %v)", err, results)
	}
	if results[0].Outcome != PendingApplied || !results[0].Attempted || results[0].Freed <= 0 {
		t.Fatalf("want applied with freed bytes, got %+v", results[0])
	}
	// Symlink is live; content reachable through the old path.
	if fi, err := os.Lstat(asset); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("target is not a symlink after apply: %v %v", fi, err)
	}
	if b, err := os.ReadFile(filepath.Join(asset, "a.txt")); err != nil || len(b) != 100 {
		t.Fatalf("content through symlink: %d bytes, err %v", len(b), err)
	}
	// Row is terminal with the outcome recorded.
	row, err := qs.GetRelocation(id)
	if err != nil || row.Status != store.RelocApplied || row.FreedBytes <= 0 || row.ResolvedAtUnix != 2000 {
		t.Fatalf("queue row after apply: %+v (err %v)", row, err)
	}
	if row.Attempts != 1 {
		t.Fatalf("blocked pre-check must not burn attempts; want 1 apply attempt, got %d", row.Attempts)
	}
	// Nothing pending anymore.
	if pending, _ := qs.ListPendingRelocations(); len(pending) != 0 {
		t.Fatalf("applied entry still pending: %v", pending)
	}
}

// TestRunPendingStillBlockedReasons: each fresh gate blocks with its own
// honest reason and never touches the source.
func TestRunPendingStillBlockedReasons(t *testing.T) {
	cases := []struct {
		name       string
		mutate     func(t *testing.T, m *Module, guard *fakeGuard, asset, destRoot string)
		wantReason string
	}{
		{"volguard refuses", func(t *testing.T, m *Module, guard *fakeGuard, _, _ string) {
			guard.err = fmt.Errorf("wrong disk mounted")
		}, "destination refused"},
		{"dest collision", func(t *testing.T, _ *Module, _ *fakeGuard, _, destRoot string) {
			if err := os.MkdirAll(filepath.Join(destRoot, "asset"), 0o755); err != nil {
				t.Fatal(err)
			}
		}, "already exists"},
		{"source vanished", func(t *testing.T, _ *Module, _ *fakeGuard, asset, _ string) {
			if err := os.RemoveAll(asset); err != nil {
				t.Fatal(err)
			}
		}, "missing or no longer a real directory"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home, asset, destRoot, cfg, pbs := fixture(t, "Claude", false)
			procs := &fakeProcs{running: map[string]bool{"Claude": true}}
			guard := &fakeGuard{}
			m := New(cfg, pbs, Deps{Home: home, Guard: guard, Procs: procs, Copy: goCopy})
			qs := openQueueStore(t)
			id := enqueueViaGate(t, m, qs, asset, destRoot)

			procs.running["Claude"] = false // the queue-time gate has cleared
			tc.mutate(t, m, guard, asset, destRoot)

			results, err := m.RunPending(context.Background(), qs, TriggerPendingManual, -1, nil)
			if err != nil || len(results) != 1 {
				t.Fatalf("RunPending: %v (results %v)", err, results)
			}
			r := results[0]
			if r.Outcome != PendingBlocked || !strings.Contains(r.Reason, tc.wantReason) {
				t.Fatalf("want blocked with %q, got %+v", tc.wantReason, r)
			}
			if r.Attempted {
				t.Fatalf("pre-check block must not invoke Apply: %+v", r)
			}
			// Source untouched (when it still exists in this case).
			if tc.name != "source vanished" {
				if fi, err := os.Lstat(asset); err != nil || !fi.IsDir() {
					t.Fatalf("source touched by blocked re-check: %v %v", fi, err)
				}
			}
			// Row still pending, reason recorded, no attempt burned.
			row, err := qs.GetRelocation(id)
			if err != nil || row.Status != store.RelocBlocked {
				t.Fatalf("row after blocked re-check: %+v (err %v)", row, err)
			}
			if !strings.Contains(row.BlockedReason, tc.wantReason) || row.Attempts != 0 {
				t.Fatalf("blocked bookkeeping wrong: %+v", row)
			}
		})
	}
}

// TestRunPendingDestPinChange: a queued entry whose consented destination no
// longer matches the live config is blocked — never silently redirected.
func TestRunPendingDestPinChange(t *testing.T) {
	home, asset, destRoot, cfg, pbs := fixture(t, "Claude", false)
	procs := &fakeProcs{running: map[string]bool{"Claude": true}}
	m := New(cfg, pbs, Deps{Home: home, Guard: &fakeGuard{}, Procs: procs, Copy: goCopy})
	qs := openQueueStore(t)
	enqueueViaGate(t, m, qs, asset, destRoot)
	procs.running["Claude"] = false

	// The user re-points offload at a different volume.
	m2 := New(Config{DestRoot: t.TempDir(), DestVolumeUUID: "other-uuid"}, pbs,
		Deps{Home: home, Guard: &fakeGuard{}, Procs: procs, Copy: goCopy})
	results, err := m2.RunPending(context.Background(), qs, TriggerPendingManual, -1, nil)
	if err != nil || len(results) != 1 {
		t.Fatalf("RunPending: %v (%v)", err, results)
	}
	if results[0].Outcome != PendingBlocked || !strings.Contains(results[0].Reason, "destination changed") {
		t.Fatalf("want blocked on dest-pin change, got %+v", results[0])
	}
	if fi, err := os.Lstat(asset); err != nil || !fi.IsDir() {
		t.Fatalf("source touched: %v %v", fi, err)
	}
}

// TestRunPendingBudgetCapsApplies: with budget 1 and two ready entries, only
// the oldest applies; the second is left pending and UNRESOLVED (not marked
// blocked — its gates passed, the run just ran out of allowance).
func TestRunPendingBudgetCapsApplies(t *testing.T) {
	home := t.TempDir()
	destRoot := filepath.Join(t.TempDir(), "offload")
	if err := os.MkdirAll(destRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	var pbs []Playbook
	assets := make([]string, 2)
	for i := range assets {
		name := fmt.Sprintf("asset%d", i)
		assets[i] = filepath.Join(home, name)
		if err := os.MkdirAll(assets[i], 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(assets[i], "f.bin"), []byte(strings.Repeat("x", 100)), 0o644); err != nil {
			t.Fatal(err)
		}
		pbs = append(pbs, Playbook{AssetID: name, Path: "~/" + name, Class: ClassRelocate, StopGate: "app"})
	}
	cfg := Config{DestRoot: destRoot, DestVolumeUUID: "u1"}
	procs := &fakeProcs{running: map[string]bool{"app": false}}
	m := New(cfg, pbs, Deps{Home: home, Guard: &fakeGuard{}, Procs: procs, Copy: goCopy})
	qs := openQueueStore(t)
	for i, a := range assets {
		if _, err := m.EnqueueDeferred(qs, modules.Action{Op: "relocate", Target: a},
			"stop-gate: app", time.Unix(int64(100*(i+1)), 0)); err != nil {
			t.Fatalf("enqueue %s: %v", a, err)
		}
	}

	results, err := m.RunPending(context.Background(), qs, TriggerPendingDaily, 1, nil)
	if err != nil || len(results) != 2 {
		t.Fatalf("RunPending: %v (%v)", err, results)
	}
	if results[0].Outcome != PendingApplied || results[0].Entry.TargetPath != assets[0] {
		t.Fatalf("oldest entry must apply first: %+v", results[0])
	}
	if results[1].Outcome != PendingSkippedBudget || results[1].Attempted {
		t.Fatalf("second entry must be budget-skipped untouched: %+v", results[1])
	}
	pending, err := qs.ListPendingRelocations()
	if err != nil || len(pending) != 1 || pending[0].TargetPath != assets[1] {
		t.Fatalf("want exactly the second entry still pending: %v (err %v)", pending, err)
	}
	if pending[0].Status != store.RelocQueued || pending[0].Attempts != 0 {
		t.Fatalf("budget-skipped row mutated: %+v", pending[0])
	}
	// Second asset untouched on disk.
	if fi, err := os.Lstat(assets[1]); err != nil || !fi.IsDir() {
		t.Fatalf("budget-skipped source touched: %v %v", fi, err)
	}
}

// TestRunPendingApplyFailureBlocksAndRestores: when the gates pass but Apply
// itself fails mid-copy, the entry is blocked with the apply error, the
// attempt IS counted (bytes moved), and the source is restored by Apply's
// own law.
func TestRunPendingApplyFailureBlocksAndRestores(t *testing.T) {
	home, asset, destRoot, cfg, pbs := fixture(t, "", false)
	boom := errors.New("disk yanked mid-copy")
	m := New(cfg, pbs, Deps{Home: home, Guard: &fakeGuard{}, Procs: &fakeProcs{},
		Copy: func(ctx context.Context, src, dst string) error { return boom }})
	qs := openQueueStore(t)
	id, err := m.EnqueueDeferred(qs, modules.Action{Op: "relocate", Target: asset}, "stop-gate: x", time.Unix(1, 0))
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	results, err := m.RunPending(context.Background(), qs, TriggerPendingManual, -1, nil)
	if err != nil || len(results) != 1 {
		t.Fatalf("RunPending: %v (%v)", err, results)
	}
	r := results[0]
	if r.Outcome != PendingBlocked || !r.Attempted || !errors.Is(r.Err, boom) {
		t.Fatalf("want attempted-blocked carrying the apply error, got %+v", r)
	}
	// Source intact (Apply restore law), destination clean.
	if fi, err := os.Lstat(asset); err != nil || !fi.IsDir() {
		t.Fatalf("source damaged: %v %v", fi, err)
	}
	if _, err := os.Lstat(filepath.Join(destRoot, "asset")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("partial copy left at destination: %v", err)
	}
	row, err := qs.GetRelocation(id)
	if err != nil || row.Status != store.RelocBlocked || row.Attempts != 1 {
		t.Fatalf("row after failed apply: %+v (err %v)", row, err)
	}
	if !strings.Contains(row.BlockedReason, "apply failed") {
		t.Fatalf("blocked reason lost the apply error: %q", row.BlockedReason)
	}
}

// TestRunPendingRejectsForbiddenTriggers mirrors autoclean's
// errPressureTrigger law at the module level: a pressure (or unknown)
// trigger is refused OUTRIGHT — no queue row is read, no gate consulted,
// nothing on disk or in the queue changes.
func TestRunPendingRejectsForbiddenTriggers(t *testing.T) {
	home, asset, destRoot, cfg, pbs := fixture(t, "Claude", false)
	procs := &fakeProcs{running: map[string]bool{"Claude": true}}
	m := New(cfg, pbs, Deps{Home: home, Guard: &fakeGuard{}, Procs: procs, Copy: goCopy})
	qs := openQueueStore(t)
	id := enqueueViaGate(t, m, qs, asset, destRoot)
	procs.running["Claude"] = false // the entry is fully ready to apply

	for _, trigger := range []string{"pressure", "", "PRESSURE", "hourly"} {
		results, err := m.RunPending(context.Background(), qs, trigger, -1, nil)
		if !errors.Is(err, ErrPendingTriggerForbidden) {
			t.Fatalf("trigger %q: want ErrPendingTriggerForbidden, got %v (results %v)", trigger, err, results)
		}
		if len(results) != 0 {
			t.Fatalf("trigger %q: forbidden trigger still produced results: %v", trigger, results)
		}
	}
	// The ready entry is completely untouched: still queued, never attempted.
	row, err := qs.GetRelocation(id)
	if err != nil || row.Status != store.RelocQueued || row.Attempts != 0 {
		t.Fatalf("forbidden triggers mutated the queue row: %+v (err %v)", row, err)
	}
	if fi, err := os.Lstat(asset); err != nil || !fi.IsDir() {
		t.Fatalf("forbidden trigger touched the source: %v %v", fi, err)
	}
}
