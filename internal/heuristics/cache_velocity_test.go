package heuristics

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/FRIKKern/noo-noo/internal/config"
	"github.com/FRIKKern/noo-noo/internal/store"
)

func TestCacheVelocityFlagsRunaway(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = st.Close() }()

	now := time.Now()
	target := "/Users/me/Library/Caches/yarn"
	// Seed 6-day-old sample at 1 GB and current at 3 GB => 3.0x growth
	// within a 7-day window.
	if err := st.RecordCacheSize(target, 1_000_000_000, now.Add(-6*24*time.Hour)); err != nil {
		t.Fatalf("seed old: %v", err)
	}
	if err := st.RecordCacheSize(target, 3_000_000_000, now); err != nil {
		t.Fatalf("seed now: %v", err)
	}

	cfg := config.Defaults()
	cfg.Heuristics.CacheVelocity.GrowthMultiplier = 2.0
	cfg.Heuristics.CacheVelocity.WindowDays = 7

	got := CacheVelocity(context.Background(), st, cfg)
	if len(got) != 1 {
		t.Fatalf("got %d suggestions, want 1: %+v", len(got), got)
	}
	s := got[0]
	if s.Module != "cache_velocity" {
		t.Errorf("Module = %q, want cache_velocity", s.Module)
	}
	if s.RiskLevel != RiskMedium {
		t.Errorf("RiskLevel = %v, want RiskMedium", s.RiskLevel)
	}
	if s.Target != target {
		t.Errorf("Target = %q", s.Target)
	}
}

func TestCacheVelocityIgnoresStableCache(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "x.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = st.Close() }()

	now := time.Now()
	target := "/Users/me/Library/Caches/stable"
	if err := st.RecordCacheSize(target, 1_000_000_000, now.Add(-6*24*time.Hour)); err != nil {
		t.Fatalf("seed old: %v", err)
	}
	if err := st.RecordCacheSize(target, 1_100_000_000, now); err != nil {
		t.Fatalf("seed now: %v", err)
	}

	cfg := config.Defaults()
	got := CacheVelocity(context.Background(), st, cfg)
	if len(got) != 0 {
		t.Errorf("expected no suggestions, got %+v", got)
	}
}

func TestCacheVelocityDisabled(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "x.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = st.Close() }()

	cfg := config.Defaults()
	cfg.Heuristics.CacheVelocity.Enabled = false
	if got := CacheVelocity(context.Background(), st, cfg); got != nil {
		t.Errorf("disabled: expected nil, got %+v", got)
	}
}
