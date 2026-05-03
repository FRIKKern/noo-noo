package cli

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/FRIKKern/noo-noo/internal/heuristics"
	"github.com/FRIKKern/noo-noo/internal/ipc"
)

type fakeSuggClient struct {
	listed    []heuristics.Suggestion
	dismissed []int64
	listErr   error
}

func (f *fakeSuggClient) SuggestionsList() ([]heuristics.Suggestion, error) {
	return f.listed, f.listErr
}
func (f *fakeSuggClient) SuggestionsDismiss(id int64) error {
	f.dismissed = append(f.dismissed, id)
	return nil
}
func (f *fakeSuggClient) Close() error { return nil }

func TestSuggestionsCmdList(t *testing.T) {
	fc := &fakeSuggClient{listed: []heuristics.Suggestion{
		{ID: 17, Module: "idle_repos", Target: "/Users/me/old", Reason: "no commits in 45d", RiskLevel: heuristics.RiskLow, CreatedAt: time.Now()},
		{ID: 18, Module: "cache_velocity", Target: "~/Library/Caches/yarn", Reason: "3.2x growth in 7d", RiskLevel: heuristics.RiskMedium, CreatedAt: time.Now()},
	}}
	out := &bytes.Buffer{}
	cmd := newSuggestionsCmd(suggOpts{Dial: func() (suggClient, error) { return fc, nil }, Out: out})
	if err := cmd.Run([]string{"list"}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	got := out.String()
	if !strings.Contains(got, "17") || !strings.Contains(got, "/Users/me/old") {
		t.Errorf("missing row 17 in output: %s", got)
	}
	if !strings.Contains(got, "18") || !strings.Contains(got, "yarn") {
		t.Errorf("missing row 18 in output: %s", got)
	}
}

func TestSuggestionsCmdDismiss(t *testing.T) {
	fc := &fakeSuggClient{}
	cmd := newSuggestionsCmd(suggOpts{Dial: func() (suggClient, error) { return fc, nil }, Out: &bytes.Buffer{}})
	if err := cmd.Run([]string{"dismiss", "17"}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(fc.dismissed) != 1 || fc.dismissed[0] != 17 {
		t.Errorf("dismissed = %v, want [17]", fc.dismissed)
	}
}

func TestSuggestionsCmdDismissBadID(t *testing.T) {
	cmd := newSuggestionsCmd(suggOpts{Dial: func() (suggClient, error) { return &fakeSuggClient{}, nil }, Out: &bytes.Buffer{}})
	err := cmd.Run([]string{"dismiss", "notanint"})
	if err == nil {
		t.Error("expected error for non-numeric id")
	}
}

var _ = ipc.SocketEnv // ensure import used
