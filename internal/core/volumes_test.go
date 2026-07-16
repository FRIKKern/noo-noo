package core

import (
	"context"
	"strings"
	"testing"
)

func TestClassifyVolume(t *testing.T) {
	cases := []struct {
		name           string
		writableMedia  bool
		writableVolume bool
		want           VolumeClass
	}{
		{"usable", true, true, VolumeUsable},
		// Kompis shape: the MEDIA refuses writes → hardware write-lock, NEVER
		// repairable. Keyed on WritableMedia=false regardless of volume flag.
		{"media-ro hardware write-lock", false, false, VolumeMediaRO},
		{"media-ro even if volume flag true", false, true, VolumeMediaRO},
		// Media writable but volume RO → filesystem corruption, backup-then-repair.
		{"fs-corruption-ro", true, false, VolumeFSCorruptionRO},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := classifyVolume(c.writableMedia, c.writableVolume); got != c.want {
				t.Errorf("classifyVolume(media=%v,volume=%v) = %q, want %q",
					c.writableMedia, c.writableVolume, got, c.want)
			}
		})
	}
}

// TestVolumeCapacity exercises the statfs primitive against the live boot
// volume: total and free must be positive and free must not exceed total.
func TestVolumeCapacity(t *testing.T) {
	total, free, err := VolumeCapacity("/")
	if err != nil {
		t.Fatalf("VolumeCapacity(/): %v", err)
	}
	if total <= 0 {
		t.Errorf("total = %d, want > 0", total)
	}
	if free < 0 || free > total {
		t.Errorf("free = %d out of range for total %d", free, total)
	}
}

func TestVolumeCapacityMissingPathErrors(t *testing.T) {
	if _, _, err := VolumeCapacity("/this/path/does/not/exist/noo-noo"); err == nil {
		t.Fatal("expected error for a nonexistent path")
	}
}

// fakeVolRunner dispatches the diskutil→plutil chain per call. diskutil emits
// JSON directly (standing in for the plist); plutil is identity (the real one
// converts plist→JSON, the fake feeds JSON straight through). This is the same
// short-circuit VolGuard's fakeRunner uses, extended to key per mount so a
// multi-volume inventory can be faked.
type fakeVolRunner struct {
	list  string            // JSON for `diskutil list -plist`
	infos map[string]string // mount → JSON for `diskutil info -plist <mount>`
}

func (f fakeVolRunner) Output(_ context.Context, stdin []byte, name string, args ...string) ([]byte, error) {
	if strings.Contains(name, "plutil") {
		return stdin, nil // identity
	}
	// diskutil: args are either ["list","-plist"] or ["info","-plist",<mount>].
	if len(args) >= 1 && args[0] == "list" {
		return []byte(f.list), nil
	}
	mount := args[len(args)-1]
	if j, ok := f.infos[mount]; ok {
		return []byte(j), nil
	}
	return nil, errNoFakeInfo(mount)
}

type errNoFakeInfo string

func (e errNoFakeInfo) Error() string { return "no fake info for " + string(e) }

func infoJSON(uuid string, writableMedia, writableVolume bool, mount string) string {
	return `{"VolumeUUID":"` + uuid + `","MountPoint":"` + mount +
		`","WritableMedia":` + boolJSON(writableMedia) +
		`,"WritableVolume":` + boolJSON(writableVolume) + `}`
}

func boolJSON(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

// TestListVolumesClassifiesEachShape drives the full ListVolumes path with a
// faked diskutil: a boot volume (usable), a Kompis-shaped media-RO external,
// and an fs-corruption-RO external. Proves WritableMedia is parsed and each of
// the three classes is assigned correctly through the real plumbing.
func TestListVolumesClassifiesEachShape(t *testing.T) {
	runner := fakeVolRunner{
		list: `{"VolumesFromDisks":["Kompis","Broken"]}`,
		infos: map[string]string{
			"/":               infoJSON("BOOT-UUID", true, true, "/"),
			"/Volumes/Kompis": infoJSON("KOMPIS-UUID", false, false, "/Volumes/Kompis"),
			"/Volumes/Broken": infoJSON("BROKEN-UUID", true, false, "/Volumes/Broken"),
		},
	}
	vols, err := ListVolumes(context.Background(), runner)
	if err != nil {
		t.Fatalf("ListVolumes: %v", err)
	}
	if len(vols) != 3 {
		t.Fatalf("expected 3 volumes, got %d: %+v", len(vols), vols)
	}

	byUUID := map[string]Volume{}
	for _, v := range vols {
		byUUID[v.UUID] = v
	}

	boot, ok := byUUID["BOOT-UUID"]
	if !ok || boot.Class != VolumeUsable {
		t.Errorf("boot volume: want usable, got %+v", boot)
	}
	if boot.MountPoint != "/" {
		t.Errorf("boot mount = %q, want /", boot.MountPoint)
	}

	kompis, ok := byUUID["KOMPIS-UUID"]
	if !ok || kompis.Class != VolumeMediaRO {
		t.Errorf("Kompis: want media-ro (rescue-copy only, never repair), got %+v", kompis)
	}
	if kompis.WritableMedia {
		t.Errorf("Kompis WritableMedia should be false")
	}

	broken, ok := byUUID["BROKEN-UUID"]
	if !ok || broken.Class != VolumeFSCorruptionRO {
		t.Errorf("Broken: want fs-corruption-ro (backup-then-repair), got %+v", broken)
	}
	if !broken.WritableMedia || broken.WritableVolume {
		t.Errorf("Broken should be WritableMedia=true, WritableVolume=false, got %+v", broken)
	}
}

// TestListVolumesDefaultsMissingFlagsWritable proves a diskutil info payload
// missing the writability keys never brands a volume media-RO on absence of
// evidence.
func TestListVolumesDefaultsMissingFlagsWritable(t *testing.T) {
	runner := fakeVolRunner{
		list: `{"VolumesFromDisks":[]}`,
		infos: map[string]string{
			"/": `{"VolumeUUID":"BOOT-UUID","MountPoint":"/"}`,
		},
	}
	vols, err := ListVolumes(context.Background(), runner)
	if err != nil {
		t.Fatalf("ListVolumes: %v", err)
	}
	if len(vols) != 1 || vols[0].Class != VolumeUsable {
		t.Fatalf("missing flags should default usable, got %+v", vols)
	}
	if !vols[0].WritableMedia || !vols[0].WritableVolume {
		t.Errorf("missing flags should default writable, got %+v", vols[0])
	}
}
