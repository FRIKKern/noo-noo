package scan

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestWalkSize(t *testing.T) {
	tmp := t.TempDir()
	// Three files of known sizes, two nested.
	sizes := map[string]int64{"a": 100, "b/c": 200, "b/d/e": 300}
	var want int64
	for p, n := range sizes {
		full := filepath.Join(tmp, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, make([]byte, n), 0o644); err != nil {
			t.Fatal(err)
		}
		want += n
	}
	got, err := walkSize(context.Background(), tmp)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Errorf("walkSize = %d, want %d", got, want)
	}
}

func TestWalkSizeSkipsSymlinks(t *testing.T) {
	tmp := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmp, "real"), make([]byte, 100), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/etc/hosts", filepath.Join(tmp, "link")); err != nil {
		t.Skip(err)
	}
	got, _ := walkSize(context.Background(), tmp)
	if got != 100 {
		t.Errorf("walkSize = %d, want 100 (symlink skipped)", got)
	}
}

func TestWalkSizeMissingRoot(t *testing.T) {
	_, err := walkSize(context.Background(), "/this/really/does/not/exist")
	if err == nil {
		t.Fatal("expected error for missing root")
	}
}

// TestScanCachesWritesSample asserts a sample is recorded for each cache root
// and is retrievable via CacheSizeSeries.
func TestScanCachesWritesSample(t *testing.T) {
	tmp := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmp, "blob"), make([]byte, 1024), 0o644); err != nil {
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
	if samples[0].Bytes != 1024 {
		t.Errorf("bytes = %d, want 1024", samples[0].Bytes)
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
