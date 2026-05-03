// Package main is the noo-noo background daemon (noo-nood). It loads the
// user config, opens the SQLite store, listens on a Unix socket for IPC,
// and runs a once-a-day scheduler tick that re-runs the heuristic scorers
// and persists any new suggestions.
//
// Resource budget: idle <30 MB RSS, <0.1% CPU. The scheduler is one
// time.Timer; no busy loop, no inotify.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/FRIKKern/noo-noo/internal/config"
	"github.com/FRIKKern/noo-noo/internal/heuristics"
	"github.com/FRIKKern/noo-noo/internal/ipc"
	"github.com/FRIKKern/noo-noo/internal/notify"
	"github.com/FRIKKern/noo-noo/internal/store"
)

const version = "0.2.0"

// daemon owns the long-running daemon state. Construct via newDaemon and
// drive with Run; Run blocks until ctx is canceled.
type daemon struct {
	cfg     config.Config
	store   *store.Store
	started time.Time
	now     func() time.Time // injectable for tests
}

func newDaemon(cfg config.Config, st *store.Store) *daemon {
	return &daemon{cfg: cfg, store: st, now: time.Now}
}

// Run brings up the IPC server and the daily scheduler, then blocks until
// ctx is canceled. Returns ctx.Err() on clean shutdown (which the caller
// is expected to treat as success).
func (d *daemon) Run(ctx context.Context) error {
	d.started = d.now()

	handlers := ipc.Handlers{
		Report:      &ipc.ReportService{Store: d.store},
		Suggestions: &ipc.SuggestionsService{Store: d.store},
		Clean:       &ipc.CleanService{Store: d.store},
		Daemon: &ipc.DaemonService{
			StartedAt: func() time.Time { return d.started },
			Version:   version,
		},
	}
	srv := ipc.NewServer(d.cfg.Daemon.SocketPath, handlers)
	if err := srv.Start(ctx); err != nil {
		return fmt.Errorf("ipc start: %w", err)
	}
	defer srv.Stop()

	// Scheduler runs in its own goroutine and exits when ctx is canceled.
	schedDone := make(chan struct{})
	go func() {
		defer close(schedDone)
		d.runScheduler(ctx)
	}()

	<-ctx.Done()
	<-schedDone
	return ctx.Err()
}

// runScheduler waits until the next configured scan hour, runs the scan,
// then re-arms. Exits when ctx is canceled.
func (d *daemon) runScheduler(ctx context.Context) {
	for {
		next := nextTickAt(d.now(), d.cfg.Daemon.ScanHour)
		wait := time.Until(next)
		log.Printf("scheduler: next scan at %s (in %s)", next.Format(time.RFC3339), wait)
		t := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-t.C:
			d.runScan(ctx)
		}
	}
}

// runScan executes the enabled heuristics, persists any new suggestions
// (deduped against currently-open ones), and posts a notification when at
// least one new suggestion landed.
func (d *daemon) runScan(ctx context.Context) {
	log.Printf("scheduler: running daily scan")
	var all []heuristics.Suggestion
	if d.cfg.Heuristics.IdleRepos.Enabled {
		all = append(all, heuristics.IdleRepos(ctx, d.store, d.cfg)...)
	}
	if d.cfg.Heuristics.CacheVelocity.Enabled {
		all = append(all, heuristics.CacheVelocity(ctx, d.store, d.cfg)...)
	}

	inserted := 0
	for _, s := range all {
		open, err := d.store.HasOpenSuggestion(s.Module, s.Target)
		if err != nil {
			log.Printf("scheduler: dedupe check: %v", err)
			continue
		}
		if open {
			continue
		}
		stored := toStored(s)
		if _, err := d.store.InsertSuggestion(stored); err != nil {
			log.Printf("scheduler: insert suggestion: %v", err)
			continue
		}
		inserted++
	}
	log.Printf("scheduler: scan done; %d new suggestion(s) (%d candidate)", inserted, len(all))

	if d.cfg.Notify.Enabled && inserted > 0 {
		title := "noo-noo"
		body := fmt.Sprintf("%d new suggestion(s). Run noo-noo suggestions list.", inserted)
		if err := notify.Send(title, body, ""); err != nil {
			log.Printf("notify: %v", err)
		}
	}
}

// toStored converts a heuristics.Suggestion (rich evidence) into the
// store row shape (string-keyed map + severity string).
func toStored(s heuristics.Suggestion) store.StoredSuggestion {
	ev := make(map[string]string, len(s.Evidence))
	for k, v := range s.Evidence {
		switch x := v.(type) {
		case string:
			ev[k] = x
		default:
			b, err := json.Marshal(v)
			if err != nil {
				ev[k] = fmt.Sprintf("%v", v)
				continue
			}
			ev[k] = string(b)
		}
	}
	ts := s.CreatedAt
	if ts.IsZero() {
		ts = time.Now()
	}
	return store.StoredSuggestion{
		Ts:       ts,
		Module:   s.Module,
		Target:   s.Target,
		Reason:   s.Reason,
		Evidence: ev,
		Severity: string(s.RiskLevel),
	}
}

// nextTickAt returns the next time-of-day at hour, given now. If the hour
// is already past (or exactly equal) today, returns hour tomorrow.
func nextTickAt(now time.Time, hour int) time.Time {
	candidate := time.Date(now.Year(), now.Month(), now.Day(), hour, 0, 0, 0, now.Location())
	if !candidate.After(now) {
		candidate = candidate.Add(24 * time.Hour)
	}
	return candidate
}

func main() {
	home, _ := os.UserHomeDir()
	cfgPath := filepath.Join(home, ".config", "noo-noo", "config.toml")
	cfg, err := config.Load(cfgPath)
	if err != nil {
		log.Fatalf("load config: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(cfg.Daemon.StorePath), 0o755); err != nil {
		log.Fatalf("mkdir store dir: %v", err)
	}
	st, err := store.Open(cfg.Daemon.StorePath)
	if err != nil {
		log.Fatalf("open store: %v", err)
	}
	defer func() { _ = st.Close() }()

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	log.Printf("noo-nood %s starting; socket=%s store=%s",
		version, cfg.Daemon.SocketPath, cfg.Daemon.StorePath)
	if err := newDaemon(cfg, st).Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		log.Fatalf("daemon: %v", err)
	}
	log.Printf("noo-nood: shutdown clean")
}
