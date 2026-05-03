package scan

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/FRIKKern/noo-noo/internal/store"
)

func init() {
	// Override the no-op default installed by scan.go (T84) with the real
	// cache-size collector implemented in this file (T86).
	scanCachesFn = scanCaches
}

// scanCaches walks each cache root, sums bytes, and inserts one
// cache_size_history row per top-level cache directory. Missing roots are
// tolerated; only ctx cancellation aborts.
func scanCaches(ctx context.Context, roots []string, st *store.Store) error {
	now := time.Now()
	for _, root := range roots {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		size, err := walkSize(ctx, root)
		if err != nil {
			// missing or unreadable cache root is fine: skip and continue
			continue
		}
		if err := st.RecordCacheSize(root, size, now); err != nil {
			return err
		}
	}
	return nil
}

// walkSize sums all regular file sizes under root. Symlinks are skipped to
// avoid double-counting (or escaping the root entirely). Per-entry errors
// during the walk are tolerated; the function only returns an error when
// the root itself can't be opened or the context is cancelled.
func walkSize(ctx context.Context, root string) (int64, error) {
	// Stat the root up-front so a missing/unreadable cache root surfaces
	// as an error to the caller, who decides whether to skip or propagate.
	if _, err := os.Stat(root); err != nil {
		return 0, err
	}
	var total int64
	err := filepath.WalkDir(root, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // tolerate per-entry errors
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		if d.IsDir() {
			return nil
		}
		if d.Type()&fs.ModeSymlink != 0 {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		total += info.Size()
		return nil
	})
	return total, err
}
