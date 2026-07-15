//go:build darwin

package sizer

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// TestLog2PhysWireLayout pins the hand-marshaled 20-byte #pragma pack(4)
// layout of darwin's struct log2phys: flags uint32 at [0:4], contigbytes
// int64 LE at [4:12], devoffset int64 LE at [12:20]. Go's natural struct
// layout would be 24 bytes and silently corrupt the fcntl exchange.
func TestLog2PhysWireLayout(t *testing.T) {
	in := log2phys{
		Flags:       0xAABBCCDD,
		Contigbytes: 0x1122334455667788,
		Devoffset:   0x0102030405060708,
	}
	b := marshalLog2Phys(in)
	want := [log2physSize]byte{
		0xDD, 0xCC, 0xBB, 0xAA, // flags u32 LE @ [0:4]
		0x88, 0x77, 0x66, 0x55, 0x44, 0x33, 0x22, 0x11, // contigbytes i64 LE @ [4:12]
		0x08, 0x07, 0x06, 0x05, 0x04, 0x03, 0x02, 0x01, // devoffset i64 LE @ [12:20]
	}
	if b != want {
		t.Errorf("marshalLog2Phys layout mismatch:\n got %x\nwant %x", b, want)
	}
	if got := unmarshalLog2Phys(b); got != in {
		t.Errorf("unmarshal roundtrip: got %+v, want %+v", got, in)
	}
	if log2physSize != 20 {
		t.Errorf("log2physSize = %d, want 20 (pack(4) wire size)", log2physSize)
	}
}

// TestExtentSetCrossDeviceNotMerged proves the union is keyed by
// (st_dev, devoffset): identical physical offsets on DIFFERENT devices are
// distinct space and must both be counted.
func TestExtentSetCrossDeviceNotMerged(t *testing.T) {
	s := newExtentSet()
	s.add(1, 4096, 4096)
	s.add(2, 4096, 4096) // same devoffset, different device
	if got := s.total(); got != 8192 {
		t.Errorf("cross-device same-offset extents merged: total = %d, want 8192", got)
	}
}

func TestExtentSetUnion(t *testing.T) {
	cases := []struct {
		name string
		add  [][3]int64 // dev, start, length
		want int64
	}{
		{"identical extents count once", [][3]int64{{1, 0, 4096}, {1, 0, 4096}}, 4096},
		{"overlap merges", [][3]int64{{1, 0, 100}, {1, 50, 100}}, 150},
		{"adjacent merges", [][3]int64{{1, 0, 100}, {1, 100, 50}}, 150},
		{"disjoint sums", [][3]int64{{1, 0, 100}, {1, 200, 100}}, 200},
		{"contained absorbs", [][3]int64{{1, 0, 1000}, {1, 100, 100}}, 1000},
		{"zero length ignored", [][3]int64{{1, 0, 0}, {1, 10, -5}}, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newExtentSet()
			for _, a := range tc.add {
				s.add(int32(a[0]), a[1], a[2])
			}
			if got := s.total(); got != tc.want {
				t.Errorf("total = %d, want %d", got, tc.want)
			}
		})
	}
}

// TestExtentWalkMatchesAllocated is the live proof that the pack(4)
// marshaling is right: with a garbage layout the extent walk of a plain
// 8MB file would return nonsense, not ~8MB.
func TestExtentWalkMatchesAllocated(t *testing.T) {
	dir := t.TempDir()
	requireAPFS(t, dir)
	size := int64(8 * testMB)
	p := filepath.Join(dir, "plain.bin")
	writeFilled(t, p, size)

	f, err := os.Open(p)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	set := newExtentSet()
	if errs := addFileExtents(f.Fd(), 0, st.Size(), set); errs != 0 {
		t.Errorf("addFileExtents errs = %d, want 0", errs)
	}
	got := set.total()
	if got < size || got > size*105/100 {
		t.Errorf("extent union = %d bytes, want ~%d (within +5%%)", got, size)
	}
}

// TestCloneAwareness is the founding-incident regression: an APFS clone
// pair du-reports (st_blocks) ~2x while the real unique allocated space is
// ~1x; diverging 1MB of the clone adds ~1MB of unique space.
func TestCloneAwareness(t *testing.T) {
	dir := t.TempDir()
	requireAPFS(t, dir)
	size := int64(8 * testMB)
	src := filepath.Join(dir, "src.bin")
	clone := filepath.Join(dir, "clone.bin")
	writeFilled(t, src, size)
	if err := unix.Clonefile(src, clone, 0); err != nil {
		t.Fatalf("clonefile: %v", err)
	}

	b, err := Blocks(dir)
	if err != nil {
		t.Fatalf("Blocks: %v", err)
	}
	if min := 2 * size * 9 / 10; b.Blocks < min {
		t.Errorf("Blocks (du tier) = %d, want >= %d (~2x: clones double-counted per file)", b.Blocks, min)
	}

	u, err := UniqueAllocated(dir)
	if err != nil {
		t.Fatalf("UniqueAllocated: %v", err)
	}
	if u.Files != 2 {
		t.Errorf("Files = %d, want 2", u.Files)
	}
	if min, max := size*95/100, size*13/10; u.UniqueAllocated < min || u.UniqueAllocated > max {
		t.Errorf("UniqueAllocated = %d, want ~%d (clone pair shares extents; [%d, %d])",
			u.UniqueAllocated, size, min, max)
	}
	if u.Blocks < 2*size*9/10 {
		t.Errorf("UniqueAllocated tier Blocks = %d, want >= %d (fills the du-fiction delta)", u.Blocks, 2*size*9/10)
	}

	// Diverge 1MB of the clone: unique allocated must grow by ~1MB.
	f, err := os.OpenFile(clone, os.O_WRONLY, 0)
	if err != nil {
		t.Fatalf("open clone: %v", err)
	}
	div := make([]byte, 1*testMB)
	for i := range div {
		div[i] = byte(255 - i%251)
	}
	if _, err := f.WriteAt(div, 2*testMB); err != nil {
		f.Close()
		t.Fatalf("diverge write: %v", err)
	}
	fullSync(t, f)
	f.Close()

	u2, err := UniqueAllocated(dir)
	if err != nil {
		t.Fatalf("UniqueAllocated after divergence: %v", err)
	}
	added := u2.UniqueAllocated - u.UniqueAllocated
	if min, max := int64(1*testMB)*8/10, int64(2*testMB); added < min || added > max {
		t.Errorf("1MB divergence added %d unique bytes, want ~%d ([%d, %d])",
			added, 1*testMB, min, max)
	}
}

// TestSparseAwareness: a truncate-extended hole must not count as allocated
// space in either the st_blocks tier or the extent-union tier.
func TestSparseAwareness(t *testing.T) {
	dir := t.TempDir()
	requireAPFS(t, dir)
	p := filepath.Join(dir, "sparse.bin")
	writeFilled(t, p, 1*testMB)
	logical := int64(64 * testMB)
	if err := os.Truncate(p, logical); err != nil {
		t.Fatalf("truncate: %v", err)
	}

	b, err := Blocks(dir)
	if err != nil {
		t.Fatalf("Blocks: %v", err)
	}
	if b.Logical != logical {
		t.Errorf("Logical = %d, want %d", b.Logical, logical)
	}
	if max := int64(4 * testMB); b.Blocks > max {
		t.Errorf("Blocks = %d, want <= %d (hole must not be allocated)", b.Blocks, max)
	}

	u, err := UniqueAllocated(dir)
	if err != nil {
		t.Fatalf("UniqueAllocated: %v", err)
	}
	if u.Logical != logical {
		t.Errorf("Logical = %d, want %d", u.Logical, logical)
	}
	if min, max := int64(1*testMB)*9/10, int64(4*testMB); u.UniqueAllocated < min || u.UniqueAllocated > max {
		t.Errorf("UniqueAllocated = %d, want ~%d ([%d, %d]: data counted, hole skipped)",
			u.UniqueAllocated, 1*testMB, min, max)
	}
}

// TestUniqueAllocatedCrossTreeClone: extent sharing is detected ACROSS the
// given roots, not just within one tree — measuring a clone and its origin
// together must not double-count.
func TestUniqueAllocatedCrossTreeClone(t *testing.T) {
	base := t.TempDir()
	requireAPFS(t, base)
	dirA := filepath.Join(base, "a")
	dirB := filepath.Join(base, "b")
	for _, d := range []string{dirA, dirB} {
		if err := os.Mkdir(d, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
	}
	size := int64(4 * testMB)
	writeFilled(t, filepath.Join(dirA, "orig.bin"), size)
	if err := unix.Clonefile(filepath.Join(dirA, "orig.bin"), filepath.Join(dirB, "clone.bin"), 0); err != nil {
		t.Fatalf("clonefile: %v", err)
	}

	u, err := UniqueAllocated(dirA, dirB)
	if err != nil {
		t.Fatalf("UniqueAllocated: %v", err)
	}
	if min, max := size*95/100, size*13/10; u.UniqueAllocated < min || u.UniqueAllocated > max {
		t.Errorf("cross-tree clone pair UniqueAllocated = %d, want ~%d ([%d, %d])",
			u.UniqueAllocated, size, min, max)
	}
}

// TestUniqueAllocatedThousandFilesUnder2s pins the documented cost envelope
// (~34µs/file): a 1000-file tree must complete well under 2 seconds.
func TestUniqueAllocatedThousandFilesUnder2s(t *testing.T) {
	dir := t.TempDir()
	requireAPFS(t, dir)
	buf := make([]byte, 4096)
	for i := range buf {
		buf[i] = byte(i%251 + 1)
	}
	for i := 0; i < 1000; i++ {
		p := filepath.Join(dir, fmt.Sprintf("f%04d.bin", i))
		f, err := os.Create(p)
		if err != nil {
			t.Fatalf("create %s: %v", p, err)
		}
		if _, err := f.Write(buf); err != nil {
			t.Fatalf("write %s: %v", p, err)
		}
		if err := f.Sync(); err != nil {
			t.Fatalf("sync %s: %v", p, err)
		}
		f.Close()
	}

	start := time.Now()
	ts, err := UniqueAllocated(dir)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("UniqueAllocated: %v", err)
	}
	if ts.Files != 1000 {
		t.Errorf("Files = %d, want 1000", ts.Files)
	}
	if ts.UniqueAllocated == 0 {
		t.Errorf("UniqueAllocated = 0, want > 0 (1000 distinct 4KB files)")
	}
	if elapsed >= 2*time.Second {
		t.Errorf("UniqueAllocated over 1000 files took %v, want < 2s", elapsed)
	}
	t.Logf("UniqueAllocated over %d files: %v (%.1fµs/file), unique=%d",
		ts.Files, elapsed, float64(elapsed.Microseconds())/float64(ts.Files), ts.UniqueAllocated)
}

func TestUniqueAllocatedMissingRootErrors(t *testing.T) {
	if _, err := UniqueAllocated(filepath.Join(t.TempDir(), "nope")); err == nil {
		t.Error("UniqueAllocated on a missing root should return an error")
	}
}
