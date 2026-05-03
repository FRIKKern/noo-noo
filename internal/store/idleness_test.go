package store

import (
	"testing"
	"time"
)

func TestIdlenessUpsert(t *testing.T) {
	s := mustStore(t)
	defer func() { _ = s.Close() }()

	now := time.Date(2026, 5, 3, 3, 0, 0, 0, time.UTC)
	r := RepoIdleness{
		Path:             "/Users/frikk/repos/old-thing",
		LastCommitAt:     now.Add(-90 * 24 * time.Hour),
		NodeModulesBytes: 900_000_000,
		LastScanAt:       now,
	}
	if err := s.UpsertRepoIdleness(r); err != nil {
		t.Fatalf("UpsertRepoIdleness: %v", err)
	}
	// Update same row with a newer scan_at.
	r.NodeModulesBytes = 950_000_000
	r.LastScanAt = now.Add(24 * time.Hour)
	if err := s.UpsertRepoIdleness(r); err != nil {
		t.Fatalf("UpsertRepoIdleness (update): %v", err)
	}
	got, err := s.ListIdleRepos(30, 500_000_000, now.Add(48*time.Hour))
	if err != nil {
		t.Fatalf("ListIdleRepos: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 idle repo, got %d", len(got))
	}
	if got[0].NodeModulesBytes != 950_000_000 {
		t.Errorf("upsert did not update bytes: %d", got[0].NodeModulesBytes)
	}
}

func TestIdlenessThresholds(t *testing.T) {
	s := mustStore(t)
	defer func() { _ = s.Close() }()
	now := time.Date(2026, 5, 3, 0, 0, 0, 0, time.UTC)

	// recent commit, small node_modules — should NOT match.
	_ = s.UpsertRepoIdleness(RepoIdleness{
		Path: "/a", LastCommitAt: now.Add(-3 * 24 * time.Hour),
		NodeModulesBytes: 10_000_000, LastScanAt: now,
	})
	// old commit, big node_modules — SHOULD match.
	_ = s.UpsertRepoIdleness(RepoIdleness{
		Path: "/b", LastCommitAt: now.Add(-90 * 24 * time.Hour),
		NodeModulesBytes: 600_000_000, LastScanAt: now,
	})
	// old commit, small node_modules — should NOT match.
	_ = s.UpsertRepoIdleness(RepoIdleness{
		Path: "/c", LastCommitAt: now.Add(-90 * 24 * time.Hour),
		NodeModulesBytes: 10_000_000, LastScanAt: now,
	})

	got, err := s.ListIdleRepos(30, 500_000_000, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Path != "/b" {
		t.Errorf("expected only /b, got %+v", got)
	}
}
