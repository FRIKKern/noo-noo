package leaks

import (
	"fmt"
	"os"
	"syscall"
	"time"
)

// birthTime returns the creation time of path. APFS records it natively
// (st_birthtimespec); the launch-causality arbitration needs true creation
// time — mtime moves with later writes and would smear the birth-proximity
// window.
func birthTime(path string) (time.Time, error) {
	fi, err := os.Lstat(path)
	if err != nil {
		return time.Time{}, err
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return time.Time{}, fmt.Errorf("no stat_t for %s", path)
	}
	return time.Unix(st.Birthtimespec.Sec, st.Birthtimespec.Nsec), nil
}
