package heuristics

import (
	"context"
	"path/filepath"
	"strings"
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

// TestCacheVelocityRateTruth pins the bytes/day computation against a known
// series: 100 MB four days ago, 500 MB now => (500-100) MB over 4 days =
// 100 MB/day. The rate must land in both the Evidence map and the Reason.
func TestCacheVelocityRateTruth(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "rate.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = st.Close() }()

	now := time.Now()
	target := "/Users/me/Library/Caches/npm"
	if err := st.RecordCacheSize(target, 100_000_000, now.Add(-4*24*time.Hour)); err != nil {
		t.Fatalf("seed old: %v", err)
	}
	if err := st.RecordCacheSize(target, 500_000_000, now); err != nil {
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

	bpd, ok := s.Evidence["bytes_per_day"].(float64)
	if !ok {
		t.Fatalf("Evidence missing bytes_per_day float64: %+v", s.Evidence)
	}
	// 400 MB / 4 days = 1.0e8 bytes/day, within a 1% tolerance for the
	// sub-second rounding the store round-trip may introduce.
	if bpd < 0.99e8 || bpd > 1.01e8 {
		t.Errorf("bytes_per_day = %v, want ~1.0e8", bpd)
	}
	if !strings.Contains(s.Reason, "/day") {
		t.Errorf("Reason = %q, want it to carry the /day rate", s.Reason)
	}
}

// TestCacheVelocityRateDivByZeroGuard proves same-second samples do not panic
// or divide by zero: with no elapsed time the rate is reported as zero rather
// than +Inf/NaN. (This is exactly why the pipeline needs a backdated baseline
// to form a real window — two live inserts in the same second yield rate 0.)
func TestCacheVelocityRateDivByZeroGuard(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "sameinstant.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = st.Close() }()

	ts := time.Now()
	target := "/Users/me/Library/Caches/same"
	// Two rows at the identical instant, 10x apart.
	if err := st.RecordCacheSize(target, 100_000_000, ts); err != nil {
		t.Fatalf("seed a: %v", err)
	}
	if err := st.RecordCacheSize(target, 1_000_000_000, ts); err != nil {
		t.Fatalf("seed b: %v", err)
	}

	cfg := config.Defaults()
	cfg.Heuristics.CacheVelocity.GrowthMultiplier = 2.0

	got := CacheVelocity(context.Background(), st, cfg)
	if len(got) != 1 {
		t.Fatalf("got %d suggestions, want 1: %+v", len(got), got)
	}
	bpd, ok := got[0].Evidence["bytes_per_day"].(float64)
	if !ok {
		t.Fatalf("Evidence missing bytes_per_day float64: %+v", got[0].Evidence)
	}
	if bpd != 0 {
		t.Errorf("bytes_per_day = %v, want 0 (no window => no div-by-zero)", bpd)
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
