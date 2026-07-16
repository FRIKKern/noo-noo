package scan

import (
	"context"
	"time"

	"github.com/FRIKKern/noo-noo/internal/core"
	"github.com/FRIKKern/noo-noo/internal/store"
)

// diskSampleThrottle is the minimum spacing between disk_space_history rows for
// one volume. ScanRoots runs on both the daily tick AND pressure-triggered
// scans (which fire ~every 5 min — the designed cooldown of a machine living at
// the memory threshold, charter D18). Sampling every scan would write ~300
// rows/day/volume of near-duplicate data; one row/hour is dense enough for the
// day-bucketed days-until-full forecast.
const diskSampleThrottle = time.Hour

// listVolumesFn is injectable so tests can drive scanDisks without diskutil.
var listVolumesFn = core.ListVolumes

func init() {
	// Override the no-op default installed by scan.go with the real disk-space
	// collector.
	scanDisksFn = scanDisks
}

// scanDisks records a capacity sample for the boot volume and each mounted
// /Volumes/* volume, throttled to one row per volume per hour. Volume
// enumeration failure is tolerated (posture is best-effort — a broken diskutil
// must not abort the repo/cache scan that ran before it); only store I/O and
// ctx cancellation bubble up.
func scanDisks(ctx context.Context, st *store.Store) error {
	vols, err := listVolumesFn(ctx, nil) // nil runner → core.ExecOutputRunner
	if err != nil {
		return nil
	}
	now := time.Now()
	for _, v := range vols {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		if v.UUID == "" {
			continue // an unidentifiable volume can't be keyed in the series
		}
		// Throttle: a sample already exists for this volume within the window.
		recent, err := st.DiskSpaceSeries(v.UUID, now.Add(-diskSampleThrottle))
		if err != nil {
			return err
		}
		if len(recent) > 0 {
			continue
		}
		if err := st.RecordDiskSpace(v.UUID, v.MountPoint, v.TotalBytes, v.FreeBytes, now); err != nil {
			return err
		}
	}
	return nil
}
