package main

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/FRIKKern/noo-noo/internal/config"
	"github.com/FRIKKern/noo-noo/internal/ipc"
	"github.com/FRIKKern/noo-noo/internal/store"
)

// TestEndToEnd boots a real noo-nood daemon against a temp socket+DB and
// exercises every IPC method through the typed client. It is the integration
// glue test for Phase 0.2: if any of the per-task unit tests pass but this
// fails, the regression is in the wiring between layers, not within a layer.
func TestEndToEnd(t *testing.T) {
	dir := t.TempDir()
	cfg := config.Defaults()
	cfg.Daemon.SocketPath = filepath.Join(dir, "noo.sock")
	cfg.Daemon.StorePath = filepath.Join(dir, "store.db")
	cfg.Notify.Enabled = false
	// Disable the heuristics so the scheduler tick (if it ever fires inside
	// the test window) is a no-op; we don't want IdleRepos walking the real
	// homedir during `go test`.
	cfg.Heuristics.IdleRepos.Enabled = false
	cfg.Heuristics.CacheVelocity.Enabled = false

	st, err := store.Open(cfg.Daemon.StorePath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = st.Close() }()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	d := newDaemon(cfg, st)
	errCh := make(chan error, 1)
	go func() { errCh <- d.Run(ctx) }()

	// Wait for the socket to come up. The daemon brings up IPC inside Run,
	// so a tight poll is the simplest correct sync (no exported "ready"
	// signal, and time.Sleep alone is racy).
	c := dialReady(t, cfg.Daemon.SocketPath, 2*time.Second)
	defer func() { _ = c.Close() }()

	// Each sub-helper exercises one IPC namespace and asserts the wire
	// round-trip is coherent (no panics, sane reply, no error where one
	// isn't expected). Splitting them out also keeps gocyclo happy.
	checkDaemonStatus(t, c)
	checkSuggestionsList(t, c)
	checkReportFull(t, c)
	checkSuggestionsDismissMissing(t, c)
	checkCleanExecuteEmpty(t, c)

	// Graceful shutdown: cancel the context, expect Run to return ctx.Err()
	// (or nil) within the 2s budget.
	cancel()
	select {
	case err := <-errCh:
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Errorf("daemon Run returned %v, want nil or context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("daemon did not shut down within 2s of context cancel")
	}
}

func checkDaemonStatus(t *testing.T, c *ipc.Client) {
	t.Helper()
	status, err := c.DaemonStatus()
	if err != nil {
		t.Fatalf("DaemonStatus: %v", err)
	}
	if !status.Running {
		t.Errorf("Status.Running = false, want true")
	}
	if status.Version == "" {
		t.Error("Status.Version is empty")
	}
}

func checkSuggestionsList(t *testing.T, c *ipc.Client) {
	t.Helper()
	sugs, err := c.SuggestionsList()
	if err != nil {
		t.Fatalf("SuggestionsList: %v", err)
	}
	if len(sugs) != 0 {
		t.Errorf("SuggestionsList len = %d, want 0", len(sugs))
	}
}

func checkReportFull(t *testing.T, c *ipc.Client) {
	t.Helper()
	rep, err := c.ReportFull()
	if err != nil {
		t.Fatalf("ReportFull: %v", err)
	}
	if rep.GeneratedAt.IsZero() {
		t.Error("Report.GeneratedAt is zero")
	}
	if len(rep.OpenSuggestions) != 0 {
		t.Errorf("Report.OpenSuggestions len = %d, want 0", len(rep.OpenSuggestions))
	}
}

func checkSuggestionsDismissMissing(t *testing.T, c *ipc.Client) {
	t.Helper()
	// The daemon's pre-check rejects unknown ids explicitly; a silent no-op
	// here would mask CLI breakage.
	if err := c.SuggestionsDismiss(99999); err == nil {
		t.Error("Dismiss(99999) returned nil error, want non-nil")
	}
}

func checkCleanExecuteEmpty(t *testing.T, c *ipc.Client) {
	t.Helper()
	cleanRes, err := c.CleanExecute(nil)
	if err != nil {
		t.Fatalf("CleanExecute(nil): %v", err)
	}
	if cleanRes.BytesFreed != 0 {
		t.Errorf("CleanExecute(nil).BytesFreed = %d, want 0", cleanRes.BytesFreed)
	}
	if cleanRes.Targets != 0 {
		t.Errorf("CleanExecute(nil).Targets = %d, want 0", cleanRes.Targets)
	}
}

// dialReady polls the socket until ipc.Dial succeeds or the deadline elapses.
// Keeps the wait loop out of the test body so the actual assertions read
// linearly.
func dialReady(t *testing.T, socketPath string, within time.Duration) *ipc.Client {
	t.Helper()
	deadline := time.Now().Add(within)
	for {
		c, err := ipc.Dial(socketPath)
		if err == nil {
			return c
		}
		if time.Now().After(deadline) {
			t.Fatalf("socket %s never came up within %s: %v", socketPath, within, err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
