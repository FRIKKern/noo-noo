//go:build e2e

package e2e

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/FRIKKern/noo-noo/internal/autoclean"
	"github.com/FRIKKern/noo-noo/internal/core"
	"github.com/FRIKKern/noo-noo/internal/heuristics"
)

// memEvents is a tiny in-memory autoclean.EventStore used purely so we can
// drive Engine.Apply in this e2e test without dragging the real SQLite
// machinery in.
type memEvents struct {
	rows []autoclean.AutoCleanEvent
}

func (m *memEvents) RecordAutoCleanEvent(e autoclean.AutoCleanEvent) (int64, error) {
	m.rows = append(m.rows, e)
	return int64(len(m.rows)), nil
}

func (m *memEvents) UpdateAutoCleanEvent(id int64, u autoclean.AutoCleanEventUpdate) error {
	idx := int(id - 1)
	if idx < 0 || idx >= len(m.rows) {
		return nil
	}
	m.rows[idx].EndedAtUnix = u.EndedAtUnix
	m.rows[idx].Outcome = u.Outcome
	m.rows[idx].FreedBytes = u.FreedBytes
	m.rows[idx].ErrorMsg = u.ErrorMsg
	return nil
}

type fakeRepo struct {
	name     string
	sizeMB   int
	idleDays int
	module   string
}

// makeSuggestion builds a heuristics.Suggestion that EvaluateSuggestion can
// score: idle_days and size_bytes both come from Evidence.
func makeSuggestion(id int64, target, module string, sizeMB, idleDays int) heuristics.Suggestion {
	return heuristics.Suggestion{
		ID:     id,
		Module: module,
		Target: target,
		Reason: "test",
		Evidence: map[string]any{
			"size_bytes": int64(sizeMB) * 1024 * 1024,
			"idle_days":  idleDays,
		},
		RiskLevel: heuristics.RiskMedium,
		CreatedAt: time.Now(),
	}
}

// materializeNodeModules creates <root>/<name>/node_modules/ with a file of
// roughly sizeMB bytes (kept tiny — the Apply path computes size before
// delete so a sparse file is fine for the gate test).
func materializeNodeModules(t *testing.T, root string, r fakeRepo) string {
	t.Helper()
	nm := filepath.Join(root, r.name, "node_modules")
	if err := os.MkdirAll(nm, 0o755); err != nil {
		t.Fatal(err)
	}
	// Write an empty file rather than allocating sizeMB bytes — gate
	// rejection is based on Suggestion.Evidence["size_bytes"], not the
	// on-disk size, and writing a multi-GB blob makes the test slow.
	if err := os.WriteFile(filepath.Join(nm, "marker"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	return nm
}

// runEngineOverSuggestions is the test harness's RunAll: it iterates the
// suggestions, calls EvaluateSuggestion, and on a pass, calls Engine.Apply.
// Returns parallel slices of (deleted, skipReasonsBySuggestionID).
func runEngineOverSuggestions(
	ctx context.Context,
	eng *autoclean.Engine,
	cfg autoclean.Config,
	sugs []heuristics.Suggestion,
) (deleted []autoclean.Result, skipped map[int64]string) {
	skipped = map[int64]string{}
	for _, s := range sugs {
		action, ok := autoclean.EvaluateSuggestion(s, cfg)
		if !ok {
			skipped[s.ID] = action.SkipReason
			continue
		}
		res, err := eng.Apply(ctx, action, "daily")
		if err != nil {
			skipped[s.ID] = "apply_error:" + err.Error()
			continue
		}
		deleted = append(deleted, res)
	}
	return deleted, skipped
}

// TestAutoCleanGates exercises the full gate cascade. We construct four
// fake repos with one that satisfies every gate and three that each fail a
// different gate (module / idle / size), plus two follow-up engines that
// flip the master switch and the risk-ack respectively.
func TestAutoCleanGates(t *testing.T) {
	tmp := t.TempDir()

	repos := []fakeRepo{
		{"all-pass", 1400, 184, "dev"},        // expect delete
		{"wrong-module", 1400, 184, "caches"}, // expect skip (module)
		{"not-idle", 1400, 30, "dev"},         // expect skip (idle)
		{"too-small", 500, 184, "dev"},        // expect skip (size)
	}
	suggestions := make([]heuristics.Suggestion, len(repos))
	for i, r := range repos {
		path := materializeNodeModules(t, tmp, r)
		suggestions[i] = makeSuggestion(int64(i+1), path, r.module, r.sizeMB, r.idleDays)
	}

	cfg := autoclean.Config{
		Enabled:            true,
		ModulesAllowed:     []string{"dev"},
		MinIdleDays:        90,
		MinSizeMB:          1024,
		SizeCapPerTickGB:   10,
		RiskAcknowledgedAt: time.Now().Format(time.RFC3339),
	}

	store := &memEvents{}
	safety := core.NewSafety([]string{tmp}, nil)
	deleters := map[string]autoclean.Deleter{
		"dev": autoclean.DefaultDeleter,
	}
	eng := autoclean.New(store, cfg, []string{tmp}, safety, deleters)

	deleted, skipped := runEngineOverSuggestions(context.Background(), eng, cfg, suggestions)

	if len(deleted) != 1 {
		t.Fatalf("want exactly 1 delete (all-pass); got %d %#v (skipped=%v)",
			len(deleted), deleted, skipped)
	}
	if filepath.Base(filepath.Dir(deleted[0].TargetPath)) != "all-pass" {
		t.Fatalf("delete should be all-pass, got %s", deleted[0].TargetPath)
	}
	if len(skipped) != 3 {
		t.Fatalf("want 3 skips; got %d (%v)", len(skipped), skipped)
	}
	wantReasons := map[string]bool{
		"module_not_allowed": false,
		"idle_too_short":     false,
		"size_too_small":     false,
	}
	for _, reason := range skipped {
		if _, ok := wantReasons[reason]; ok {
			wantReasons[reason] = true
		}
	}
	for r, seen := range wantReasons {
		if !seen {
			t.Errorf("missing skip reason: %s (got skipped=%v)", r, skipped)
		}
	}

	// case 5: enabled=false => skip ALL.
	cfg2 := cfg
	cfg2.Enabled = false
	eng2 := autoclean.New(store, cfg2, []string{tmp}, safety, deleters)
	d2, _ := runEngineOverSuggestions(context.Background(), eng2, cfg2, suggestions)
	if len(d2) != 0 {
		t.Fatalf("disabled engine should never delete; got %d", len(d2))
	}

	// case 6: enabled=true but risk_ack empty => skip ALL.
	cfg3 := cfg
	cfg3.RiskAcknowledgedAt = ""
	eng3 := autoclean.New(store, cfg3, []string{tmp}, safety, deleters)
	d3, sk3 := runEngineOverSuggestions(context.Background(), eng3, cfg3, suggestions)
	if len(d3) != 0 {
		t.Fatalf("unacknowledged engine should never delete; got %d", len(d3))
	}
	for _, reason := range sk3 {
		if reason != "risk_not_acknowledged" {
			t.Errorf("with empty ack, every skip should be risk_not_acknowledged; got %q", reason)
		}
	}
}
