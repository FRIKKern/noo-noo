package scan

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/FRIKKern/noo-noo/internal/config"
	"github.com/FRIKKern/noo-noo/internal/heuristics"
	"github.com/FRIKKern/noo-noo/internal/store"
)

// openTestStore opens a fresh on-disk store under t.TempDir(). We use a real
// SQLite file (not in-memory) because the store package is built around
// modernc.org/sqlite + WAL mode, which expects a path. The temp dir is
// cleaned up automatically at end of test.
func openTestStore(t *testing.T) *store.Store {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func TestScanRootsNilStoreErrors(t *testing.T) {
	if err := ScanRoots(context.Background(), Roots{}, nil); err == nil {
		t.Fatal("nil store should error")
	}
}

func TestScanRootsEmpty(t *testing.T) {
	st := openTestStore(t)
	if err := ScanRoots(context.Background(), Roots{}, st); err != nil {
		t.Fatalf("empty roots should succeed: %v", err)
	}
}

// TestVelocityPipelineEndToEnd exercises the whole vertical the config field
// lights up: a Caches root flows through ScanRoots -> cache_size_history, and
// with a backdated baseline the CacheVelocity heuristic fires for that exact
// target. This is the regression that proves the pipeline is no longer dead
// (Caches:nil at both call sites previously recorded zero rows).
func TestVelocityPipelineEndToEnd(t *testing.T) {
	tmp := t.TempDir()
	// Real bytes so sizer.Blocks reports a non-zero live sample.
	if err := os.WriteFile(filepath.Join(tmp, "blob"), make([]byte, 200_000), 0o644); err != nil {
		t.Fatal(err)
	}
	st := openTestStore(t)

	// (a) A cache-roots scan writes exactly one cache_size_history row.
	if err := ScanRoots(context.Background(), Roots{Caches: []string{tmp}}, st); err != nil {
		t.Fatalf("ScanRoots: %v", err)
	}
	since := time.Now().Add(-time.Hour)
	live, err := st.CacheSizeSeries(tmp, since)
	if err != nil {
		t.Fatalf("CacheSizeSeries: %v", err)
	}
	if len(live) != 1 {
		t.Fatalf("expected 1 live sample from ScanRoots, got %d", len(live))
	}
	current := live[0].Bytes
	if current <= 0 {
		t.Fatalf("live sample bytes = %d, want > 0", current)
	}

	// (b) Backdate a baseline 6 days ago at 1/10th of the current size: two
	// same-second live inserts make no window, so the backdated row is what
	// gives CacheVelocity a >2x, multi-day window to fire on.
	baseline := current / 10
	if baseline < 1 {
		baseline = 1
	}
	if err := st.RecordCacheSize(tmp, baseline, time.Now().Add(-6*24*time.Hour)); err != nil {
		t.Fatalf("seed backdated baseline: %v", err)
	}

	cfg := config.Defaults()
	cfg.Heuristics.CacheVelocity.GrowthMultiplier = 2.0
	cfg.Heuristics.CacheVelocity.WindowDays = 7

	got := heuristics.CacheVelocity(context.Background(), st, cfg)
	var found *heuristics.Suggestion
	for i := range got {
		if got[i].Target == tmp {
			found = &got[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("CacheVelocity did not fire for %s; got %+v", tmp, got)
	}
	bpd, ok := found.Evidence["bytes_per_day"].(float64)
	if !ok {
		t.Fatalf("Evidence missing bytes_per_day float64: %+v", found.Evidence)
	}
	if bpd <= 0 {
		t.Errorf("bytes_per_day = %v, want > 0 over the 6-day window", bpd)
	}
}
