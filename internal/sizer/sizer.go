//go:build darwin

// Package sizer measures how much disk space file trees REALLY occupy on
// APFS, where naive logical sizing (du, st_size sums) is fiction: APFS
// copy-on-write clones share physical extents (65 leaked Chrome
// code_sign_clone dirs du-reported 129 GB; deleting 64 of them freed ~3 GB),
// and sparse files report logical bytes that were never allocated.
//
// Three tiers, cheapest first:
//
//  1. Blocks — a filepath.WalkDir summing st_blocks*512 (allocated bytes) and
//     st_size (logical bytes), with hardlinks deduplicated by (dev, inode).
//     du-equivalent and honest on non-clone trees. Use this as the broad
//     default.
//
//  2. UniqueAllocated — the precision tier: per regular file, walk physical
//     extents via fcntl(F_LOG2PHYS_EXT) and union the intervals keyed by
//     (st_dev, devoffset). The sum of merged intervals is the tree's real
//     unique allocated bytes: extents shared between clones are counted
//     once, and holes in sparse files are never counted at all. Deleting
//     the measured tree can free at most this many bytes (minus whatever
//     extents are also referenced from OUTSIDE the measured paths).
//
//  3. FreedByDelete — ground truth after the fact: statfs available-bytes
//     on the volume before and after a delete action. This is what audit
//     rows should record, regardless of clone complexity.
//
// # Cost doctrine — UniqueAllocated is an on-demand instrument
//
// Measured on this machine (M-series, APFS, warm cache): ~34µs per file —
// 943 files in 74ms, 197k files in 6.7s. That is affordable for scoped
// targets (a leak-signature directory, an offload candidate, a drill-down
// the user asked for) and NOT affordable as an every-tick full-disk walk.
// Callers in periodic paths use Blocks; they escalate to UniqueAllocated
// only for the specific trees they are about to make a claim about.
//
// Symlinks are never followed (including a symlink given as the root path).
// Only regular files contribute bytes.
package sizer

import (
	"errors"
	"io/fs"
	"path/filepath"
	"syscall"
)

// blockUnit is the fixed unit of Stat_t.Blocks on darwin (always 512 bytes,
// independent of the filesystem block size).
const blockUnit = 512

// TreeSize is the result of a sizing walk. Fields not measured by the tier
// that produced it stay zero (Blocks fills Logical+Blocks; UniqueAllocated
// fills all three byte fields).
type TreeSize struct {
	// Files is the number of regular files measured. Hard-linked files are
	// counted once per (dev, inode), not once per name.
	Files int
	// Errs is the number of entries that could not be measured (stat/open
	// failures, unreadable dirs). The walk continues past them.
	Errs int
	// Logical is the sum of st_size — what `du --apparent-size` would say.
	Logical int64
	// Blocks is the sum of st_blocks*512 — allocated bytes as the catalog
	// reports them, counting clone-shared extents once PER FILE (so a
	// clone pair reports ~2x). Honest on non-clone trees, fiction on
	// clone-heavy ones.
	Blocks int64
	// UniqueAllocated is the size of the union of physical extents across
	// the measured paths — clone-aware and sparse-aware. Only filled by
	// the UniqueAllocated tier.
	UniqueAllocated int64
}

// devIno identifies an inode for hardlink deduplication.
type devIno struct {
	dev int32
	ino uint64
}

// Blocks walks path and returns logical (st_size) and allocated
// (st_blocks*512) byte totals. This is the cheap default tier: honest where
// no APFS clones are involved, and an over-count (like du) where they are.
// Symlinks are skipped; hardlinks are counted once per (dev, inode).
// Unreadable entries are tolerated and counted in Errs; only a failure to
// walk the root itself returns an error.
func Blocks(path string) (TreeSize, error) {
	ts := TreeSize{}
	seen := make(map[devIno]struct{})
	err := filepath.WalkDir(path, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if p == path {
				return err // root itself unwalkable
			}
			ts.Errs++
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			if !errors.Is(err, fs.ErrNotExist) {
				ts.Errs++
			}
			return nil
		}
		st, ok := info.Sys().(*syscall.Stat_t)
		if !ok {
			ts.Errs++
			return nil
		}
		if st.Nlink > 1 {
			key := devIno{dev: st.Dev, ino: st.Ino}
			if _, dup := seen[key]; dup {
				return nil
			}
			seen[key] = struct{}{}
		}
		ts.Files++
		ts.Logical += st.Size
		ts.Blocks += st.Blocks * blockUnit
		return nil
	})
	if err != nil {
		return TreeSize{}, err
	}
	return ts, nil
}
