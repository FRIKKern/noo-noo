package pressure

import "syscall"

// DiskSampler reports free space on a single mount point.
type DiskSampler struct{ path string }

// NewDiskSampler creates a sampler for the given mount path.
// Pass "/" for the boot volume, which is what we typically care about.
func NewDiskSampler(path string) *DiskSampler { return &DiskSampler{path: path} }

// Sample returns the available free space on the configured mount in GB.
func (d *DiskSampler) Sample() (Reading, error) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(d.path, &stat); err != nil {
		return Reading{}, err
	}
	// Bavail is blocks available to non-root; Bsize is bytes per block.
	free := float64(stat.Bavail) * float64(stat.Bsize) / 1e9
	return Reading{FreeDiskGB: free}, nil
}
