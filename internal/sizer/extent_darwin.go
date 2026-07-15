//go:build darwin

package sizer

import (
	"encoding/binary"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"
)

// log2phys mirrors darwin's struct log2phys (sys/fcntl.h) used by
// fcntl(F_LOG2PHYS_EXT). On input, Devoffset is the LOGICAL file offset to
// query and Contigbytes is the maximum number of bytes to map; on output,
// Contigbytes is the length of the physical extent and Devoffset is its
// PHYSICAL offset on the device.
type log2phys struct {
	Flags       uint32
	Contigbytes int64
	Devoffset   int64
}

// log2physSize is the wire size of struct log2phys. The C struct is
// declared under #pragma pack(4): uint32 flags at [0:4], int64 contigbytes
// at [4:12], int64 devoffset at [12:20] — 20 bytes total. Go's natural
// struct layout pads flags to 8 bytes (24 bytes total), so passing a Go
// struct pointer to the fcntl silently returns garbage. The buffer MUST be
// hand-marshaled. Verified live on this machine (macOS 15, arm64).
const log2physSize = 20

// marshalLog2Phys packs l into the 20-byte pack(4) wire layout
// (little-endian, as arm64/x86-64 darwin are both LE).
func marshalLog2Phys(l log2phys) [log2physSize]byte {
	var b [log2physSize]byte
	binary.LittleEndian.PutUint32(b[0:4], l.Flags)
	binary.LittleEndian.PutUint64(b[4:12], uint64(l.Contigbytes))
	binary.LittleEndian.PutUint64(b[12:20], uint64(l.Devoffset))
	return b
}

// unmarshalLog2Phys unpacks the 20-byte pack(4) wire layout.
func unmarshalLog2Phys(b [log2physSize]byte) log2phys {
	return log2phys{
		Flags:       binary.LittleEndian.Uint32(b[0:4]),
		Contigbytes: int64(binary.LittleEndian.Uint64(b[4:12])),
		Devoffset:   int64(binary.LittleEndian.Uint64(b[12:20])),
	}
}

// log2physExt asks the filesystem for the physical extent backing the file
// at logical offset off, mapping at most maxBytes. It returns an error for
// unallocated regions (holes, not-yet-flushed delayed allocations).
func log2physExt(fd uintptr, off, maxBytes int64) (log2phys, error) {
	buf := marshalLog2Phys(log2phys{Contigbytes: maxBytes, Devoffset: off})
	_, err := unix.FcntlInt(fd, unix.F_LOG2PHYS_EXT, int(uintptr(unsafe.Pointer(&buf[0]))))
	runtime.KeepAlive(&buf)
	if err != nil {
		return log2phys{}, err
	}
	return unmarshalLog2Phys(buf), nil
}

// extent is a half-open physical byte interval [start, end) on one device.
type extent struct {
	start, end int64
}

// extentSet accumulates physical extents keyed by device. The union MUST be
// per-device: devoffset is a per-volume physical address, so a single global
// union would falsely merge same-offset extents of files on different
// volumes.
type extentSet struct {
	byDev map[int32][]extent
}

func newExtentSet() *extentSet {
	return &extentSet{byDev: make(map[int32][]extent)}
}

func (s *extentSet) add(dev int32, start, length int64) {
	if length <= 0 {
		return
	}
	s.byDev[dev] = append(s.byDev[dev], extent{start: start, end: start + length})
}

// total merges overlapping/adjacent extents per device and returns the
// summed length of the union — the unique allocated bytes.
func (s *extentSet) total() int64 {
	var sum int64
	for _, exts := range s.byDev {
		sort.Slice(exts, func(i, j int) bool { return exts[i].start < exts[j].start })
		var curStart, curEnd int64
		open := false
		for _, e := range exts {
			if !open {
				curStart, curEnd, open = e.start, e.end, true
				continue
			}
			if e.start <= curEnd {
				if e.end > curEnd {
					curEnd = e.end
				}
				continue
			}
			sum += curEnd - curStart
			curStart, curEnd = e.start, e.end
		}
		if open {
			sum += curEnd - curStart
		}
	}
	return sum
}

// fsBlockSize returns the filesystem's allocation block size for the file
// behind fd, falling back to 4096.
func fsBlockSize(fd uintptr) int64 {
	var st unix.Statfs_t
	if err := unix.Fstatfs(int(fd), &st); err != nil || st.Bsize == 0 {
		return 4096
	}
	return int64(st.Bsize)
}

// addFileExtents walks the physical extents of the open file and adds them
// to the set. Hole semantics verified live on APFS (macOS 15): querying
// inside a hole SUCCEEDS with devoffset = -1 and contigbytes spanning the
// hole — those regions are skipped, not counted. Regions where the fcntl
// errors instead are skipped via SEEK_DATA. Returns the number of regions
// that could not be mapped or skipped cleanly.
func addFileExtents(fd uintptr, dev int32, size int64, set *extentSet) (errs int) {
	if size <= 0 {
		return 0
	}
	bs := fsBlockSize(fd)
	off := int64(0)
	for off < size {
		// Request up to the block-rounded remainder so the trailing
		// partial block is counted as allocated (it is).
		remain := size - off
		if r := remain % bs; r != 0 {
			remain += bs - r
		}
		l2p, err := log2physExt(fd, off, remain)
		if err == nil && l2p.Contigbytes > 0 {
			if l2p.Devoffset >= 0 {
				// Real extent. A hole reports devoffset -1 and
				// is skipped: it occupies no space.
				set.add(dev, l2p.Devoffset, l2p.Contigbytes)
			}
			off += l2p.Contigbytes
			continue
		}
		// Hole (or unallocated region): jump to the next data region.
		next, serr := unix.Seek(int(fd), off, unix.SEEK_DATA)
		if serr != nil {
			// ENXIO: no more data past off — the rest is hole.
			if !errors.Is(serr, unix.ENXIO) {
				errs++
			}
			return errs
		}
		if next <= off {
			// Data is here but the extent query failed — bail out
			// rather than loop forever; count it once.
			errs++
			return errs
		}
		off = next
	}
	return errs
}

// UniqueAllocated measures the REAL unique allocated bytes of the given
// trees: it walks the physical extents of every regular file via
// fcntl(F_LOG2PHYS_EXT) and unions them keyed by (st_dev, devoffset).
// Extents shared by APFS clones — within one tree or across the given
// trees — are counted exactly once, and sparse holes are not counted at
// all. This is the number a delete of all given paths could at most free
// (extents also referenced from outside the measured paths will not be
// freed and cannot be detected from here; FreedByDelete is the ground
// truth after the fact).
//
// Cost: ~34µs per file (see the package doc) — use it scoped and
// on-demand, never as a periodic full-disk walk. Symlinks are skipped;
// hardlinks are deduplicated by (dev, inode); Logical and Blocks are
// filled alongside UniqueAllocated so callers can show the du-fiction
// delta directly.
func UniqueAllocated(paths ...string) (TreeSize, error) {
	ts := TreeSize{}
	set := newExtentSet()
	seen := make(map[devIno]struct{})
	for _, root := range paths {
		err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				if p == root {
					return err
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
			key := devIno{dev: st.Dev, ino: st.Ino}
			if _, dup := seen[key]; dup {
				return nil
			}
			seen[key] = struct{}{}
			ts.Files++
			ts.Logical += st.Size
			ts.Blocks += st.Blocks * blockUnit
			f, err := os.OpenFile(p, os.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
			if err != nil {
				ts.Errs++
				return nil
			}
			ts.Errs += addFileExtents(f.Fd(), st.Dev, st.Size, set)
			f.Close()
			return nil
		})
		if err != nil {
			return TreeSize{}, err
		}
	}
	ts.UniqueAllocated = set.total()
	return ts, nil
}
