//go:build darwin

package main

import (
	"os"
	"syscall"
	"time"
)

// pathBirthTime returns the APFS creation time of path (st_birthtimespec).
// ok=false when the path is gone or carries no stat_t. Creation time — not
// mtime — is what "a new leak instance appeared" means: agent scratch dirs
// are rewritten for as long as a session lives, and mtime would make every
// busy session look newborn on each daemon restart.
func pathBirthTime(path string) (time.Time, bool) {
	fi, err := os.Lstat(path)
	if err != nil {
		return time.Time{}, false
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return time.Time{}, false
	}
	return time.Unix(st.Birthtimespec.Sec, st.Birthtimespec.Nsec), true
}
