package scan

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/FRIKKern/noo-noo/internal/core"
)

// stubVolumes installs a fake volume lister for the duration of a test.
func stubVolumes(t *testing.T, vols []core.Volume, err error) {
	t.Helper()
	orig := listVolumesFn
	t.Cleanup(func() { listVolumesFn = orig })
	listVolumesFn = func(_ context.Context, _ core.OutputRunner) ([]core.Volume, error) {
		return vols, err
	}
}

// TestScanRootsRecordsDiskSamplesThrottled proves the wave's throttle contract:
// ScanRoots writes one disk_space_history row per volume on the first call, and
// a second call within the 1-hour window writes NOTHING (pressure scans fire
// ~every 5 min — unthrottled this would flood the series).
func TestScanRootsRecordsDiskSamplesThrottled(t *testing.T) {
	st := openTestStore(t)
	stubVolumes(t, []core.Volume{
		{UUID: "BOOT", MountPoint: "/", TotalBytes: 100, FreeBytes: 40, Class: core.VolumeUsable},
		{UUID: "EXT", MountPoint: "/Volumes/X", TotalBytes: 200, FreeBytes: 10, Class: core.VolumeUsable},
	}, nil)

	ctx := context.Background()
	if err := ScanRoots(ctx, Roots{}, st); err != nil {
		t.Fatalf("first ScanRoots: %v", err)
	}
	if err := ScanRoots(ctx, Roots{}, st); err != nil {
		t.Fatalf("second ScanRoots: %v", err)
	}

	epoch := time.Unix(0, 0)
	for _, uuid := range []string{"BOOT", "EXT"} {
		rows, err := st.DiskSpaceSeries(uuid, epoch)
		if err != nil {
			t.Fatalf("DiskSpaceSeries(%s): %v", uuid, err)
		}
		if len(rows) != 1 {
			t.Errorf("volume %s: expected exactly 1 throttled row, got %d", uuid, len(rows))
		}
	}
	// The recorded sample carries the capacity we handed it.
	boot, _ := st.DiskSpaceSeries("BOOT", epoch)
	if len(boot) == 1 && (boot[0].TotalBytes != 100 || boot[0].FreeBytes != 40 || boot[0].MountPoint != "/") {
		t.Errorf("boot sample mis-recorded: %+v", boot[0])
	}
}

// TestScanDisksSkipsEmptyUUID: a volume with no resolvable UUID can't be keyed
// into the series and is skipped rather than written under an empty key.
func TestScanDisksSkipsEmptyUUID(t *testing.T) {
	st := openTestStore(t)
	stubVolumes(t, []core.Volume{
		{UUID: "", MountPoint: "/Volumes/Mystery", TotalBytes: 1, FreeBytes: 1},
	}, nil)
	if err := scanDisks(context.Background(), st); err != nil {
		t.Fatalf("scanDisks: %v", err)
	}
	rows, _ := st.DiskSpaceSeries("", time.Unix(0, 0))
	if len(rows) != 0 {
		t.Errorf("empty-UUID volume should not be recorded, got %d rows", len(rows))
	}
}

// TestScanDisksToleratesEnumerationFailure: a diskutil/enumeration failure is
// best-effort posture — it must NOT abort the surrounding scan.
func TestScanDisksToleratesEnumerationFailure(t *testing.T) {
	st := openTestStore(t)
	stubVolumes(t, nil, errors.New("diskutil exploded"))
	if err := scanDisks(context.Background(), st); err != nil {
		t.Fatalf("enumeration failure should be tolerated, got %v", err)
	}
	if err := ScanRoots(context.Background(), Roots{}, st); err != nil {
		t.Fatalf("ScanRoots should tolerate disk enumeration failure, got %v", err)
	}
}
