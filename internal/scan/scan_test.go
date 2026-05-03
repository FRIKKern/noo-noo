package scan

import (
	"context"
	"path/filepath"
	"testing"

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
