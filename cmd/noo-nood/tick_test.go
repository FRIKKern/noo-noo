package main

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/FRIKKern/noo-noo/internal/config"
	"github.com/FRIKKern/noo-noo/internal/core"
	"github.com/FRIKKern/noo-noo/internal/heuristics"
	"github.com/FRIKKern/noo-noo/internal/modules"
	"github.com/FRIKKern/noo-noo/internal/store"
)

// tickFakeLeakSource stands in for the real leaks module: Scan returns the
// canned report, Plan keeps only stale items (mirroring leaks.Module.Plan).
type tickFakeLeakSource struct{ items []modules.Item }

func (f *tickFakeLeakSource) Scan(context.Context) (modules.Report, error) {
	return modules.Report{Module: "leaks", Items: f.items}, nil
}

func (f *tickFakeLeakSource) Plan(r modules.Report) []modules.Action {
	var out []modules.Action
	for _, it := range r.Items {
		if it.Evidence["staleness"] != "stale" {
			continue
		}
		out = append(out, modules.Action{
			Module: "leaks", Op: "delete", Target: it.Path, Size: it.Size, Risk: modules.RiskLow,
		})
	}
	return out
}

const tickTestWorkaround = "Quit and relaunch Chrome cleanly, or launch with " +
	"--disable-features=MacAppCodeSignClone to stop the leak class at its source."

const tickTestTarget = "/private/var/folders/zz/T/X/gone.code_sign_clone/code_sign_clone.zzzzzz"

func tickStaleItem(size int64) modules.Item {
	return modules.Item{
		Path: tickTestTarget,
		Size: core.Bytes(size),
		Evidence: map[string]string{
			"signature":  "chrome-code-sign-clone",
			"staleness":  "stale",
			"lsof":       "no open files",
			"workaround": tickTestWorkaround,
		},
	}
}

// tickTestDaemon builds a daemon over a temp store with every heuristic
// except leaks disabled and no scan roots, so RunTick exercises exactly the
// leak path. Returns the daemon and its (open) store.
func tickTestDaemon(t *testing.T) (*Daemon, *store.Store) {
	t.Helper()
	dir := t.TempDir()
	cfg := config.Defaults()
	cfg.Daemon.SocketPath = filepath.Join(dir, "noo.sock")
	cfg.Daemon.StorePath = filepath.Join(dir, "store.db")
	cfg.Notify.Enabled = false
	cfg.Heuristics.IdleRepos.Enabled = false
	cfg.Heuristics.CacheVelocity.Enabled = false
	cfg.Scan.Roots = nil
	cfg.Scan.CacheRoots = nil // never walk the real ~/Library/Caches in tests
	st, err := store.Open(cfg.Daemon.StorePath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return newDaemon(cfg, st), st
}

// swapLeakSource points leakSourceFn at a fake for the test's duration.
func swapLeakSource(t *testing.T, src heuristics.LeakSource) {
	t.Helper()
	old := leakSourceFn
	leakSourceFn = func(*Daemon) heuristics.LeakSource { return src }
	t.Cleanup(func() { leakSourceFn = old })
}

// captureNotifications swaps notifySendFn for a recorder.
func captureNotifications(t *testing.T) *notifyRecorder {
	t.Helper()
	rec := &notifyRecorder{}
	old := notifySendFn
	notifySendFn = rec.send
	t.Cleanup(func() { notifySendFn = old })
	return rec
}

type notifyRecorder struct {
	mu     sync.Mutex
	titles []string
	bodies []string
}

func (r *notifyRecorder) send(title, body, sound string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.titles = append(r.titles, title)
	r.bodies = append(r.bodies, body)
	return nil
}

// TestRunTickLeaksFireOnBothTriggers is the founding-loop closure proof: a
// stale leak-signature hit becomes an open "leaks" suggestion within ONE
// RunTick, for the daily AND the pressure trigger — the two triggers that
// actually reach RunTick in production (charter D15).
func TestRunTickLeaksFireOnBothTriggers(t *testing.T) {
	for _, trigger := range []TickTrigger{TriggerDaily, TriggerPressure} {
		t.Run(trigger.String(), func(t *testing.T) {
			d, st := tickTestDaemon(t)
			swapLeakSource(t, &tickFakeLeakSource{items: []modules.Item{tickStaleItem(5 << 30)}})

			if err := d.RunTick(context.Background(), trigger); err != nil {
				t.Fatalf("RunTick(%s): %v", trigger, err)
			}
			open, err := st.HasOpenSuggestion("leaks", tickTestTarget)
			if err != nil {
				t.Fatalf("HasOpenSuggestion: %v", err)
			}
			if !open {
				t.Fatalf("no open leaks suggestion for %s after RunTick(%s)", tickTestTarget, trigger)
			}

			// The store carry (charter D16/D17): size_bytes must be in the
			// persisted evidence so the round trip reads back a real size.
			rows, err := st.ListOpenSuggestions()
			if err != nil {
				t.Fatalf("ListOpenSuggestions: %v", err)
			}
			if len(rows) != 1 {
				t.Fatalf("open suggestions = %d, want 1", len(rows))
			}
			if got := rows[0].Evidence["size_bytes"]; got != "5368709120" {
				t.Errorf("persisted Evidence[size_bytes] = %q, want 5368709120", got)
			}
		})
	}
}

// TestRunTickLeakNotificationCopy pins the leak-named notification: it names
// the signature, the REAL reclaimable size, and the workaround command
// verbatim — and it does not fire twice for a target that is already an open
// suggestion (dedup rides persistNew's HasOpenSuggestion filter).
func TestRunTickLeakNotificationCopy(t *testing.T) {
	d, _ := tickTestDaemon(t)
	d.cfg.Notify.Enabled = true
	swapLeakSource(t, &tickFakeLeakSource{items: []modules.Item{tickStaleItem(2 << 30)}})
	rec := captureNotifications(t)

	if err := d.RunTick(context.Background(), TriggerDaily); err != nil {
		t.Fatalf("RunTick: %v", err)
	}
	if len(rec.bodies) != 1 {
		t.Fatalf("notifications sent = %d, want exactly 1 (leak-named, no generic)", len(rec.bodies))
	}
	if rec.titles[0] != "noo-noo — disk leak" {
		t.Errorf("title = %q, want %q", rec.titles[0], "noo-noo — disk leak")
	}
	body := rec.bodies[0]
	for _, want := range []string{"chrome-code-sign-clone", "2.0 GB", tickTestWorkaround} {
		if !strings.Contains(body, want) {
			t.Errorf("leak notification body %q missing %q", body, want)
		}
	}
	if strings.Contains(body, "new suggestion(s)") {
		t.Errorf("leak notification uses the generic copy: %q", body)
	}

	// Second tick, same open suggestion: dedup means no re-alert.
	if err := d.RunTick(context.Background(), TriggerDaily); err != nil {
		t.Fatalf("RunTick #2: %v", err)
	}
	if len(rec.bodies) != 1 {
		t.Fatalf("re-alerted on an already-open leak: %d notifications total, want 1", len(rec.bodies))
	}
}

// TestTickNotifyGenericCopyUnchanged pins the pre-existing generic copy for
// non-leak suggestions — the leak branch must not have touched it.
func TestTickNotifyGenericCopyUnchanged(t *testing.T) {
	d, _ := tickTestDaemon(t)
	d.cfg.Notify.Enabled = true
	rec := captureNotifications(t)

	d.tickNotify(0, 0, 2)
	if len(rec.bodies) != 1 {
		t.Fatalf("notifications = %d, want 1", len(rec.bodies))
	}
	if got, want := rec.bodies[0], "2 new suggestion(s). Run noo-noo suggestions list."; got != want {
		t.Errorf("generic copy = %q, want %q", got, want)
	}
	if got, want := rec.titles[0], "noo-noo"; got != want {
		t.Errorf("generic title = %q, want %q", got, want)
	}
}

// TestLeakAlertsAggregatePerSignature: three hits of one signature produce
// ONE alert with the summed real size, not three notifications.
func TestLeakAlertsAggregatePerSignature(t *testing.T) {
	mk := func(target string, size int64) heuristics.Suggestion {
		return heuristics.Suggestion{
			Module: "leaks", Target: target, SizeBytes: size,
			Evidence: map[string]any{
				"signature":  "chrome-code-sign-clone",
				"workaround": tickTestWorkaround,
			},
		}
	}
	alerts := leakAlerts([]heuristics.Suggestion{
		mk("/a", 1<<30), mk("/b", 1<<30), mk("/c", 2<<30),
	})
	if len(alerts) != 1 {
		t.Fatalf("alerts = %d, want 1 per signature class", len(alerts))
	}
	body := alerts[0].Body
	for _, want := range []string{"chrome-code-sign-clone", "4.0 GB", "3 leak(s)", tickTestWorkaround} {
		if !strings.Contains(body, want) {
			t.Errorf("aggregated body %q missing %q", body, want)
		}
	}
}
