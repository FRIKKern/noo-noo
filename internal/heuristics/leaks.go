package heuristics

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/FRIKKern/noo-noo/internal/config"
	"github.com/FRIKKern/noo-noo/internal/modules"
)

// LeakSource is the diagnose-only subset of the leaks module this heuristic
// drives: Scan finds signature hits (live and stale), Plan filters to the
// provably-stale, deletable subset. Apply is deliberately absent — this
// interface cannot express a delete. Deletion happens ONLY through
// autoclean's leaks deleter (leaks.Module.Apply, which re-proves staleness
// at delete time), and only when the operator has opted "leaks" into
// auto_clean.modules_allowed.
type LeakSource interface {
	Scan(ctx context.Context) (modules.Report, error)
	Plan(r modules.Report) []modules.Action
}

// Leaks runs the leak-signature registry on the daemon tick and emits one
// Suggestion per provably-stale hit. This closes the founding leak loop:
// the signatures that previously only fired on an explicit `noo-noo leaks`
// CLI run now fire on every daily and pressure tick.
//
// Sizes are the sizer-backed REAL reclaimable bytes (Item.Size =
// sizer.UniqueAllocated, clone- and sparse-aware — charter D2), carried both
// first-class (SizeBytes) and as Evidence["size_bytes"] so the value
// survives the store's string-keyed evidence_json round-trip (charter D17).
// Evidence also carries the signature id, staleness proof trail, and the
// workaround command verbatim (injected by the leaks module's inspect).
func Leaks(ctx context.Context, src LeakSource, cfg config.Config) []Suggestion {
	rep, ok := ScanLeaks(ctx, src, cfg)
	if !ok {
		return nil
	}
	return LeaksFromReport(src, rep)
}

// ScanLeaks runs the leak scan once and returns the full report — live hits
// included — so the tick can feed BOTH the suggestion path (stale only) and
// the storm detector (every instance) from one lsof pass. ok=false means
// disabled, no source, or a scan error: fail-safe, an aborted scan yields
// nothing rather than a partial picture presented as complete.
func ScanLeaks(ctx context.Context, src LeakSource, cfg config.Config) (modules.Report, bool) {
	if !cfg.Heuristics.Leaks.Enabled || src == nil {
		return modules.Report{}, false
	}
	rep, err := src.Scan(ctx)
	if err != nil {
		return modules.Report{}, false
	}
	return rep, true
}

// LeakStorm is a burst of NEW instances of one leak signature: Count
// instances whose appearance time falls inside the detection window. A
// burst is alert-worthy on its own, before any instance is stale enough to
// clean — 28 Chrome code-sign clones in 20 minutes is a relaunch loop, and
// the fix is to stop the loop (the signature's Workaround), not to wait for
// lsof to go quiet.
type LeakStorm struct {
	Signature  string
	Count      int
	Workaround string
}

// LeakStorms counts, per signature, the report's instances (live AND stale)
// that appeared at or after now-window, and returns every signature whose
// count reaches minCount. appearedAt supplies each path's appearance time
// (creation time when the filesystem has it, first-sighting otherwise).
// Pure: the tick owns rate-limiting and notification.
func LeakStorms(rep modules.Report, appearedAt func(path string) time.Time, now time.Time, window time.Duration, minCount int) []LeakStorm {
	cutoff := now.Add(-window)
	type agg struct {
		count      int
		workaround string
	}
	var order []string
	bySig := map[string]*agg{}
	for _, it := range rep.Items {
		if appearedAt(it.Path).Before(cutoff) {
			continue
		}
		sig := it.Evidence["signature"]
		if sig == "" {
			sig = "unknown-leak"
		}
		a, ok := bySig[sig]
		if !ok {
			a = &agg{}
			bySig[sig] = a
			order = append(order, sig)
		}
		a.count++
		if w := it.Evidence["workaround"]; w != "" {
			a.workaround = w
		}
	}
	var out []LeakStorm
	for _, sig := range order {
		a := bySig[sig]
		if a.count < minCount {
			continue
		}
		out = append(out, LeakStorm{Signature: sig, Count: a.count, Workaround: a.workaround})
	}
	return out
}

// LeaksFromReport builds the stale-only suggestions from an already-taken
// report (see Leaks for the semantics).
func LeaksFromReport(src LeakSource, rep modules.Report) []Suggestion {
	items := make(map[string]modules.Item, len(rep.Items))
	for _, it := range rep.Items {
		items[it.Path] = it
	}
	now := time.Now()
	actions := src.Plan(rep)
	out := make([]Suggestion, 0, len(actions))
	for _, a := range actions {
		it, ok := items[a.Target]
		if !ok {
			continue // Plan invented a target Scan never reported; skip.
		}
		size := int64(a.Size)
		ev := make(map[string]any, len(it.Evidence)+1)
		for k, v := range it.Evidence {
			ev[k] = v
		}
		// The store carry (D16/D17): evidence values are strings after the
		// store round-trip, so write the canonical string form here and let
		// ipc.suggestionFromStored parse it back into SizeBytes.
		ev["size_bytes"] = strconv.FormatInt(size, 10)
		sig := it.Evidence["signature"]
		if sig == "" {
			sig = "unknown-leak"
		}
		out = append(out, Suggestion{
			Module:    "leaks",
			Target:    a.Target,
			Reason:    fmt.Sprintf("%s: %s really reclaimable (proven stale)", sig, humanSize(size)),
			Evidence:  ev,
			RiskLevel: RiskLevel(a.Risk.String()),
			SizeBytes: size,
			CreatedAt: now,
		})
	}
	return out
}

// humanSize formats a byte count in 1024-based units with one decimal for
// non-byte sizes. Local so the package pulls in no humanize dependency.
func humanSize(n int64) string {
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
