package heuristics

import (
	"context"
	"errors"
	"strings"
	"testing"

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

func TestLeaksNilSourceEmitsNothing(t *testing.T) {
	if got := Leaks(context.Background(), nil, config.Defaults()); got != nil {
		t.Fatalf("nil source emitted %d suggestions, want none", len(got))
	}
}
