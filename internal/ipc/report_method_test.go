package ipc

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/FRIKKern/noo-noo/internal/store"
)

func TestReportFullReturnsSnapshot(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = st.Close() }()

	// Seed one action.
	if _, err := st.RecordAction(store.StoredAction{
		Ts:      time.Now(),
		Module:  "test",
		Op:      "noop",
		Target:  "/x",
		Outcome: "ok",
	}); err != nil {
		t.Fatalf("seed action: %v", err)
	}

	// Seed one open suggestion.
	if _, err := st.InsertSuggestion(store.StoredSuggestion{
		Ts:       time.Now(),
		Module:   "idle_repos",
		Target:   "/repo",
		Reason:   "idle 60d",
		Severity: "low",
	}); err != nil {
		t.Fatalf("seed suggestion: %v", err)
	}

	// Seed one idle repo (commit 90 days ago, 200MB node_modules).
	if err := st.UpsertRepoIdleness(store.RepoIdleness{
		Path:             "/repo",
		LastCommitAt:     time.Now().AddDate(0, 0, -90),
		NodeModulesBytes: 200 << 20,
		LastScanAt:       time.Now(),
	}); err != nil {
		t.Fatalf("seed idleness: %v", err)
	}

	svc := &ReportService{Store: st}
	var resp Report
	if err := svc.Full(ReportRequest{}, &resp); err != nil {
		t.Fatalf("Full: %v", err)
	}
	if resp.GeneratedAt.IsZero() {
		t.Error("GeneratedAt should be set")
	}
	if len(resp.RecentActions) != 1 {
		t.Errorf("RecentActions = %d, want 1", len(resp.RecentActions))
	}
	if len(resp.OpenSuggestions) != 1 {
		t.Errorf("OpenSuggestions = %d, want 1", len(resp.OpenSuggestions))
	}
	if got := resp.OpenSuggestions[0].Target; got != "/repo" {
		t.Errorf("OpenSuggestions[0].Target = %q, want /repo", got)
	}
	if len(resp.RepoIdleness) != 1 {
		t.Errorf("RepoIdleness = %d, want 1", len(resp.RepoIdleness))
	}
}

func TestReportFullEmptyStore(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = st.Close() }()

	svc := &ReportService{Store: st}
	var resp Report
	if err := svc.Full(ReportRequest{}, &resp); err != nil {
		t.Fatalf("Full: %v", err)
	}
	if resp.GeneratedAt.IsZero() {
		t.Error("GeneratedAt should be set even with empty store")
	}
	if len(resp.RecentActions) != 0 {
		t.Errorf("RecentActions = %d, want 0", len(resp.RecentActions))
	}
	if len(resp.OpenSuggestions) != 0 {
		t.Errorf("OpenSuggestions = %d, want 0", len(resp.OpenSuggestions))
	}
	if len(resp.RepoIdleness) != 0 {
		t.Errorf("RepoIdleness = %d, want 0", len(resp.RepoIdleness))
	}
}
