//go:build !darwin

package leaks

import (
	"fmt"
	"time"
)

// birthTime is unavailable off macOS: Linux offers no portable creation
// time through os.Stat. Launch causality therefore never dismisses an
// alias there — callers fail safe to LIVE, which only means a leaked tree
// waits for its plain lsof-empty window. Every shipped AliasLaunchCausality
// signature is macOS-specific anyway.
func birthTime(path string) (time.Time, error) {
	return time.Time{}, fmt.Errorf("birthtime unavailable on this platform for %s", path)
}
