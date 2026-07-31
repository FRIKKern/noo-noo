package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/FRIKKern/noo-noo/internal/autoclean"
	"github.com/FRIKKern/noo-noo/internal/config"
	"github.com/FRIKKern/noo-noo/internal/core"
	"github.com/FRIKKern/noo-noo/internal/heuristics"
	"github.com/FRIKKern/noo-noo/internal/leaks"
	"github.com/FRIKKern/noo-noo/internal/modules"
	"github.com/FRIKKern/noo-noo/internal/modules/offload"
	"github.com/FRIKKern/noo-noo/internal/notify"
	"github.com/FRIKKern/noo-noo/internal/scan"
	"github.com/FRIKKern/noo-noo/internal/store"
	"github.com/FRIKKern/noo-noo/internal/worktrees"
)

// Compile-time assertion: *store.Store satisfies the relocation-queue surface
// the offload pending runner consumes. Anchored here, next to the daemon
// wiring that passes it in (mirrors main.go's EventStore assertion).
var _ offload.QueueStore = (*store.Store)(nil)

// init swaps the package-level runTickFn (defined in main.go) so the tick
// consumer goroutine dispatches to RunTick — the autoclean-aware body
// shipped with this file. Keeps T94's main.go untouched while T95 lights
// up the new behavior.
func init() {
	runTickFn = func(d *Daemon, ctx context.Context, t TickTrigger) error {
		return d.RunTick(ctx, t)
	}
}

// autoCleanCfgFn returns the autoclean config snapshot for a given
// daemon config. It defaults to a disabled snapshot so this code path is
// inert until T97 extends config.Config with an [auto_clean] section and
// reassigns this function. Keeping the indirection here means T97 only
// has to touch internal/config (and reassign in init), not tick.go.
var autoCleanCfgFn = func(_ config.Config) autoclean.Config {
	return autoclean.Config{} // Enabled: false — engine refuses every action.
}

// leakSourceFn builds the leak-signature source the tick's Leaks heuristic
// scans. A function variable (same pattern as autoCleanCfgFn/runTickFn) so
// tests can substitute a fake source without touching real system paths or
// spawning lsof. The default is the shipped registry behind the same Safety
// the autoclean branch uses; the heuristic only ever calls Scan+Plan on it —
// diagnose-only by construction.
var leakSourceFn = func(d *Daemon) heuristics.LeakSource {
	return leaks.New(leaks.DefaultSignatures(), core.NewSafety(d.cfg.Scan.Roots, []string{".git"}))
}

// notifySendFn is the notification sink, injectable so tests can capture
// the exact user-facing copy instead of posting real macOS notifications.
var notifySendFn = notify.Send

// RunTick is the per-trigger entrypoint registered by init() above. Steps:
//
//  1. scan.ScanRoots — populate fresh data the heuristics will score.
//  2. Run enabled heuristics; persist new (deduped) suggestions.
//  3. If trigger == daily AND autoclean is enabled in config:
//     evaluate every fresh suggestion against the gates, run Apply for
//     each pass, and dismiss any suggestion whose target was actually
//     deleted so the user doesn't see stale entries.
//     3b. If trigger == daily AND the [offload] auto-apply pair is armed
//     (auto_apply_pending=true AND risk_acknowledged_at set): re-check
//     queued relocations with ALL gates fresh and apply AT MOST ONE
//     (budget 1/tick). Two-phase row updates around the apply; a
//     still-blocked entry is marked with its reason, source untouched.
//  4. Notify, with different copy for "freed N bytes" vs.
//     "M suggestions waiting"; a completed deferred relocation sends its
//     own "relocated X, freed Y internal" notification.
//
// Pressure-triggered ticks NEVER reach the autoclean branch OR the
// pending-relocation branch (pressure correlates with active dev work;
// safety design). Both engines also refuse any trigger != daily/manual
// themselves (errPressureTrigger / ErrPendingTriggerForbidden), so this
// is defense in depth.
func (d *Daemon) RunTick(ctx context.Context, trigger TickTrigger) error {
	// One tick at a time, whichever door it came through (scheduler
	// consumer, pressure watcher via the consumer, or the synchronous
	// force-scan RPC). Concurrent ticks deadlock on the store.
	d.tickMu.Lock()
	defer d.tickMu.Unlock()
	log.Printf("tick start: trigger=%s", trigger)

	// Pressure ticks are the LIGHT path: leaks only. A pressure tick exists
	// to catch a disk crisis, and the leak signatures are the only fast,
	// crisis-relevant heuristic; the full-cost passes (repo+cache walks,
	// worktree classification) belong to the daily tick. Measured before
	// this split: every pressure tick paid the full 5-8 minute scan, and
	// there were 113 of them in one day.
	light := trigger == TriggerPressure

	// Step 1: walk the filesystem -> populate fresh data.
	if !light {
		if err := scan.ScanRoots(ctx, scan.Roots{Repos: d.cfg.Scan.Roots, Caches: d.cfg.Scan.CacheRoots}, d.store); err != nil {
			log.Printf("tick: scan: %v", err)
		}
	}

	// Step 2: run heuristics over the fresh data.
	suggestions := d.collectSuggestions(ctx, light)

	// Persist (deduped) before deciding what to auto-clean — autoclean
	// dismisses by id, so we need real ids on the in-memory rows. The
	// updated slice has ID populated for every newly-inserted row.
	suggestions = d.persistNew(suggestions)
	d.lastTickNew.Store(int64(len(suggestions)))

	// Step 3: maybe auto-clean. Two preconditions:
	//   - daily trigger only (defense in depth — engine also enforces).
	//   - autoCleanCfgFn returns a config with Enabled=true.
	//
	// Cleanable = fresh suggestions PLUS still-open leak rows. Fresh-only
	// was a one-shot trap for leaks: a clone that is LIVE on the tick that
	// first proves a sibling stale files no row, and the row it DOES file
	// later is deduped as already-open forever after — 26 proven-stale
	// suggestions sat open while the disk ran to zero. Leak deletes are
	// safe to retry every tick because the leaks deleter re-proves
	// staleness at delete time; dev rows stay fresh-only.
	var freed int64
	var deleted int
	autoCfg := autoCleanCfgFn(d.cfg)
	if (trigger == TriggerDaily || trigger == TriggerManual) && autoCfg.Enabled {
		cleanable := append(suggestions, d.openLeakSuggestions(suggestions)...)
		freed, deleted = d.runAutoClean(ctx, autoCfg, cleanable, trigger.String())
	}

	// Step 3b: maybe apply ONE queued relocation, behind the full cascade.
	if ok, why := pendingAutoApplyAllowed(trigger, d.cfg.Offload); ok {
		d.runPendingRelocations(ctx)
	} else {
		log.Printf("pending-relocations: skipped (%s)", why)
	}

	// Step 4: notify. NEW leak suggestions get their own leak-named copy —
	// signature, real reclaimable bytes, and the one fixing command — while
	// everything else keeps the generic tick copy. persistNew already
	// deduped against open rows (HasOpenSuggestion), so a leak target alerts
	// once, not on every tick it stays unfixed.
	leakSugs, otherSugs := splitLeaks(suggestions)
	d.notifyLeaks(leakSugs)
	d.tickNotify(deleted, freed, len(otherSugs))
	return nil
}

// pendingAutoApplyAllowed is the daemon-side gate cascade for auto-applying
// queued relocations, mirroring autoclean's opt-in law. ALL of:
//
//  1. daily trigger only — a pressure tick fires exactly when the user's
//     machine is busiest; moving directories then is forbidden by design.
//  2. auto_apply_pending master switch (default OFF).
//  3. risk_acknowledged_at non-empty — a hand-flipped switch without the
//     acknowledgement timestamp is treated as not-acknowledged.
//
// Pure function: the returned reason string is the log/test-visible verdict.
func pendingAutoApplyAllowed(trigger TickTrigger, oc config.OffloadCfg) (bool, string) {
	if trigger != TriggerDaily {
		return false, "trigger_not_daily"
	}
	if !oc.AutoApplyPending {
		return false, "auto_apply_pending_off"
	}
	if oc.RiskAcknowledgedAt == "" {
		return false, "risk_not_acknowledged"
	}
	return true, ""
}

// runPendingRelocFn is the seam the cascade dispatches through — tests swap
// it to observe (or fake) the engine call without real volumes. The default
// runs the production engine: playbooks + deps from module defaults, the
// daemon's own store as the queue, budget 1 (at most one relocation per
// daily tick), trigger "daily" (the engine re-refuses anything else).
var runPendingRelocFn = func(d *Daemon, ctx context.Context) ([]offload.PendingResult, error) {
	m := offload.New(offload.Config{
		DestRoot:       d.cfg.Offload.DestRoot,
		DestVolumeUUID: d.cfg.Offload.DestVolumeUUID,
	}, nil, offload.Deps{})
	return m.RunPending(ctx, d.store, offload.TriggerPendingDaily, 1, d.now)
}

// runPendingRelocations runs the gated engine call and notifies on success
// with the brief's copy: "relocated X, freed Y internal". Blocked entries
// are logged (their reason already lives on the queue row for `offload
// pending` to show); they never notify — waking the user for "still
// blocked" would train them to ignore noo-noo.
func (d *Daemon) runPendingRelocations(ctx context.Context) {
	results, err := runPendingRelocFn(d, ctx)
	if err != nil {
		log.Printf("pending-relocations: %v", err)
		return
	}
	for _, r := range results {
		switch r.Outcome {
		case offload.PendingApplied:
			log.Printf("pending-relocations: relocated %s → %s (freed %s locally)",
				r.Entry.TargetPath, filepath.Join(r.Entry.DestRoot, r.Entry.PlaybookAssetID), humanBytes(int64(r.Freed)))
			if d.cfg.Notify.Enabled {
				body := fmt.Sprintf("relocated %s, freed %s internal",
					filepath.Base(r.Entry.TargetPath), humanBytes(int64(r.Freed)))
				if err := notify.Send("noo-noo", body, ""); err != nil {
					log.Printf("notify: %v", err)
				}
			}
		case offload.PendingBlocked:
			log.Printf("pending-relocations: #%d %s still blocked: %s",
				r.Entry.ID, r.Entry.TargetPath, r.Reason)
		default:
			log.Printf("pending-relocations: #%d %s skipped: %s",
				r.Entry.ID, r.Entry.TargetPath, r.Reason)
		}
	}
}

// splitLeaks partitions suggestions into leak-module rows and the rest, so
// the notification step can give leaks their signature-named copy without
// double-counting them in the generic "N new suggestion(s)" message.
func splitLeaks(in []heuristics.Suggestion) (leakSugs, others []heuristics.Suggestion) {
	for _, s := range in {
		if s.Module == "leaks" {
			leakSugs = append(leakSugs, s)
		} else {
			others = append(others, s)
		}
	}
	return leakSugs, others
}

// collectSuggestions runs every enabled heuristic and returns the union.
// Mirrors what main.go's runScan used to do; pulled into tick.go so the
// autoclean branch can see the same in-memory list (ids populated by
// persistNew).
func (d *Daemon) collectSuggestions(ctx context.Context, light bool) []heuristics.Suggestion {
	var all []heuristics.Suggestion
	if !light && d.cfg.Heuristics.IdleRepos.Enabled {
		all = append(all, heuristics.IdleRepos(ctx, d.store, d.cfg)...)
	}
	if !light && d.cfg.Heuristics.CacheVelocity.Enabled {
		all = append(all, heuristics.CacheVelocity(ctx, d.store, d.cfg)...)
	}
	if d.cfg.Heuristics.Leaks.Enabled {
		all = append(all, heuristics.Leaks(ctx, leakSourceFn(d), d.cfg)...)
	}
	if !light {
		all = append(all, heuristics.Worktrees(ctx, worktreeSourceFn(d), d.cfg.Worktrees.Enabled)...)
	}
	return all
}

// worktreeSourceFn builds the worktree sweeper the tick's heuristic scans —
// a function variable (same pattern as leakSourceFn) so tests can substitute
// a fake without real repos or lsof. [worktrees] roots fall back to the
// [scan] roots so one setting drives both.
var worktreeSourceFn = func(d *Daemon) heuristics.WorktreeSource {
	return worktrees.New(worktreeConfig(d))
}

// worktreeConfig snapshots the daemon config into the module's own type.
func worktreeConfig(d *Daemon) worktrees.Config {
	roots := d.cfg.Worktrees.Roots
	if len(roots) == 0 {
		roots = d.cfg.Scan.Roots
	}
	return worktrees.Config{
		Roots:    roots,
		MinIdle:  time.Duration(d.cfg.Worktrees.MinIdleHours) * time.Hour,
		JudgeCmd: d.cfg.Worktrees.JudgeCmd,
		GraveDir: d.cfg.Worktrees.GraveDir,
	}
}

// persistNew inserts each suggestion that is not already represented by
// an open row, populating Suggestion.ID with the row id of the inserted
// row. Suggestions that were already open are dropped from the returned
// slice so the autoclean branch only acts on fresh ones.
func (d *Daemon) persistNew(in []heuristics.Suggestion) []heuristics.Suggestion {
	out := in[:0]
	for _, s := range in {
		open, err := d.store.HasOpenSuggestion(s.Module, s.Target)
		if err != nil {
			log.Printf("tick: dedupe check: %v", err)
			continue
		}
		if open {
			continue
		}
		stored := toStored(s)
		id, err := d.store.InsertSuggestion(stored)
		if err != nil {
			log.Printf("tick: insert suggestion: %v", err)
			continue
		}
		s.ID = id
		out = append(out, s)
	}
	log.Printf("tick: %d new suggestion(s) (%d candidate)", len(out), len(in))
	return out
}

// openLeakSuggestions loads still-open self-proving rows (leaks, worktrees)
// from the store, minus any already present in fresh (persistNew just
// inserted those), so the auto-clean branch can retry proven-dead targets
// on every tick instead of exactly once. Only the fields the gate cascade
// and deleter consume are projected; a store failure yields nothing
// (fail-quiet — next tick retries).
func (d *Daemon) openLeakSuggestions(fresh []heuristics.Suggestion) []heuristics.Suggestion {
	rows, err := d.store.ListOpenSuggestions()
	if err != nil {
		log.Printf("autoclean: list open leak suggestions: %v", err)
		return nil
	}
	seen := make(map[int64]bool, len(fresh))
	for _, s := range fresh {
		seen[s.ID] = true
	}
	var out []heuristics.Suggestion
	for _, r := range rows {
		if (r.Module != "leaks" && r.Module != "worktrees") || seen[r.ID] {
			continue
		}
		// A target that vanished (manual clean, reboot purge) can never be
		// applied again — retrying would error every tick forever. Dismiss
		// it here: the row's purpose (get the bytes back) is fulfilled.
		if _, err := os.Lstat(r.Target); os.IsNotExist(err) {
			if err := d.store.DismissSuggestion(r.ID, d.now()); err != nil {
				log.Printf("autoclean: dismiss gone target id=%d: %v", r.ID, err)
			}
			continue
		}
		out = append(out, heuristics.Suggestion{
			ID:     r.ID,
			Module: r.Module,
			Target: r.Target,
			Reason: r.Reason,
		})
	}
	return out
}

// runAutoClean evaluates each suggestion against the gates, applies the
// ones that pass, and dismisses any suggestion whose target was deleted.
// trigger is the tick's trigger name ("daily" or "manual" — the engine
// refuses anything else). Returns (freed bytes, count of successful deletes).
func (d *Daemon) runAutoClean(ctx context.Context, cfg autoclean.Config, suggestions []heuristics.Suggestion, trigger string) (int64, int) {
	safety := core.NewSafety(d.cfg.Scan.Roots, []string{".git"})
	// The leaks deleter is the leaks module itself: signature-scoped path
	// predicate plus an apply-time staleness re-proof (TOCTOU guard) — a
	// STRICTER wall than the root allowlist the dev deleter lives behind.
	// Same no-roots Safety as the CLI's newLeaksModule (charter D4).
	leaksMod := leaks.New(leaks.DefaultSignatures(), core.NewSafety(nil, nil))
	wtMod := worktrees.New(worktreeConfig(d))
	eng := autoclean.New(d.store, cfg, d.cfg.Scan.Roots, safety, map[string]autoclean.Deleter{
		"dev": autoclean.DefaultDeleter,
		"leaks": func(ctx context.Context, path string) (int64, error) {
			res, err := leaksMod.Apply(ctx, modules.Action{Module: "leaks", Op: "delete", Target: path})
			return int64(res.BytesFreed), err
		},
		// Same contract as leaks: the module's own Apply re-proves the
		// obvious tier at delete time and refuses anything else, so a
		// judgment-tier row can never slip through this deleter.
		"worktrees": func(ctx context.Context, path string) (int64, error) {
			res, err := wtMod.Apply(ctx, modules.Action{Module: "worktrees", Op: "delete", Target: path})
			return int64(res.BytesFreed), err
		},
	})
	budget := autoclean.NewBudget(cfg.SizeCapPerTickGB)

	var freed int64
	var deleted int
	for _, s := range suggestions {
		action, ok := autoclean.EvaluateSuggestion(s, cfg)
		if !ok {
			log.Printf("autoclean: skip id=%d reason=%s", s.ID, action.SkipReason)
			continue
		}
		if !budget.Take(action.SizeBytes) {
			log.Printf("autoclean: budget exhausted (used=%d cap=%d); halting tick",
				budget.Used(), budget.Cap())
			break
		}
		res, err := eng.Apply(ctx, action, trigger)
		if err != nil {
			log.Printf("autoclean: apply id=%d: %v", s.ID, err)
			continue
		}
		freed += res.FreedBytes
		deleted++
		// Dismiss the suggestion so it doesn't reappear in the user's
		// list. RecordAction would also be reasonable, but Dismiss is
		// the right end-user signal: "we already handled it".
		if err := d.store.DismissSuggestion(s.ID, d.now()); err != nil {
			log.Printf("autoclean: dismiss id=%d: %v", s.ID, err)
		}
	}
	log.Printf("autoclean: deleted=%d freed=%s budget_remaining=%d",
		deleted, humanBytes(freed), budget.Remaining())
	return freed, deleted
}

// tickNotify sends one user-facing notification. Different message for
// "we deleted things" vs. "we found things you should look at".
func (d *Daemon) tickNotify(deleted int, freed int64, suggestionsLeft int) {
	if !d.cfg.Notify.Enabled {
		return
	}
	switch {
	case deleted > 0:
		body := fmt.Sprintf("freed %s; %d suggestion(s) remain", humanBytes(freed), suggestionsLeft)
		_ = notifySendFn("noo-noo", body, "")
	case suggestionsLeft > 0:
		body := fmt.Sprintf("%d new suggestion(s). Run noo-noo suggestions list.", suggestionsLeft)
		_ = notifySendFn("noo-noo", body, "")
	}
}

// leakAlert is one leak-named notification: signature, real reclaimable
// bytes, and the fixing command, aggregated per signature class.
type leakAlert struct {
	Title string
	Body  string
}

// leakAlerts builds the leak-named notification copy from NEW leak
// suggestions. One alert per signature class (a tick that finds 3 Chrome
// clones sends one Chrome alert, not three): the body names the signature,
// the summed REAL reclaimable bytes (sizer-backed SizeBytes), the hit count,
// and — verbatim — the workaround that stops the leak class at its source.
// Pure function so tests assert the exact copy.
func leakAlerts(sugs []heuristics.Suggestion) []leakAlert {
	type agg struct {
		bytes      int64
		count      int
		workaround string
	}
	var order []string
	bySig := map[string]*agg{}
	for _, s := range sugs {
		sig, _ := s.Evidence["signature"].(string)
		if sig == "" {
			sig = "unknown-leak"
		}
		a, ok := bySig[sig]
		if !ok {
			a = &agg{}
			bySig[sig] = a
			order = append(order, sig)
		}
		a.bytes += s.SizeBytes
		a.count++
		if w, _ := s.Evidence["workaround"].(string); w != "" {
			a.workaround = w
		}
	}
	out := make([]leakAlert, 0, len(order))
	for _, sig := range order {
		a := bySig[sig]
		body := fmt.Sprintf("%s: %s really reclaimable (%d leak(s)). Run noo-noo leaks clean.",
			sig, humanBytes(a.bytes), a.count)
		if a.workaround != "" {
			body += " Fix: " + a.workaround
		}
		out = append(out, leakAlert{Title: "noo-noo — disk leak", Body: body})
	}
	return out
}

// notifyLeaks posts one leak-named notification per signature class found
// this tick. Dedup against re-alerting rides persistNew's HasOpenSuggestion
// filter: callers pass only NEWLY-persisted suggestions.
func (d *Daemon) notifyLeaks(sugs []heuristics.Suggestion) {
	if !d.cfg.Notify.Enabled || len(sugs) == 0 {
		return
	}
	for _, al := range leakAlerts(sugs) {
		if err := notifySendFn(al.Title, al.Body, ""); err != nil {
			log.Printf("notify leak: %v", err)
		}
	}
}

// humanBytes formats bytes as a 1-decimal value with a unit suffix.
// Inlined here so tick.go does not pull in a third-party humanize dep.
func humanBytes(n int64) string {
	const k = 1024
	if n < k {
		return fmt.Sprintf("%d B", n)
	}
	units := []string{"KB", "MB", "GB", "TB"}
	f := float64(n) / k
	u := 0
	for f >= k && u < len(units)-1 {
		f /= k
		u++
	}
	return fmt.Sprintf("%.1f %s", f, units[u])
}

// The auto_clean_events audit writes now go straight through *store.Store
// (see internal/store/autoclean_events.go), which satisfies
// autoclean.EventStore directly. The former tick.go sqlEventStore adapter —
// and its inline INSERT/UPDATE SQL — is gone: the DDL and the queries live in
// exactly one place. The compile-time assertion that *store.Store implements
// EventStore lives in main.go, next to the wiring that passes it in.
