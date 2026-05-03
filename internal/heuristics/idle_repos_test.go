package heuristics

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/FRIKKern/noo-noo/internal/config"
	"github.com/FRIKKern/noo-noo/internal/store"
)

func TestIdleReposFlagsOldFatRepo(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = st.Close() }()

	now := time.Now()
	if err := st.UpsertRepoIdleness(store.RepoIdleness{
		Path:             "/Users/me/old-thing",
		LastCommitAt:     now.Add(-45 * 24 * time.Hour),
		NodeModulesBytes: 800 * 1024 * 1024, // 800 MB
		LastScanAt:       now,
	}); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	cfg := config.Defaults()
	cfg.Heuristics.IdleRepos.MinIdleDays = 30
	cfg.Heuristics.IdleRepos.MinNodeModulesBytes = 500 * 1024 * 1024

	got := IdleRepos(context.Background(), st, cfg)
	if len(got) != 1 {
		t.Fatalf("got %d, want 1: %+v", len(got), got)
	}
	if got[0].Module != "idle_repos" {
		t.Errorf("Module = %q", got[0].Module)
	}
	if got[0].RiskLevel != RiskLow {
		t.Errorf("RiskLevel = %v, want RiskLow", got[0].RiskLevel)
	}
	if got[0].Target != "/Users/me/old-thing" {
		t.Errorf("Target = %q", got[0].Target)
	}
}

func TestIdleReposIgnoresRecentlyTouched(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "x.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = st.Close() }()

	now := time.Now()
	if err := st.UpsertRepoIdleness(store.RepoIdleness{
		Path:             "/active",
		LastCommitAt:     now.Add(-5 * 24 * time.Hour),
		NodeModulesBytes: 800 * 1024 * 1024,
		LastScanAt:       now,
	}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	cfg := config.Defaults()
	got := IdleRepos(context.Background(), st, cfg)
	if len(got) != 0 {
		t.Errorf("expected 0, got %+v", got)
	}
}

func TestIdleReposIgnoresThinRepos(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "x.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = st.Close() }()

	now := time.Now()
	if err := st.UpsertRepoIdleness(store.RepoIdleness{
		Path:             "/old-but-tiny",
		LastCommitAt:     now.Add(-90 * 24 * time.Hour),
		NodeModulesBytes: 1024 * 1024, // 1 MB
		LastScanAt:       now,
	}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	cfg := config.Defaults()
	got := IdleRepos(context.Background(), st, cfg)
	if len(got) != 0 {
		t.Errorf("expected 0 (too small), got %+v", got)
	}
}

func TestIdleReposDisabled(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "x.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = st.Close() }()

	cfg := config.Defaults()
	cfg.Heuristics.IdleRepos.Enabled = false
	if got := IdleRepos(context.Background(), st, cfg); got != nil {
		t.Errorf("disabled: expected nil, got %+v", got)
	}
}
