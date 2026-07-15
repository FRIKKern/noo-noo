package scan

import (
	"context"
	"time"

	"github.com/FRIKKern/noo-noo/internal/sizer"
	"github.com/FRIKKern/noo-noo/internal/store"
)

func init() {
	// Override the no-op default installed by scan.go (T84) with the real
	// cache-size collector implemented in this file (T86).
	scanCachesFn = scanCaches
}

// scanCaches walks each cache root, measures allocated bytes, and inserts one
// cache_size_history row per top-level cache directory. Missing roots are
// tolerated; only ctx cancellation aborts.
//
// Sizing uses sizer.Blocks (st_blocks*512, the cheap allocated-bytes tier,
// charter D1) rather than a naive st_size sum: on APFS the logical total is
// du-fiction (clones share extents, sparse files over-report), and the
// velocity heuristic wants the disk-truth trend, not the logical one.
func scanCaches(ctx context.Context, roots []string, st *store.Store) error {
	now := time.Now()
	for _, root := range roots {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		ts, err := sizer.Blocks(root)
		if err != nil {
			// missing or unreadable cache root is fine: skip and continue
			continue
		}
		if err := st.RecordCacheSize(root, ts.Blocks, now); err != nil {
			return err
		}
	}
	return nil
}
