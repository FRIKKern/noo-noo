//go:build darwin

package sizer

import "golang.org/x/sys/unix"

// FreedByDelete runs fn (typically a delete action) and returns the REAL
// number of bytes that became available on the volume containing volPath,
// measured as the statfs Bavail*Bsize delta before/after fn. This is the
// ground truth for audit rows: it is immune to clone accounting entirely —
// if deleting a "2 GB" clone frees 40 MB, this reports 40 MB.
//
// fn's error is returned alongside the measured delta (a partially-failed
// delete still frees real bytes worth recording). The delta can be negative
// when unrelated writes on the volume outrun the delete; callers recording
// audit evidence should store it as measured.
func FreedByDelete(volPath string, fn func() error) (int64, error) {
	var before, after unix.Statfs_t
	if err := unix.Statfs(volPath, &before); err != nil {
		return 0, err
	}
	fnErr := fn()
	if err := unix.Statfs(volPath, &after); err != nil {
		return 0, err
	}
	freed := (int64(after.Bavail) - int64(before.Bavail)) * int64(before.Bsize)
	return freed, fnErr
}
