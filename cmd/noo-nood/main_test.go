package main

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/FRIKKern/noo-noo/internal/config"
	"github.com/FRIKKern/noo-noo/internal/store"
)

func TestStartShutdown(t *testing.T) {
	dir := t.TempDir()
	cfg := config.Defaults()
	cfg.Daemon.SocketPath = filepath.Join(dir, "noo.sock")
	cfg.Daemon.StorePath = filepath.Join(dir, "store.db")
	cfg.Notify.Enabled = false
	// Disable heuristics so the scheduler tick (if it ever fires in test) is
	// a no-op. The 100ms sleep below shouldn't cross 03:00 anyway, but be safe.
	cfg.Heuristics.IdleRepos.Enabled = false
	cfg.Heuristics.CacheVelocity.Enabled = false

	st, err := store.Open(cfg.Daemon.StorePath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = st.Close() }()

	ctx, cancel := context.WithCancel(context.Background())
	d := newDaemon(cfg, st)

	done := make(chan error, 1)
	go func() { done <- d.Run(ctx) }()

	// Give the daemon a moment to bring up IPC + scheduler.
	time.Sleep(100 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if err != nil && err != context.Canceled {
			t.Errorf("daemon Run returned: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("daemon did not shut down within 2s")
	}
}

func TestNextTickAt(t *testing.T) {
	// Hour not yet reached today: next tick is today at hour.
	now := time.Date(2026, 5, 2, 1, 30, 0, 0, time.UTC)
	got := nextTickAt(now, 3)
	want := time.Date(2026, 5, 2, 3, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("nextTickAt(%v, 3) = %v, want %v", now, got, want)
	}

	// Hour already passed today: next tick is tomorrow at hour.
	now2 := time.Date(2026, 5, 2, 14, 30, 0, 0, time.UTC)
	got2 := nextTickAt(now2, 3)
	want2 := time.Date(2026, 5, 3, 3, 0, 0, 0, time.UTC)
	if !got2.Equal(want2) {
		t.Errorf("nextTickAt(%v, 3) = %v, want %v", now2, got2, want2)
	}

	// Exactly at the hour boundary: roll forward 24h to avoid double-firing.
	now3 := time.Date(2026, 5, 2, 3, 0, 0, 0, time.UTC)
	got3 := nextTickAt(now3, 3)
	want3 := time.Date(2026, 5, 3, 3, 0, 0, 0, time.UTC)
	if !got3.Equal(want3) {
		t.Errorf("nextTickAt(%v, 3) = %v, want %v", now3, got3, want3)
	}
}

func TestRunScanRespectsDisabledHeuristics(t *testing.T) {
	dir := t.TempDir()
	cfg := config.Defaults()
	cfg.Daemon.SocketPath = filepath.Join(dir, "noo.sock")
	cfg.Daemon.StorePath = filepath.Join(dir, "store.db")
	cfg.Notify.Enabled = false
	cfg.Heuristics.IdleRepos.Enabled = false
	cfg.Heuristics.CacheVelocity.Enabled = false

	st, err := store.Open(cfg.Daemon.StorePath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = st.Close() }()

	d := newDaemon(cfg, st)
	// Should be a no-op and never panic when both heuristics are disabled.
	d.runScan(context.Background())

	rows, err := st.ListOpenSuggestions()
	if err != nil {
		t.Fatalf("list suggestions: %v", err)
	}
	if len(rows) != 0 {
		t.Errorf("expected 0 suggestions, got %d", len(rows))
	}
}
