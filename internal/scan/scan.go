// Package scan walks the filesystem and populates the noo-noo store with
// fresh data for the heuristics package to score.
//
// This package replaces a Phase 0.2 limitation where the daemon scheduler
// re-scored whatever happened to be in the store; if the user never ran
// `noo-noo scan` from the CLI, the daemon would re-score stale (often empty)
// data forever. With ScanRoots called from each scheduler tick (and from
// pressure-triggered scans), the store always has fresh data before
// heuristics run.
package scan

import (
	"context"
	"errors"

	"github.com/FRIKKern/noo-noo/internal/store"
)

// Roots groups the two kinds of paths we walk.
type Roots struct {
	Repos  []string // e.g. ["~/Code", "~/Projects"]
	Caches []string // e.g. ["~/Library/Caches/Homebrew", "~/Library/Developer/Xcode/DerivedData"]
}

// scanReposFn / scanCachesFn are package-level function variables so that
// the per-collector files (repos.go in T85, caches.go in T86) can override
// them without needing to touch scan.go after T84 lands. The defaults are
// no-ops to keep the T84 skeleton self-contained.
var (
	scanReposFn  = func(_ context.Context, _ []string, _ *store.Store) error { return nil }
	scanCachesFn = func(_ context.Context, _ []string, _ *store.Store) error { return nil }
)

// ScanRoots runs the repo-idleness and cache-size collectors over the
// configured roots, writing results into st. Idempotent: calling twice
// inserts a fresh sample per asset (timestamped), which is what the
// velocity heuristic wants.
//
// Errors during a single root are tolerated by the collectors themselves;
// only fatal errors (e.g. ctx cancellation, store I/O failure) bubble up.
func ScanRoots(ctx context.Context, roots Roots, st *store.Store) error {
	if st == nil {
		return errors.New("scan: store is nil")
	}
	if err := scanReposFn(ctx, roots.Repos, st); err != nil {
		return err
	}
	if err := scanCachesFn(ctx, roots.Caches, st); err != nil {
		return err
	}
	return nil
}
