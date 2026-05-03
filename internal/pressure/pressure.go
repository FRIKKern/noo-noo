// Package pressure samples macOS memory and free-disk pressure
// and triggers callbacks when sustained-high pressure is detected.
//
// Used by the daemon scheduler to fire out-of-band scans when the user's
// machine starts to fill up — rather than waiting for the daily tick.
package pressure

// Reading is one snapshot of system pressure.
type Reading struct {
	MemRatio   float64 // 0.0 .. 1.0 (1.0 = all memory in use)
	FreeDiskGB float64 // free space on the boot volume
}
