package heuristics

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/FRIKKern/noo-noo/internal/modules"
)

// WorktreeSource is the diagnose-only subset of the worktrees module the
// daemon drives: Scan classifies, Plan filters to the provably-dead obvious
// tier. Deletion happens ONLY through autoclean's worktrees deleter
// (worktrees.Module.Apply — which re-classifies at delete time), and only
// when the operator has opted "worktrees" into auto_clean.modules_allowed.
type WorktreeSource interface {
	Scan(ctx context.Context) (modules.Report, error)
	Plan(r modules.Report) []modules.Action
}

// Worktrees emits one Suggestion per provably-dead (obvious-tier) worktree.
// Judgment-tier rows are deliberately NOT suggestions: they need `noo-noo
// worktrees judge` (or an AI session reading `worktrees list --json`), and a
// daemon row it can never act on would sit open forever as noise.
func Worktrees(ctx context.Context, src WorktreeSource, enabled bool) []Suggestion {
	if !enabled || src == nil {
		return nil
	}
	rep, err := src.Scan(ctx)
	if err != nil {
		return nil // fail-safe: no partial picture presented as complete
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
			continue
		}
		size := int64(a.Size)
		ev := make(map[string]any, len(it.Evidence)+1)
		for k, v := range it.Evidence {
			ev[k] = v
		}
		ev["size_bytes"] = strconv.FormatInt(size, 10)
		out = append(out, Suggestion{
			Module:    "worktrees",
			Target:    a.Target,
			Reason:    fmt.Sprintf("dead worktree (%s, idle %s): merged to remote, clean, nothing open", it.Evidence["branch"], it.Evidence["idle"]),
			Evidence:  ev,
			RiskLevel: RiskLevel(a.Risk.String()),
			SizeBytes: size,
			CreatedAt: now,
		})
	}
	return out
}
