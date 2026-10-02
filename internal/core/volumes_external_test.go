package core

import (
	"context"
	"testing"
)

// richInfoJSON is infoJSON plus the identity keys the external filter reads.
func richInfoJSON(uuid, mount, name, container, physStore string) string {
	j := `{"VolumeUUID":"` + uuid + `","MountPoint":"` + mount + `","VolumeName":"` + name +
		`","WritableMedia":true,"WritableVolume":true`
	if container != "" {
		j += `,"ParentWholeDisk":"` + container + `"`
	}
	if physStore != "" {
		j += `,"APFSPhysicalStores":[{"APFSPhysicalStore":"` + physStore + `"}]`
	}
	return j + `}`
}

// TestListVolumesExcludesBootSiblingsAndSystemVolumes is the /Volumes/Recovery
// incident: a same-container sibling reported the boot disk's capacity as
// external headroom. Only a volume on another disk survives the filter.
func TestListVolumesExcludesBootSiblingsAndSystemVolumes(t *testing.T) {
	runner := fakeVolRunner{
		list: `{"VolumesFromDisks":["Macintosh HD","Recovery","Scratch","Kompis","Update"]}`,
		infos: map[string]string{
			"/":                     richInfoJSON("BOOT-UUID", "/", "Macintosh HD", "disk3", "disk0s2"),
			"/Volumes/Macintosh HD": richInfoJSON("BOOT-UUID", "/", "Macintosh HD", "disk3", "disk0s2"),            // firmlink to /
			"/Volumes/Recovery":     richInfoJSON("REC-UUID", "/Volumes/Recovery", "Recovery", "disk3", "disk0s2"), // same container
			"/Volumes/Scratch":      richInfoJSON("SCR-UUID", "/Volumes/Scratch", "Scratch", "disk4", "disk0s3"),   // 2nd container, same SSD
			"/Volumes/Kompis":       richInfoJSON("KOMPIS-UUID", "/Volumes/Kompis", "Kompis", "disk5", ""),         // real external (exFAT)
			"/Volumes/Update":       richInfoJSON("UPD-UUID", "/Volumes/Update", "Update", "disk9", "disk8s1"),     // system name, odd disk
		},
	}
	vols, err := ListVolumes(context.Background(), runner)
	if err != nil {
		t.Fatalf("ListVolumes: %v", err)
	}
	var mounts []string
	for _, v := range vols {
		mounts = append(mounts, v.MountPoint)
	}
	if len(vols) != 2 || vols[0].MountPoint != "/" || vols[1].MountPoint != "/Volumes/Kompis" {
		t.Fatalf("want [/ /Volumes/Kompis], got %v", mounts)
	}
	if vols[0].Container != "disk3" || vols[0].PhysicalDisk != "disk0" || vols[0].Name != "Macintosh HD" {
		t.Errorf("boot identity not parsed: %+v", vols[0])
	}
}

func TestIsExternalCandidate(t *testing.T) {
	boot := Volume{UUID: "BOOT", MountPoint: "/", Name: "Macintosh HD", Container: "disk3", PhysicalDisk: "disk0"}
	cases := []struct {
		name string
		v    Volume
		want bool
	}{
		{"real external", Volume{UUID: "X", MountPoint: "/Volumes/X", Name: "X", Container: "disk5", PhysicalDisk: "disk5"}, true},
		{"non-APFS external, no phys store", Volume{UUID: "Y", MountPoint: "/Volumes/Y", Name: "Y", Container: "disk6"}, true},
		{"same container", Volume{UUID: "R", MountPoint: "/Volumes/Recovery", Name: "Recovery", Container: "disk3", PhysicalDisk: "disk0"}, false},
		{"same physical disk, other container", Volume{UUID: "S", MountPoint: "/Volumes/S", Name: "S", Container: "disk4", PhysicalDisk: "disk0"}, false},
		{"boot under another name", Volume{UUID: "BOOT", MountPoint: "/Volumes/Macintosh HD", Name: "Macintosh HD"}, false},
		{"system name on unknown disk", Volume{UUID: "P", MountPoint: "/Volumes/Preboot", Name: "Preboot"}, false},
		{"user drive named Data", Volume{UUID: "D", MountPoint: "/Volumes/Data", Name: "Data", Container: "disk7", PhysicalDisk: "disk7"}, true},
		{"unknown identity, plain name", Volume{UUID: "U", MountPoint: "/Volumes/U", Name: "U"}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := IsExternalCandidate(boot, c.v); got != c.want {
				t.Errorf("IsExternalCandidate = %v, want %v for %+v", got, c.want, c.v)
			}
		})
	}
	if wholeDisk("disk0s2") != "disk0" || wholeDisk("disk12") != "disk12" || wholeDisk("") != "" {
		t.Error("wholeDisk wrong")
	}
}
