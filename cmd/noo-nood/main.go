// Package main is the noo-noo background daemon (noo-nood). It loads the
// user config, opens the SQLite store, listens on a Unix socket for IPC,
// and runs a once-a-day scheduler tick that re-runs the heuristic scorers
// and persists any new suggestions.
//
// Phase 0.5 additions:
//   - The pressure watcher samples vmstat + statfs and fires an out-of-band
//     scan when sustained-high pressure is detected. Daily-cron and pressure
//     triggers both fan into a single tick channel; the consumer dispatches
//     to runTickFn (a function variable that tick.go swaps to the
//     autoclean-aware body once T95 lands).
//   - Auto-clean writes its audit rows into auto_clean_events, a table not
//     present in the embedded schema.sql. applyMigrations runs the 0005
//     migration inline at boot so the daemon does not depend on an external
//     migration runner.
//
// Resource budget: idle <30 MB RSS, <0.1% CPU. The scheduler is one
// time.Timer; the pressure watcher is a single time.Ticker; no busy loops
// or inotify.
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
	"github.com/FRIKKern/noo-noo/internal/pressure"
	"github.com/FRIKKern/noo-noo/internal/scan"
	"github.com/FRIKKern/noo-noo/internal/store"
)

const version = "0.5.0-dev"

// TickTrigger is why a tick is firing. Used by tick.go to gate which steps
// run (e.g. autoclean is daily-only by design).
type TickTrigger int

const (
	// TriggerDaily is the regular 03:00 cron tick. Auto-clean (when enabled)
	// runs only on this trigger.
	TriggerDaily TickTrigger = iota
	// TriggerPressure is fired by the pressure watcher when sustained-high
	// memory or low-disk is observed. Never auto-cleans.
	TriggerPressure
	// TriggerManual is reserved for `noo-noo daemon trigger-scan` IPC calls.
	TriggerManual
)

// String renders a TickTrigger for log lines. Keeps grep-ability of
// "trigger=daily" / "trigger=pressure" stable across log infrastructure.
func (t TickTrigger) String() string {
	switch t {
	case TriggerDaily:
		return "daily"
	case TriggerPressure:
		return "pressure"
	case TriggerManual:
		return "manual"
	default:
		return fmt.Sprintf("unknown(%d)", int(t))
	}
}

// runTickFn is the per-tick handler. T94 ships the legacy body (scan +
// heuristics + notify); T95's tick.go init() swaps in the autoclean-aware
// body so the wiring remains a single function-pointer indirection rather
// than method-renaming gymnastics across two commits.
var runTickFn = func(d *Daemon, ctx context.Context, t TickTrigger) error {
	d.runScan(ctx, t)
	return nil
}

// runScan adapter used by main_test.go which still passes only ctx.
// Variadic trigger keeps the test compile-clean while letting the new
// scheduler pass the actual trigger. Default is TriggerDaily so a
// trigger-less call from tests behaves the way it always has.

// Daemon owns the long-running daemon state. Construct via newDaemon and
// drive with Run; Run blocks until ctx is canceled. The exported name
// (capital D) is required so tick.go (T95) can hang RunTick off it.
type Daemon struct {
	cfg     config.Config
	store   *store.Store
	started time.Time
	now     func() time.Time // injectable for tests
}

// newDaemon retains the lowercase constructor name so existing tests
// (main_test.go, e2e_test.go) keep compiling.
func newDaemon(cfg config.Config, st *store.Store) *Daemon {
	return &Daemon{cfg: cfg, store: st, now: time.Now}
}

// Run brings up the IPC server, the daily scheduler, and the pressure
// watcher, then blocks until ctx is canceled. Returns ctx.Err() on clean
// shutdown (which the caller is expected to treat as success).
func (d *Daemon) Run(ctx context.Context) error {
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

	// Single channel both triggers feed into. Buffered so a pressure event
	// arriving while a daily tick is in flight is not dropped on the floor;
	// extras beyond capacity are intentionally dropped (the consumer will
	// pick up the next sample anyway).
	trig := make(chan TickTrigger, 4)

	// Daily cron at d.cfg.Daemon.ScanHour.
	schedDone := make(chan struct{})
	go func() {
		defer close(schedDone)
		d.runScheduler(ctx, trig)
	}()

	// Pressure watcher (only spins up if both samplers can be constructed
	// without panicking; Watch handles per-tick errors internally).
	pressureDone := make(chan struct{})
	go func() {
		defer close(pressureDone)
		d.runPressureWatcher(ctx, trig)
	}()

	// Tick consumer. Both daily and pressure triggers come through here,
	// so all per-tick safety logic (autoclean trigger gating in T95) lives
	// downstream of this single dispatch point.
	consumerDone := make(chan struct{})
	go func() {
		defer close(consumerDone)
		for {
			select {
			case <-ctx.Done():
				return
			case t := <-trig:
				if err := runTickFn(d, ctx, t); err != nil {
					log.Printf("tick: %v", err)
				}
			}
		}
	}()

	<-ctx.Done()
	<-schedDone
	<-pressureDone
	<-consumerDone
	return ctx.Err()
}

// runScheduler waits until the next configured scan hour, posts a daily
// trigger, then re-arms. Exits when ctx is canceled.
func (d *Daemon) runScheduler(ctx context.Context, trig chan<- TickTrigger) {
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
			select {
			case trig <- TriggerDaily:
			default:
				// Backpressure: the consumer is busy; drop the daily tick
				// rather than blocking the scheduler. The next 24h cycle
				// will re-fire.
				log.Printf("scheduler: trigger channel full; dropping daily tick")
			}
		}
	}
}

// runPressureWatcher runs the sustained-high-pressure sampler and posts a
// pressure trigger when it fires. Uses defaults that match the
// internal/pressure package's design (5 GB low-disk, 95% mem-high, 30 s
// debounce window with a 5 min internal cooldown).
func (d *Daemon) runPressureWatcher(ctx context.Context, trig chan<- TickTrigger) {
	th := pressure.Threshold{
		MemHighRatio:   0.95,
		DiskLowGB:      5,
		SampleInterval: 10 * time.Second,
		DebounceWindow: 30 * time.Second,
	}
	pressure.Watch(ctx, th, func() {
		log.Printf("pressure: sustained-high; firing out-of-band scan")
		select {
		case trig <- TriggerPressure:
		default:
			log.Printf("pressure: trigger channel full; dropping pressure tick")
		}
	})
}

// runScan executes the enabled heuristics, persists any new suggestions
// (deduped against currently-open ones), and posts a notification when at
// least one new suggestion landed.
//
// Phase 0.5: precedes heuristics with scan.ScanRoots so the data the
// heuristics score is fresh for THIS tick rather than whatever was left
// in the store from a long-ago `noo-noo scan`.
func (d *Daemon) runScan(ctx context.Context, triggers ...TickTrigger) {
	trigger := TriggerDaily
	if len(triggers) > 0 {
		trigger = triggers[0]
	}
	log.Printf("scheduler: running scan (trigger=%s)", trigger)

	if err := scan.ScanRoots(ctx, scan.Roots{Repos: d.cfg.Scan.Roots}, d.store); err != nil {
		log.Printf("scheduler: scan: %v", err)
	}

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

// applyAutoCleanMigration creates the auto_clean_events table inline. The
// embedded schema.sql in internal/store does not yet include it (that
// table arrived in Phase 0.5 as 0005_auto_clean_events.sql), so until the
// store package grows a generic migration runner the daemon applies just
// this one migration at boot. CREATE IF NOT EXISTS makes it idempotent.
func applyAutoCleanMigration(st *store.Store) error {
	const stmt = `
CREATE TABLE IF NOT EXISTS auto_clean_events (
    id                     INTEGER PRIMARY KEY AUTOINCREMENT,
    started_at_unix        INTEGER NOT NULL,
    ended_at_unix          INTEGER,
    trigger                TEXT NOT NULL,
    outcome                TEXT NOT NULL,
    skip_reason            TEXT,
    target_path            TEXT NOT NULL,
    module                 TEXT NOT NULL,
    target_size_bytes      INTEGER NOT NULL,
    freed_bytes            INTEGER NOT NULL DEFAULT 0,
    idle_days_at_decision  INTEGER NOT NULL DEFAULT 0,
    suggestion_id          TEXT NOT NULL,
    error_msg              TEXT
);
CREATE INDEX IF NOT EXISTS idx_auto_clean_started ON auto_clean_events(started_at_unix);
CREATE INDEX IF NOT EXISTS idx_auto_clean_outcome ON auto_clean_events(outcome);
`
	if _, err := st.DB().Exec(stmt); err != nil {
		return fmt.Errorf("apply 0005 auto_clean_events: %w", err)
	}
	return nil
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

	if err := applyAutoCleanMigration(st); err != nil {
		log.Fatalf("migrate: %v", err)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	log.Printf("noo-nood %s starting; socket=%s store=%s",
		version, cfg.Daemon.SocketPath, cfg.Daemon.StorePath)
	if err := newDaemon(cfg, st).Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		log.Fatalf("daemon: %v", err)
	}
	log.Printf("noo-nood: shutdown clean")
}
