package ipc

import (
	"fmt"
	"time"

	"github.com/FRIKKern/noo-noo/internal/heuristics"
	"github.com/FRIKKern/noo-noo/internal/store"
)

// recentActionsLimit caps the number of audit-log rows returned by Report.Full.
// Fifty matches the CLI render budget (one screenful) and keeps the RPC
// response cheap to encode.
const recentActionsLimit = 50

// recentActionsWindow bounds how far back ListActions looks. The CLI only
// surfaces "what the daemon did lately"; older rows stay in the DB for
// audit but are not shipped over the wire.
const recentActionsWindow = 30 * 24 * time.Hour

// idleRepoMinDays / idleRepoMinBytes match the defaults used by the
// idle_repos heuristic (see internal/heuristics). Report.Full surfaces the
// same set the suggestions pipeline would consider, so the CLI can show "all
// candidates" alongside the actually-emitted suggestions.
const (
	idleRepoMinDays  = 60
	idleRepoMinBytes = 100 << 20 // 100 MiB
)

// Full assembles a one-shot snapshot for `noo-noo report`. It is read-only:
// no rows are inserted or mutated. Errors from any underlying query abort
// the call (no partial snapshots).
func (r *ReportService) Full(_ ReportRequest, resp *Report) error {
	if r.Store == nil {
		return fmt.Errorf("report: store not configured")
	}
	now := time.Now()

	actions, err := r.Store.ListActions(now.Add(-recentActionsWindow), recentActionsLimit)
	if err != nil {
		return fmt.Errorf("recent actions: %w", err)
	}

	stored, err := r.Store.ListOpenSuggestions()
	if err != nil {
		return fmt.Errorf("open suggestions: %w", err)
	}
	sugs := make([]SuggestionAlias, 0, len(stored))
	for _, s := range stored {
		sugs = append(sugs, suggestionFromStored(s))
	}

	idle, err := r.Store.ListIdleRepos(idleRepoMinDays, idleRepoMinBytes, now)
	if err != nil {
		return fmt.Errorf("idleness: %w", err)
	}

	*resp = Report{
		GeneratedAt:     now,
		RecentActions:   actions,
		OpenSuggestions: sugs,
		RepoIdleness:    idle,
	}
	return nil
}

// suggestionFromStored projects a store.StoredSuggestion row onto the wire
// type (heuristics.Suggestion). Severity strings map 1:1 to RiskLevel
// constants; unknown values pass through as RiskLevel(severity) so the CLI
// can still render them.
func suggestionFromStored(s store.StoredSuggestion) heuristics.Suggestion {
	ev := make(map[string]any, len(s.Evidence))
	for k, v := range s.Evidence {
		ev[k] = v
	}
	return heuristics.Suggestion{
		ID:        s.ID,
		Module:    s.Module,
		Target:    s.Target,
		Reason:    s.Reason,
		Evidence:  ev,
		RiskLevel: heuristics.RiskLevel(s.Severity),
		CreatedAt: s.Ts,
	}
}
