//go:build darwin

package sizer

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// TestFreedByDeleteMeasuresRealBytes: deleting a synced 32MB file must show
// up as roughly 32MB of newly-available volume space. The bound is half the
// file size to tolerate unrelated volume churn during the measurement.
func TestFreedByDeleteMeasuresRealBytes(t *testing.T) {
	dir := t.TempDir()
	size := int64(32 * testMB)
	p := filepath.Join(dir, "victim.bin")
	writeFilled(t, p, size)

	freed, err := FreedByDelete(dir, func() error {
		return os.Remove(p)
	})
	if err != nil {
		t.Fatalf("FreedByDelete: %v", err)
	}
	if freed < size/2 {
		t.Errorf("freed = %d bytes, want >= %d (delete of a %d-byte file)", freed, size/2, size)
	}
}

func TestFreedByDeletePropagatesFnError(t *testing.T) {
	sentinel := errors.New("delete failed")
	_, err := FreedByDelete(t.TempDir(), func() error { return sentinel })
	if !errors.Is(err, sentinel) {
		t.Errorf("err = %v, want the fn error to propagate", err)
	}
}

func TestFreedByDeleteBadVolumeErrors(t *testing.T) {
	if _, err := FreedByDelete(filepath.Join(t.TempDir(), "nope"), func() error { return nil }); err == nil {
		t.Error("FreedByDelete on a missing volume path should return an error")
	}
}
