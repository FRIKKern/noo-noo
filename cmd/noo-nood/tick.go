package main

import (
	"context"
	"database/sql"
	"fmt"
	"log"

	"github.com/FRIKKern/noo-noo/internal/autoclean"
	"github.com/FRIKKern/noo-noo/internal/config"
	"github.com/FRIKKern/noo-noo/internal/core"
	"github.com/FRIKKern/noo-noo/internal/heuristics"
	"github.com/FRIKKern/noo-noo/internal/notify"
	"github.com/FRIKKern/noo-noo/internal/scan"
	"github.com/FRIKKern/noo-noo/internal/store"
)

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

// RunTick is the per-trigger entrypoint registered by init() above. Steps:
//
//  1. scan.ScanRoots — populate fresh data the heuristics will score.
//  2. Run enabled heuristics; persist new (deduped) suggestions.
//  3. If trigger == daily AND autoclean is enabled in config:
//     evaluate every fresh suggestion against the gates, run Apply for
//     each pass, and dismiss any suggestion whose target was actually
//     deleted so the user doesn't see stale entries.
//  4. Notify, with different copy for "freed N bytes" vs.
//     "M suggestions waiting".
//
// Pressure-triggered ticks NEVER reach the autoclean branch (correlates
// with active dev work; safety design). The autoclean engine itself
// also refuses any trigger != "daily", so this is defense in depth.
func (d *Daemon) RunTick(ctx context.Context, trigger TickTrigger) error {
	log.Printf("tick start: trigger=%s", trigger)

	// Step 1: walk the filesystem -> populate fresh data.
	if err := scan.ScanRoots(ctx, scan.Roots{Repos: d.cfg.Scan.Roots}, d.store); err != nil {
		log.Printf("tick: scan: %v", err)
	}

	// Step 2: run heuristics over the fresh data.
	suggestions := d.collectSuggestions(ctx)

	// Persist (deduped) before deciding what to auto-clean — autoclean
	// dismisses by id, so we need real ids on the in-memory rows. The
	// updated slice has ID populated for every newly-inserted row.
	suggestions = d.persistNew(suggestions)

	// Step 3: maybe auto-clean. Two preconditions:
	//   - daily trigger only (defense in depth — engine also enforces).
	//   - autoCleanCfgFn returns a config with Enabled=true.
	var freed int64
	var deleted int
	autoCfg := autoCleanCfgFn(d.cfg)
	if trigger == TriggerDaily && autoCfg.Enabled {
		freed, deleted = d.runAutoClean(ctx, autoCfg, suggestions)
	}

	// Step 4: notify. Different copy for cleaned vs. suggestions-only.
	d.tickNotify(deleted, freed, len(suggestions))
	return nil
}

// collectSuggestions runs every enabled heuristic and returns the union.
// Mirrors what main.go's runScan used to do; pulled into tick.go so the
// autoclean branch can see the same in-memory list (ids populated by
// persistNew).
func (d *Daemon) collectSuggestions(ctx context.Context) []heuristics.Suggestion {
	var all []heuristics.Suggestion
	if d.cfg.Heuristics.IdleRepos.Enabled {
		all = append(all, heuristics.IdleRepos(ctx, d.store, d.cfg)...)
	}
	if d.cfg.Heuristics.CacheVelocity.Enabled {
		all = append(all, heuristics.CacheVelocity(ctx, d.store, d.cfg)...)
	}
	return all
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

// runAutoClean evaluates each suggestion against the gates, applies the
// ones that pass, and dismisses any suggestion whose target was deleted.
// Returns (freed bytes, count of successful deletes).
func (d *Daemon) runAutoClean(ctx context.Context, cfg autoclean.Config, suggestions []heuristics.Suggestion) (int64, int) {
	safety := core.NewSafety(d.cfg.Scan.Roots, []string{".git"})
	eng := autoclean.New(newSQLEventStore(d.store), cfg, d.cfg.Scan.Roots, safety, map[string]autoclean.Deleter{
		"dev": autoclean.DefaultDeleter,
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
		res, err := eng.Apply(ctx, action, "daily")
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
		_ = notify.Send("noo-noo", body, "")
	case suggestionsLeft > 0:
		body := fmt.Sprintf("%d new suggestion(s). Run noo-noo suggestions list.", suggestionsLeft)
		_ = notify.Send("noo-noo", body, "")
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

// sqlEventStore adapts *sql.DB to autoclean.EventStore. Used so the
// daemon can hand a real store to autoclean.New without widening the
// internal/store public surface for what's really an autoclean-private
// audit table. T97 (or a follow-up) may move this onto *store.Store
// directly; for now keeping it in tick.go avoids a cross-package change
// outside this task's verifier arm.
//
// Currently unused at runtime (autoCleanCfgFn returns Enabled=false, so
// autoclean.New is never called via this adapter); kept here so T97 can
// flip the switch without re-introducing the type.
type sqlEventStore struct{ db *sql.DB }

func newSQLEventStore(st *store.Store) sqlEventStore { return sqlEventStore{db: st.DB()} }

func (s sqlEventStore) RecordAutoCleanEvent(e autoclean.AutoCleanEvent) (int64, error) {
	res, err := s.db.Exec(`INSERT INTO auto_clean_events
		(started_at_unix, ended_at_unix, trigger, outcome, skip_reason, target_path, module,
		 target_size_bytes, freed_bytes, idle_days_at_decision, suggestion_id, error_msg)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		e.StartedAtUnix, nullableEnded(e.EndedAtUnix), e.Trigger, e.Outcome, e.SkipReason,
		e.TargetPath, e.Module, e.TargetSizeBytes, e.FreedBytes, e.IdleDaysAtDecision,
		e.SuggestionID, e.ErrorMsg)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (s sqlEventStore) UpdateAutoCleanEvent(id int64, u autoclean.AutoCleanEventUpdate) error {
	_, err := s.db.Exec(`UPDATE auto_clean_events
		SET ended_at_unix = ?, outcome = ?, freed_bytes = ?, error_msg = ?
		WHERE id = ?`,
		u.EndedAtUnix, u.Outcome, u.FreedBytes, u.ErrorMsg, id)
	return err
}

func nullableEnded(v int64) any {
	if v == 0 {
		return nil
	}
	return v
}

// Compile-time assertion: sqlEventStore satisfies autoclean.EventStore.
// Caught here rather than at the eng := autoclean.New(...) site so a
// signature drift surfaces in `go vet`, not at runtime. The blank-name
// assignment is a standard Go idiom for "use this var only for its
// type-check side effect."
var _ autoclean.EventStore = sqlEventStore{}
