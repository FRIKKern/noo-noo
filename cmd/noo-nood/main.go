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
//   - Auto-clean writes its audit rows into auto_clean_events, which is part
//     of the embedded schema.sql (store schema v2). store.Open creates it on
//     boot, so the daemon needs no external migration runner.
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
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/FRIKKern/noo-noo/internal/autoclean"
	"github.com/FRIKKern/noo-noo/internal/config"
	"github.com/FRIKKern/noo-noo/internal/heuristics"
	"github.com/FRIKKern/noo-noo/internal/ipc"
	"github.com/FRIKKern/noo-noo/internal/notify"
	"github.com/FRIKKern/noo-noo/internal/pressure"
	"github.com/FRIKKern/noo-noo/internal/scan"
	"github.com/FRIKKern/noo-noo/internal/store"
)

const version = "0.7.0"

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
	// autoCleanCfg is a live mutable snapshot of cfg.AutoClean handed to
	// the IPC AutoCleanService. Toggle mutates this in place so the next
	// tick observes the change without a config reload. Nil-safe: tick.go
	// falls back to cfg.AutoClean when this is nil (legacy test path).
	autoCleanCfg *ipc.AutoCleanConfig
	// lastTickNew is the new-suggestion count of the most recent tick,
	// recorded by RunTick for TriggerScan's reply.
	lastTickNew atomic.Int64
	// tickMu serializes RunTick across its two entry points (the trig
	// channel consumer and manualKicker's synchronous IPC path). Proven
	// necessary the hard way: an unserialized manual tick overlapping a
	// pressure tick deadlocked both on the store's single-writer lock and
	// froze the daemon at zero CPU with no further tick logs.
	tickMu sync.Mutex
	// leakFirstSeen is the storm detector's fallback appearance time for
	// leak paths without a filesystem birth time (see leakAppearedAt).
	// Guarded by tickMu; lazily allocated.
	leakFirstSeen map[string]time.Time
}

// manualKicker satisfies ipc.SchedulerKicker by running one manual tick
// synchronously. It bypasses the trig channel on purpose: the caller of
// `daemon force-scan` wants the scan's outcome in the reply, not a queued
// promise. RunTick's tickMu makes the overlap with a concurrently-scheduled
// tick safe: the manual tick simply waits its turn.
type manualKicker struct {
	d   *Daemon
	ctx context.Context
}

func (k *manualKicker) TriggerNow() (int, time.Duration, error) {
	start := time.Now()
	err := runTickFn(k.d, k.ctx, TriggerManual)
	return int(k.d.lastTickNew.Load()), time.Since(start), err
}

// newDaemon retains the lowercase constructor name so existing tests
// (main_test.go, e2e_test.go) keep compiling.
func newDaemon(cfg config.Config, st *store.Store) *Daemon {
	return &Daemon{cfg: cfg, store: st, now: time.Now}
}

// autoCleanConfigFromCfg snapshots the loaded TOML [auto_clean] section
// into the IPC service's struct. The two are intentionally distinct
// types (config.AutoCleanCfg lives in internal/config; ipc.AutoCleanConfig
// is re-declared in internal/ipc to avoid an import cycle); this adapter
// is the single conversion point.
func autoCleanConfigFromCfg(c config.AutoCleanCfg) ipc.AutoCleanConfig {
	return ipc.AutoCleanConfig{
		Enabled:            c.Enabled,
		RiskAcknowledgedAt: c.RiskAcknowledgedAt,
		ModulesAllowed:     append([]string(nil), c.ModulesAllowed...),
		MinIdleDays:        c.MinIdleDays,
		MinSizeMB:          c.MinSizeMB,
		SizeCapPerTickGB:   c.SizeCapPerTickGB,
	}
}

// Run brings up the IPC server, the daily scheduler, and the pressure
// watcher, then blocks until ctx is canceled. Returns ctx.Err() on clean
// shutdown (which the caller is expected to treat as success).
func (d *Daemon) Run(ctx context.Context) error {
	d.started = d.now()

	// Build a live, mutable snapshot of cfg.AutoClean for the IPC service.
	// Toggle (called by `noo-noo auto-clean enable/disable`) mutates this
	// struct in place; tick.go reads it via autoCleanCfgFn so the very next
	// tick observes the change without a daemon restart.
	acSnap := autoCleanConfigFromCfg(d.cfg.AutoClean)
	d.autoCleanCfg = &acSnap

	// Point tick.go's autoCleanCfgFn at the live, IPC-mutable snapshot.
	// Without this, tick.go's default returns Enabled=false unconditionally
	// and `noo-noo auto-clean enable` would have no effect on tick behavior.
	autoCleanCfgFn = func(_ config.Config) autoclean.Config {
		return autoclean.Config{
			Enabled:            acSnap.Enabled,
			RiskAcknowledgedAt: acSnap.RiskAcknowledgedAt,
			ModulesAllowed:     acSnap.ModulesAllowed,
			MinIdleDays:        acSnap.MinIdleDays,
			MinSizeMB:          acSnap.MinSizeMB,
			SizeCapPerTickGB:   acSnap.SizeCapPerTickGB,
		}
	}

	handlers := ipc.Handlers{
		Report:      &ipc.ReportService{Store: d.store},
		Suggestions: &ipc.SuggestionsService{Store: d.store},
		Clean:       &ipc.CleanService{Store: d.store},
		Daemon: (&ipc.DaemonService{
			StartedAt: func() time.Time { return d.started },
			Version:   version,
		}).WithScheduler(&manualKicker{d: d, ctx: ctx}),
		// stats=d.store: *store.Store implements AutoCleanStatsSince, so
		// AutoClean.Status reports the real 7-day deletion count and freed
		// bytes from the auto_clean_events ledger (zero, honestly, until the
		// first daily auto-clean runs). save=nil: Toggle persists in memory
		// only for now (backlog nn-bl-toggle-persist); restart re-reads TOML.
		AutoClean: ipc.NewAutoCleanService(&acSnap, d.store, nil),
	}
	srv := ipc.NewServer(d.cfg.Daemon.SocketPath, handlers)
	if err := srv.Start(ctx); err != nil {
		if errors.Is(err, ipc.ErrAlreadyRunning) {
			// Another noo-nood owns the socket. Return it bare so main's
			// message is the user-facing one; nothing on disk was touched.
			return err
		}
		return fmt.Errorf("ipc start: %w", err)
	}
	defer srv.Stop()

	// Pressure triggers feed this channel. Buffered so a pressure event
	// arriving while a tick is in flight is not dropped on the floor;
	// extras beyond capacity are intentionally dropped (the consumer will
	// pick up the next sample anyway).
	trig := make(chan TickTrigger, 4)

	// The daily tick gets its OWN reserved lane. On a machine that lives at
	// the pressure threshold, pressure ticks kept trig full around the
	// clock and the 03:00 daily — the only tick that auto-cleans — was
	// dropped and rescheduled a full day out (observed live: "dropping
	// daily tick" while the disk was refilling). Capacity 1: a second
	// daily arriving before the first is consumed is genuinely redundant.
	daily := make(chan TickTrigger, 1)

	// Daily cron at d.cfg.Daemon.ScanHour.
	schedDone := make(chan struct{})
	go func() {
		defer close(schedDone)
		d.runScheduler(ctx, daily)
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
		run := func(t TickTrigger) {
			if err := runTickFn(d, ctx, t); err != nil {
				log.Printf("tick: %v", err)
			}
		}
		for {
			// Daily first, non-blocking: with pressure firing every few
			// minutes, a fair select would make the daily wait a coin-flip
			// per cycle; the reserved lane plus this priority check makes
			// it deterministic.
			select {
			case t := <-daily:
				run(t)
				continue
			default:
			}
			select {
			case <-ctx.Done():
				return
			case t := <-daily:
				run(t)
			case t := <-trig:
				run(t)
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
				// The reserved daily lane (cap 1) is only ever full when a
				// previous daily is still waiting to be consumed — running
				// two back-to-back would be redundant, not a loss.
				log.Printf("scheduler: previous daily tick still queued; not stacking another")
			}
		}
	}
}

// runPressureWatcher runs the sustained-high-pressure sampler and posts a
// pressure trigger when it fires. The thresholds come straight from
// cfg.Pressure — the config defaults (mem 0.85, disk 10 GB, 15 s sample,
// 60 s debounce) are the live values, and a user's [pressure] overrides
// take effect without a code change. Seconds are widened to Durations here,
// the single conversion point between the TOML surface and pressure.Watch.
func (d *Daemon) runPressureWatcher(ctx context.Context, trig chan<- TickTrigger) {
	th := pressure.Threshold{
		MemHighRatio:   d.cfg.Pressure.MemHighRatio,
		DiskLowGB:      d.cfg.Pressure.DiskLowGB,
		SampleInterval: time.Duration(d.cfg.Pressure.SampleIntervalSeconds) * time.Second,
		DebounceWindow: time.Duration(d.cfg.Pressure.DebounceSeconds) * time.Second,
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

	if err := scan.ScanRoots(ctx, scan.Roots{Repos: d.cfg.Scan.Roots, Caches: d.cfg.Scan.CacheRoots}, d.store); err != nil {
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

// Compile-time assertions that *store.Store satisfies both audit interfaces:
// EventStore (autoclean writes its before/after rows through it) and
// AutoCleanStatsStore (the IPC AutoClean.Status read side). Anchored here, at
// the single wiring site, so a signature drift on either surface fails
// `go vet`/build rather than surfacing at runtime.
var (
	_ autoclean.EventStore    = (*store.Store)(nil)
	_ ipc.AutoCleanStatsStore = (*store.Store)(nil)
)

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
		if errors.Is(err, ipc.ErrAlreadyRunning) {
			// Second instance (e.g. a bare `noo-nood` beside the launchd
			// one): refuse loudly, exit without touching the live socket.
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		log.Fatalf("daemon: %v", err)
	}
	log.Printf("noo-nood: shutdown clean")
}
