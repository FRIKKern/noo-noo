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
	// DiskMeasured marks FreeDiskGB as a real statfs measurement. A
	// sampler that only measures memory leaves it false — its zero-value
	// FreeDiskGB must never read as "0 GB free". The combined sampler once
	// min()'d that zero in, so the merged reading was PERMANENTLY disk-low
	// and, under the disk-only trigger, fired a scan every cooldown window
	// on a machine with 45 GB free.
	DiskMeasured bool
}
