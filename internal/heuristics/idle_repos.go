package heuristics

import (
	"context"
	"fmt"
	"time"

	"github.com/FRIKKern/noo-noo/internal/config"
	"github.com/FRIKKern/noo-noo/internal/store"
)

// IdleRepos flags repositories with (a) no commit in the last MinIdleDays AND
// (b) a node_modules directory larger than MinNodeModulesBytes. Risk: low.
func IdleRepos(ctx context.Context, st *store.Store, cfg config.Config) []Suggestion {
	if !cfg.Heuristics.IdleRepos.Enabled {
		return nil
	}
	now := time.Now()
	rows, err := st.ListIdleRepos(
		cfg.Heuristics.IdleRepos.MinIdleDays,
		cfg.Heuristics.IdleRepos.MinNodeModulesBytes,
		now,
	)
	if err != nil {
		return nil
	}
	out := make([]Suggestion, 0, len(rows))
	for _, r := range rows {
		select {
		case <-ctx.Done():
			return out
		default:
		}
		idleDays := int(now.Sub(r.LastCommitAt).Hours() / 24)
		out = append(out, Suggestion{
			Module: "idle_repos",
			Target: r.Path,
			Reason: fmt.Sprintf("no commits in %dd; node_modules=%d bytes", idleDays, r.NodeModulesBytes),
			Evidence: map[string]any{
				"idle_days":          idleDays,
				"node_modules_bytes": r.NodeModulesBytes,
				"last_commit_at":     r.LastCommitAt,
			},
			RiskLevel: RiskLow,
			CreatedAt: now,
		})
	}
	return out
}
