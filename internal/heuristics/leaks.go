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
// provably-stale, deletable subset. Apply is deliberately absent — the daemon
// SURFACES leaks; it never deletes them (leaks stays outside autoclean's
// ModulesAllowed, and this interface cannot express a delete).
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
	if !cfg.Heuristics.Leaks.Enabled || src == nil {
		return nil
	}
	rep, err := src.Scan(ctx)
	if err != nil {
		// Fail-safe: an aborted scan yields no suggestions rather than a
		// partial picture presented as complete.
		return nil
	}
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
