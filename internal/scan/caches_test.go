package scan

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/FRIKKern/noo-noo/internal/sizer"
)

// TestScanCachesWritesSample asserts a sample is recorded for each cache root,
// is retrievable via CacheSizeSeries, and carries ALLOCATED bytes (sizer.Blocks
// — st_blocks*512), not the old logical st_size sum. A 1000-byte file occupies
// at least one 4 KiB block, so allocated > logical proves walkSize is gone.
func TestScanCachesWritesSample(t *testing.T) {
	tmp := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmp, "blob"), make([]byte, 1000), 0o644); err != nil {
		t.Fatal(err)
	}

	st := openTestStore(t)
	if err := scanCaches(context.Background(), []string{tmp}, st); err != nil {
		t.Fatalf("scanCaches: %v", err)
	}

	since := time.Now().Add(-time.Hour)
	samples, err := st.CacheSizeSeries(tmp, since)
	if err != nil {
		t.Fatalf("CacheSizeSeries: %v", err)
	}
	if len(samples) != 1 {
		t.Fatalf("expected 1 sample, got %d", len(samples))
	}
	// The recorded value is exactly what sizer.Blocks reports — allocated
	// bytes, block-aligned, and strictly greater than the 1000 logical bytes
	// the retired walkSize would have summed.
	want, err := sizer.Blocks(tmp)
	if err != nil {
		t.Fatalf("sizer.Blocks: %v", err)
	}
	if samples[0].Bytes != want.Blocks {
		t.Errorf("bytes = %d, want allocated %d", samples[0].Bytes, want.Blocks)
	}
	if samples[0].Bytes <= 1000 {
		t.Errorf("bytes = %d, want > 1000 (allocated exceeds logical; walkSize would return 1000)", samples[0].Bytes)
	}
	if samples[0].Bytes%512 != 0 {
		t.Errorf("bytes = %d, want a multiple of 512 (allocated blocks)", samples[0].Bytes)
	}
	if samples[0].TargetPath != tmp {
		t.Errorf("target = %q, want %q", samples[0].TargetPath, tmp)
	}
}

// TestScanCachesMissingRootIsTolerated ensures a non-existent cache root
// is silently skipped (caches come and go on a developer machine).
func TestScanCachesMissingRootIsTolerated(t *testing.T) {
	st := openTestStore(t)
	err := scanCaches(context.Background(), []string{"/this/really/does/not/exist"}, st)
	if err != nil {
		t.Fatalf("missing cache root should be tolerated, got %v", err)
	}
}
