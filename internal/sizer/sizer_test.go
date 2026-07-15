//go:build darwin

package sizer

import (
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

const testMB = 1 << 20

// fsTypeName returns the statfs f_fstypename for the volume behind path.
func fsTypeName(t *testing.T, path string) string {
	t.Helper()
	var st unix.Statfs_t
	if err := unix.Statfs(path, &st); err != nil {
		t.Fatalf("statfs %s: %v", path, err)
	}
	return unix.ByteSliceToString(st.Fstypename[:])
}

// requireAPFS skips the test when the test volume is not APFS — the
// clone/sparse/extent behaviors under test are APFS semantics.
func requireAPFS(t *testing.T, path string) {
	t.Helper()
	if name := fsTypeName(t, path); name != "apfs" {
		t.Skipf("test volume is %q, not apfs; skipping APFS-semantics test", name)
	}
}

// fullSync forces the file's data to disk (F_FULLFSYNC) so extents are
// really allocated before we measure them; falls back to fsync.
func fullSync(t *testing.T, f *os.File) {
	t.Helper()
	if _, err := unix.FcntlInt(f.Fd(), unix.F_FULLFSYNC, 0); err != nil {
		if err := f.Sync(); err != nil {
			t.Fatalf("sync %s: %v", f.Name(), err)
		}
	}
}

// writeFilled creates path with size bytes of a non-zero pattern, synced to
// disk.
func writeFilled(t *testing.T, path string, size int64) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create %s: %v", path, err)
	}
	defer f.Close()
	buf := make([]byte, 64*1024)
	for i := range buf {
		buf[i] = byte(i%251 + 1)
	}
	var written int64
	for written < size {
		n := int64(len(buf))
		if size-written < n {
			n = size - written
		}
		if _, err := f.Write(buf[:n]); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
		written += n
	}
	fullSync(t, f)
}

func TestBlocksSumsLogicalAndAllocated(t *testing.T) {
	dir := t.TempDir()
	writeFilled(t, filepath.Join(dir, "a.bin"), 1*testMB)
	writeFilled(t, filepath.Join(dir, "b.bin"), 2*testMB)

	ts, err := Blocks(dir)
	if err != nil {
		t.Fatalf("Blocks: %v", err)
	}
	if ts.Files != 2 {
		t.Errorf("Files = %d, want 2", ts.Files)
	}
	if want := int64(3 * testMB); ts.Logical != want {
		t.Errorf("Logical = %d, want %d", ts.Logical, want)
	}
	if min := int64(3 * testMB * 95 / 100); ts.Blocks < min {
		t.Errorf("Blocks = %d, want >= %d (allocated should cover the data)", ts.Blocks, min)
	}
}

func TestBlocksDedupsHardlinks(t *testing.T) {
	dir := t.TempDir()
	orig := filepath.Join(dir, "orig.bin")
	writeFilled(t, orig, 1*testMB)
	if err := os.Link(orig, filepath.Join(dir, "hardlink.bin")); err != nil {
		t.Fatalf("link: %v", err)
	}

	ts, err := Blocks(dir)
	if err != nil {
		t.Fatalf("Blocks: %v", err)
	}
	if ts.Files != 1 {
		t.Errorf("Files = %d, want 1 (hardlinks dedup by (dev,inode))", ts.Files)
	}
	if want := int64(1 * testMB); ts.Logical != want {
		t.Errorf("Logical = %d, want %d (hardlink counted once)", ts.Logical, want)
	}
}

func TestBlocksSkipsSymlinks(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target.bin")
	writeFilled(t, target, 1*testMB)
	if err := os.Symlink(target, filepath.Join(dir, "link.bin")); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	ts, err := Blocks(dir)
	if err != nil {
		t.Fatalf("Blocks: %v", err)
	}
	if ts.Files != 1 {
		t.Errorf("Files = %d, want 1 (symlink must not be followed or counted)", ts.Files)
	}
	if want := int64(1 * testMB); ts.Logical != want {
		t.Errorf("Logical = %d, want %d", ts.Logical, want)
	}
}

func TestBlocksMissingRootErrors(t *testing.T) {
	if _, err := Blocks(filepath.Join(t.TempDir(), "does-not-exist")); err == nil {
		t.Error("Blocks on a missing root should return an error")
	}
}

func TestBlocksSingleFileRoot(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "solo.bin")
	writeFilled(t, p, 1*testMB)

	ts, err := Blocks(p)
	if err != nil {
		t.Fatalf("Blocks: %v", err)
	}
	if ts.Files != 1 || ts.Logical != 1*testMB {
		t.Errorf("Files=%d Logical=%d, want 1 file of %d bytes", ts.Files, ts.Logical, 1*testMB)
	}
}
