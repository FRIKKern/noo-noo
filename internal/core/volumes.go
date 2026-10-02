package core

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"golang.org/x/sys/unix"
)

// VolumeClass is a volume's writability posture — the single honest statement
// `noo-noo status` reports about each disk, and the branch that decides what
// remediation is even POSSIBLE.
type VolumeClass string

const (
	// VolumeUsable: writable media and writable volume. A real offload target.
	VolumeUsable VolumeClass = "usable"
	// VolumeMediaRO: the MEDIA itself refuses writes (WritableMedia=false) —
	// hardware write-lock (SD lock switch, controller RO fault, dying firmware
	// lock). fsck/repairVolume CANNOT succeed and must NEVER be suggested; the
	// only correct playbook is rescue-copy-first, then hardware diagnosis. This
	// is the verified /Volumes/Kompis shape (charter D7, corrected wave 2).
	VolumeMediaRO VolumeClass = "media-ro"
	// VolumeFSCorruptionRO: media is writable but the volume mounted read-only
	// (WritableMedia=true, WritableVolume=false) — filesystem corruption. Here
	// backup-then-repair (with consent) is the correct, possible playbook.
	VolumeFSCorruptionRO VolumeClass = "fs-corruption-ro"
)

// Volume is one mounted volume's identity, capacity, and posture. Capacity is a
// statfs primitive (VolumeCapacity), NOT a sizer tree-walk: a volume's total
// and free bytes are answered by the kernel directly and cheaply.
type Volume struct {
	UUID           string
	MountPoint     string
	Name           string // diskutil VolumeName ("Recovery", "Kompis")
	Container      string // diskutil ParentWholeDisk: the APFS container / whole disk ("disk3")
	PhysicalDisk   string // whole disk behind the first APFS physical store ("disk0"); empty when not APFS
	TotalBytes     int64
	FreeBytes      int64
	WritableMedia  bool
	WritableVolume bool
	Class          VolumeClass
}

// VolumeCapacity returns the total and available bytes of the filesystem
// containing path, via statfs. Bavail*Bsize is the space a non-root user can
// actually use (excludes the reserved pool); Blocks*Bsize is the total. This is
// the ONLY place in noo-noo that reads a volume's total capacity — nothing else
// had it, and days-until-full is meaningless without it.
//
// @canonical capability:volume-capacity aka:statfs,disk-total,disk-free,total-capacity
func VolumeCapacity(path string) (total, free int64, err error) {
	var st unix.Statfs_t
	if err := unix.Statfs(path, &st); err != nil {
		return 0, 0, fmt.Errorf("statfs %q: %w", path, err)
	}
	bsize := int64(st.Bsize)
	total = int64(st.Blocks) * bsize
	free = int64(st.Bavail) * bsize
	return total, free, nil
}

// classifyVolume is the posture discriminator, keyed on WritableMedia
// specifically (charter D7). Kept pure and separate so the three shapes are
// table-testable without diskutil.
func classifyVolume(writableMedia, writableVolume bool) VolumeClass {
	switch {
	case !writableMedia:
		// Hardware write-lock — repair is impossible, rescue-copy only.
		return VolumeMediaRO
	case !writableVolume:
		// Media accepts writes but the volume is RO — filesystem corruption.
		return VolumeFSCorruptionRO
	default:
		return VolumeUsable
	}
}

// diskutilVolumeInfo is the subset of `diskutil info -plist <mount>` (after
// plutil→JSON) that posture needs. Bools are pointers so a MISSING key (older
// diskutil, odd volume) is distinguishable from an explicit false and defaults
// to writable — we never brand a volume media-RO on absence of evidence.
type diskutilVolumeInfo struct {
	VolumeUUID         string `json:"VolumeUUID"`
	MountPoint         string `json:"MountPoint"`
	VolumeName         string `json:"VolumeName"`
	ParentWholeDisk    string `json:"ParentWholeDisk"`
	WritableVolume     *bool  `json:"WritableVolume"`
	WritableMedia      *bool  `json:"WritableMedia"`
	APFSPhysicalStores []struct {
		APFSPhysicalStore string `json:"APFSPhysicalStore"`
	} `json:"APFSPhysicalStores"`
}

// diskutilList is the subset of `diskutil list -plist` we use: the names of the
// non-boot volumes mounted under /Volumes.
type diskutilList struct {
	VolumesFromDisks []string `json:"VolumesFromDisks"`
}

// boolOrTrue dereferences a *bool defaulting missing→true.
func boolOrTrue(b *bool) bool { return b == nil || *b }

// systemVolumeNames are the APFS helper volumes macOS mounts under /Volumes
// on the boot container (and the Recovery HD of older layouts). None of them
// is somewhere a user could put data, whatever statfs says about them.
//
// Deliberately NOT here: "Data" and "Hardware" — plausible names for a
// user's external drive. The boot container's own Data volume is caught by
// the same-container rule instead.
var systemVolumeNames = map[string]bool{
	"Recovery": true, "Recovery HD": true, "Preboot": true, "VM": true,
	"Update": true, "xART": true, "xarts": true, "iSCPreboot": true,
}

// IsSystemVolumeName reports whether name is one of macOS's own helper
// volumes (Recovery, Preboot, VM, Update, ...).
func IsSystemVolumeName(name string) bool { return systemVolumeNames[name] }

// IsExternalCandidate reports whether v is a volume a user could actually
// offload to: not the boot volume under another name, not on the boot
// volume's APFS container or physical disk, and not a macOS helper volume.
// Pure so the inventory filter is table-testable with fake volume lists.
//
// A same-container sibling (/Volumes/Recovery is disk3s3 next to the boot
// disk3s1s1) reports the boot disk's capacity and free space verbatim — it
// is the SAME space, and counting it as external headroom was a lie.
func IsExternalCandidate(boot, v Volume) bool {
	if v.MountPoint == "/" || (v.UUID != "" && v.UUID == boot.UUID) {
		return false
	}
	if v.Container != "" && v.Container == boot.Container {
		return false
	}
	if v.PhysicalDisk != "" && v.PhysicalDisk == boot.PhysicalDisk {
		return false
	}
	return !IsSystemVolumeName(v.Name)
}

// wholeDisk strips the slice suffix from a BSD device name: disk0s2 → disk0.
func wholeDisk(dev string) string {
	const prefix = "disk"
	if !strings.HasPrefix(dev, prefix) {
		return dev
	}
	i := len(prefix)
	for i < len(dev) && dev[i] >= '0' && dev[i] <= '9' {
		i++
	}
	return dev[:i]
}

// ListVolumes returns the boot volume ("/") plus every mounted /Volumes/*
// volume that is genuinely elsewhere (see IsExternalCandidate), each with its
// UUID, capacity (statfs), and posture class. The runner is injected (nil →
// ExecOutputRunner) so tests fake the diskutil→plutil chain exactly like
// VolGuard does. Per-volume failures are tolerated: a volume whose diskutil
// info won't parse or whose statfs fails is still reported (with the data we
// could gather) rather than silently dropped — an unreadable mounted volume
// is itself a posture signal.
func ListVolumes(ctx context.Context, runner OutputRunner) ([]Volume, error) {
	if runner == nil {
		runner = ExecOutputRunner{}
	}

	listJSON, err := diskutilInfoJSON(ctx, runner, "list", "-plist")
	if err != nil {
		return nil, fmt.Errorf("diskutil list: %w", err)
	}
	var dl diskutilList
	if err := json.Unmarshal(listJSON, &dl); err != nil {
		return nil, fmt.Errorf("parse diskutil list: %w", err)
	}

	// Boot volume first, then each named /Volumes/* volume.
	mounts := make([]string, 0, len(dl.VolumesFromDisks)+1)
	mounts = append(mounts, "/")
	for _, name := range dl.VolumesFromDisks {
		mounts = append(mounts, "/Volumes/"+name)
	}

	var out []Volume
	var boot Volume
	for _, mount := range mounts {
		select {
		case <-ctx.Done():
			return out, ctx.Err()
		default:
		}
		v, err := volumeAt(ctx, runner, mount)
		if err != nil {
			// Tolerate: skip a mount whose identity we cannot resolve at all.
			continue
		}
		if mount == "/" {
			boot = v
		} else if !IsExternalCandidate(boot, v) {
			// Same container/disk as the boot volume, or a macOS helper
			// volume: not external headroom, however statfs phrases it.
			continue
		}
		out = append(out, v)
	}
	return out, nil
}

// volumeAt resolves one mount point's Volume record.
func volumeAt(ctx context.Context, runner OutputRunner, mount string) (Volume, error) {
	infoJSON, err := diskutilInfoJSON(ctx, runner, "info", "-plist", mount)
	if err != nil {
		return Volume{}, err
	}
	var vi diskutilVolumeInfo
	if err := json.Unmarshal(infoJSON, &vi); err != nil {
		return Volume{}, err
	}
	wm := boolOrTrue(vi.WritableMedia)
	wv := boolOrTrue(vi.WritableVolume)
	mp := vi.MountPoint
	if mp == "" {
		mp = mount
	}
	v := Volume{
		UUID:           vi.VolumeUUID,
		MountPoint:     mp,
		Name:           vi.VolumeName,
		Container:      vi.ParentWholeDisk,
		WritableMedia:  wm,
		WritableVolume: wv,
		Class:          classifyVolume(wm, wv),
	}
	if len(vi.APFSPhysicalStores) > 0 {
		v.PhysicalDisk = wholeDisk(vi.APFSPhysicalStores[0].APFSPhysicalStore)
	}
	// Capacity is best-effort: a statfs failure (e.g. a mount that vanished
	// between list and info) leaves total/free at 0 but keeps the posture.
	if total, free, err := VolumeCapacity(mp); err == nil {
		v.TotalBytes = total
		v.FreeBytes = free
	}
	return v, nil
}

// diskutilInfoJSON runs a diskutil command with -plist and pipes it through
// plutil to JSON, matching VolGuard's dependency-free plist handling.
func diskutilInfoJSON(ctx context.Context, runner OutputRunner, args ...string) ([]byte, error) {
	plist, err := runner.Output(ctx, nil, "/usr/sbin/diskutil", args...)
	if err != nil {
		return nil, err
	}
	jsonBytes, err := runner.Output(ctx, plist, "/usr/bin/plutil", "-convert", "json", "-o", "-", "-")
	if err != nil {
		return nil, err
	}
	return jsonBytes, nil
}
