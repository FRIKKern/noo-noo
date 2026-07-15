package offload

import (
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"syscall"

	"github.com/FRIKKern/noo-noo/internal/core"
)

// SizeFns is the sizing seam. The wave's sizer slice (internal/sizer,
// nn-w1-sizer-truth) owns APFS-clone-aware measurement; this module merges
// LAST, so until the rebase wires these fields to sizer.Blocks,
// sizer.UniqueAllocated and sizer.FreedByDelete, the defaults below give an
// honest allocated-blocks (st_blocks) measure — never naive logical du.
type SizeFns struct {
	// Blocks returns allocated bytes (st_blocks * 512) under path.
	Blocks func(path string) (core.Bytes, error)
	// UniqueAllocated returns bytes that would ACTUALLY be reclaimed if the
	// tree were the only owner of its blocks (APFS clones share blocks and
	// make naive sums wildly over-report).
	UniqueAllocated func(path string) (core.Bytes, error)
	// FreedByDelete returns the real local bytes a delete/relocate of path
	// frees.
	FreedByDelete func(path string) (core.Bytes, error)
}

// fill returns s with nil fields replaced by the fallback implementations.
func (s SizeFns) fill() SizeFns {
	if s.Blocks == nil {
		s.Blocks = allocatedBlocks
	}
	if s.UniqueAllocated == nil {
		// Honest fallback: allocated blocks is an UPPER BOUND on unique
		// bytes (clone-shared blocks are counted in full). The sizer wiring
		// replaces this with the true clone-aware figure.
		s.UniqueAllocated = allocatedBlocks
	}
	if s.FreedByDelete == nil {
		s.FreedByDelete = allocatedBlocks
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
