package offload

import (
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"syscall"

	"github.com/FRIKKern/noo-noo/internal/core"
	"github.com/FRIKKern/noo-noo/internal/sizer"
)

// SizeFns is the sizing seam, defaulting to internal/sizer (the wave's
// truth-sizing package, charter D2). Injectable so failure-path tests can
// fake sizes without real trees.
type SizeFns struct {
	// Blocks returns allocated bytes (st_blocks * 512) under path.
	Blocks func(path string) (core.Bytes, error)
	// UniqueAllocated returns bytes that would ACTUALLY be reclaimed if the
	// tree were the only owner of its blocks (APFS clones share blocks and
	// make naive sums wildly over-report).
	UniqueAllocated func(path string) (core.Bytes, error)
	// FreedByDelete estimates the local bytes a delete/relocate of path
	// frees, BEFORE the tree is touched. Defaults to the clone-aware
	// extent-union (deleting a tree frees at most its unique allocated
	// bytes; extents also referenced from outside it stay allocated).
	FreedByDelete func(path string) (core.Bytes, error)
}

// sizerUnique adapts sizer.UniqueAllocated (clone- and sparse-aware
// extent-union) to the seam's single-path shape.
func sizerUnique(path string) (core.Bytes, error) {
	ts, err := sizer.UniqueAllocated(path)
	if err != nil {
		return 0, err
	}
	return core.Bytes(ts.UniqueAllocated), nil
}

// fill returns s with nil fields replaced by the production defaults.
func (s SizeFns) fill() SizeFns {
	if s.Blocks == nil {
		// allocatedBlocks rather than sizer.Blocks: it surfaces a "partial:
		// N unreadable" error alongside the partial total, which Scan turns
		// into size_error evidence instead of silently under-reporting.
		s.Blocks = allocatedBlocks
	}
	if s.UniqueAllocated == nil {
		s.UniqueAllocated = sizerUnique
	}
	if s.FreedByDelete == nil {
		s.FreedByDelete = sizerUnique
	}
	return s
}

// allocatedBlocks sums st_blocks*512 for every regular file under path —
// the allocated (not logical) size, which already ignores sparse-file holes.
// Symlinks are not followed. Unreadable entries are skipped and counted:
// a single permission-denied file must not collapse a 8 GB tree to "0 B" —
// the partial total plus an error describing the gap is the honest answer.
func allocatedBlocks(path string) (core.Bytes, error) {
	var total core.Bytes
	var denied int
	walkErr := filepath.WalkDir(path, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			switch {
			case errors.Is(err, fs.ErrNotExist):
				return nil // raced with a concurrent delete
			case errors.Is(err, fs.ErrPermission):
				denied++
				if d != nil && d.IsDir() {
					return fs.SkipDir
				}
				return nil
			default:
				return err
			}
		}
		if !d.Type().IsRegular() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			if errors.Is(err, fs.ErrPermission) {
				denied++
				return nil
			}
			return err
		}
		if st, ok := info.Sys().(*syscall.Stat_t); ok {
			total += core.Bytes(st.Blocks * 512)
		} else {
			total += core.Bytes(info.Size())
		}
		return nil
	})
	if walkErr != nil {
		return total, walkErr
	}
	if denied > 0 {
		return total, fmt.Errorf("partial: %d unreadable entr(ies) skipped under %s", denied, path)
	}
	return total, nil
}

// countAndBytes walks path counting regular files and their total LOGICAL
// bytes. Used to verify a copy: ditto preserves logical content, so count
// and logical bytes must match between source and destination even when
// allocated blocks differ (clones, compression).
func countAndBytes(path string) (files int64, bytes core.Bytes, err error) {
	err = filepath.WalkDir(path, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.Type().IsRegular() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		files++
		bytes += core.Bytes(info.Size())
		return nil
	})
	if err != nil {
		return 0, 0, err
	}
	return files, bytes, nil
}
