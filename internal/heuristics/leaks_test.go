package heuristics

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/FRIKKern/noo-noo/internal/config"
	"github.com/FRIKKern/noo-noo/internal/core"
	"github.com/FRIKKern/noo-noo/internal/modules"
)

// fakeLeakSource satisfies LeakSource without touching real system paths or
// spawning lsof. Plan mirrors the real leaks module: only items classified
// stale become delete actions.
type fakeLeakSource struct {
	rep     modules.Report
	scanErr error
}

func (f *fakeLeakSource) Scan(context.Context) (modules.Report, error) {
	return f.rep, f.scanErr
}

func (f *fakeLeakSource) Plan(r modules.Report) []modules.Action {
	var out []modules.Action
	for _, it := range r.Items {
		if it.Evidence["staleness"] != "stale" {
			continue
		}
		out = append(out, modules.Action{
			Module: "leaks", Op: "delete", Target: it.Path, Size: it.Size, Risk: modules.RiskLow,
		})
	}
	return out
}

const testWorkaround = "launch with --disable-features=MacAppCodeSignClone to stop the leak class at its source."

func staleCloneItem(path string, size int64) modules.Item {
	return modules.Item{
		Path: path,
		Size: core.Bytes(size),
		Evidence: map[string]string{
			"signature":  "chrome-code-sign-clone",
			"staleness":  "stale",
			"lsof":       "no open files",
			"workaround": testWorkaround,
		},
	}
}

func TestLeaksEmitsSuggestionPerStaleHit(t *testing.T) {
	src := &fakeLeakSource{rep: modules.Report{
		Module: "leaks",
		Items: []modules.Item{
			staleCloneItem("/private/var/folders/xx/T/X/a.code_sign_clone/code_sign_clone.aaaaaa", 2<<30),
			{ // live hit: reported by Scan, filtered out by Plan, never a suggestion
				Path: "/private/var/folders/xx/T/X/b.code_sign_clone/code_sign_clone.bbbbbb",
				Size: core.Bytes(1 << 30),
				Evidence: map[string]string{
					"signature": "chrome-code-sign-clone",
					"staleness": "live",
				},
			},
		},
	}}
	got := Leaks(context.Background(), src, config.Defaults())
	if len(got) != 1 {
		t.Fatalf("Leaks emitted %d suggestions, want 1 (stale only)", len(got))
	}
	s := got[0]
	if s.Module != "leaks" {
		t.Errorf("Module = %q, want leaks", s.Module)
	}
	if want := int64(2 << 30); s.SizeBytes != want {
		t.Errorf("SizeBytes = %d, want %d", s.SizeBytes, want)
	}
	if got := s.Evidence["size_bytes"]; got != "2147483648" {
		t.Errorf("Evidence[size_bytes] = %v, want string \"2147483648\" (store carry)", got)
	}
	if got := s.Evidence["signature"]; got != "chrome-code-sign-clone" {
		t.Errorf("Evidence[signature] = %v", got)
	}
	if got, _ := s.Evidence["workaround"].(string); got != testWorkaround {
		t.Errorf("Evidence[workaround] = %q, want the registry workaround verbatim", got)
	}
	if s.RiskLevel != RiskLow {
		t.Errorf("RiskLevel = %q, want low", s.RiskLevel)
	}
	if !strings.Contains(s.Reason, "chrome-code-sign-clone") || !strings.Contains(s.Reason, "2.0 GB") {
		t.Errorf("Reason = %q, want signature name and real size", s.Reason)
	}
}

func TestLeaksDisabledEmitsNothing(t *testing.T) {
	src := &fakeLeakSource{rep: modules.Report{
		Items: []modules.Item{staleCloneItem("/private/tmp/claude-x", 1<<30)},
	}}
	cfg := config.Defaults()
	cfg.Heuristics.Leaks.Enabled = false
	if got := Leaks(context.Background(), src, cfg); got != nil {
		t.Fatalf("disabled Leaks emitted %d suggestions, want none", len(got))
	}
}

func TestLeaksScanErrorFailsSafe(t *testing.T) {
	src := &fakeLeakSource{scanErr: errors.New("boom")}
	if got := Leaks(context.Background(), src, config.Defaults()); got != nil {
		t.Fatalf("erroring scan emitted %d suggestions, want none", len(got))
	}
}

// TestLeakStormsCountsLiveAndStaleWithinWindow: the storm detector counts
// EVERY instance (live clones included — they are live precisely because
// the relaunch loop is running), only inside the window, per signature,
// and only at or above the threshold.
func TestLeakStormsCountsLiveAndStaleWithinWindow(t *testing.T) {
	now := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	born := map[string]time.Time{}
	var items []modules.Item
	add := func(path, sig, staleness string, age time.Duration) {
		born[path] = now.Add(-age)
		items = append(items, modules.Item{Path: path, Evidence: map[string]string{
			"signature": sig, "staleness": staleness, "workaround": testWorkaround,
		}})
	}
	for i := 0; i < 9; i++ {
		add(fmt.Sprintf("/x/chrome-%d", i), "chrome-code-sign-clone", "live", time.Duration(i)*time.Minute)
	}
	add("/x/chrome-stale", "chrome-code-sign-clone", "stale", 30*time.Minute) // 10th, inside window
	add("/x/chrome-old", "chrome-code-sign-clone", "stale", 3*time.Hour)      // outside window
	for i := 0; i < 5; i++ {
		add(fmt.Sprintf("/x/scratch-%d", i), "private-tmp-agent-scratch", "live", time.Minute)
	}
	appearedAt := func(p string) time.Time { return born[p] }

	storms := LeakStorms(modules.Report{Items: items}, appearedAt, now, time.Hour, 10)
	if len(storms) != 1 {
		t.Fatalf("storms = %+v, want exactly the chrome one", storms)
	}
	s := storms[0]
	if s.Signature != "chrome-code-sign-clone" || s.Count != 10 || s.Workaround != testWorkaround {
		t.Errorf("storm = %+v", s)
	}
	// Threshold is inclusive at 10 and exclusive below it.
	if got := LeakStorms(modules.Report{Items: items}, appearedAt, now, time.Hour, 11); len(got) != 0 {
		t.Errorf("minCount 11 should yield no storm, got %+v", got)
	}
}

func TestLeaksNilSourceEmitsNothing(t *testing.T) {
	if got := Leaks(context.Background(), nil, config.Defaults()); got != nil {
		t.Fatalf("nil source emitted %d suggestions, want none", len(got))
	}
}
