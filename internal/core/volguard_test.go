package core

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
)

// fakeRunner fakes the diskutil→plutil chain. It returns diskutilErr for the
// diskutil call, otherwise jsonOut for the plutil call.
type fakeRunner struct {
	diskutilErr error
	jsonOut     string
}

func (f fakeRunner) Output(_ context.Context, _ []byte, name string, _ ...string) ([]byte, error) {
	if strings.Contains(name, "diskutil") {
		if f.diskutilErr != nil {
			return nil, f.diskutilErr
		}
		return []byte("<plist/>"), nil // opaque; the fake plutil step ignores it
	}
	return []byte(f.jsonOut), nil
}

const satechiUUID = "0DBD1B63-0377-450B-A340-7E72D0925EBC"

func volJSON(uuid string, writable bool) string {
	return fmt.Sprintf(`{"VolumeUUID":%q,"MountPoint":"/Volumes/X","Writable":%v,"WritableVolume":%v}`,
		uuid, writable, writable)
}

func TestCheckDestUnconfigured(t *testing.T) {
	g := VolGuard{Runner: fakeRunner{}}
	if err := g.CheckDest(context.Background(), "", ""); !errors.Is(err, ErrDestUnconfigured) {
		t.Fatalf("want ErrDestUnconfigured, got %v", err)
	}
	if err := g.CheckDest(context.Background(), t.TempDir(), ""); !errors.Is(err, ErrDestUnconfigured) {
		t.Fatalf("empty UUID: want ErrDestUnconfigured, got %v", err)
	}
}

func TestCheckDestVolumeAbsent(t *testing.T) {
	g := VolGuard{Runner: fakeRunner{jsonOut: volJSON(satechiUUID, true)}}
	err := g.CheckDest(context.Background(), "/Volumes/definitely-not-mounted-noo-noo", satechiUUID)
	if !errors.Is(err, ErrVolumeAbsent) {
		t.Fatalf("want ErrVolumeAbsent, got %v", err)
	}

	// Directory exists but diskutil cannot resolve it to a volume.
	g = VolGuard{Runner: fakeRunner{diskutilErr: errors.New("Could not find disk")}}
	err = g.CheckDest(context.Background(), t.TempDir(), satechiUUID)
	if !errors.Is(err, ErrVolumeAbsent) {
		t.Fatalf("diskutil failure: want ErrVolumeAbsent, got %v", err)
	}
}

func TestCheckDestUUIDMismatch(t *testing.T) {
	// A volume IS mounted and parseable — but it is not the pinned disk.
	// Mount names are not identity.
	g := VolGuard{Runner: fakeRunner{jsonOut: volJSON("AAAAAAAA-0000-0000-0000-000000000000", true)}}
	err := g.CheckDest(context.Background(), t.TempDir(), satechiUUID)
	if !errors.Is(err, ErrUUIDMismatch) {
		t.Fatalf("want ErrUUIDMismatch, got %v", err)
	}
}

func TestCheckDestUUIDCaseInsensitive(t *testing.T) {
	g := VolGuard{Runner: fakeRunner{jsonOut: volJSON(strings.ToLower(satechiUUID), true)}}
	if err := g.CheckDest(context.Background(), t.TempDir(), satechiUUID); err != nil {
		t.Fatalf("case-differing UUID should match, got %v", err)
	}
}

// TestCheckDestKompisShape encodes the verified /Volumes/Kompis
// counterexample: mounted, plist parseable, UUID even matches — but
// Writable=0. Presence is NOT usability.
func TestCheckDestKompisShape(t *testing.T) {
	g := VolGuard{Runner: fakeRunner{jsonOut: volJSON(satechiUUID, false)}}
	err := g.CheckDest(context.Background(), t.TempDir(), satechiUUID)
	if !errors.Is(err, ErrNotWritable) {
		t.Fatalf("want ErrNotWritable, got %v", err)
	}
}

// TestCheckDestProbeFailsReadOnly is the deeper Kompis lesson: even when the
// metadata CLAIMS writable, only a live write proves it. A read-only
// directory makes the probe fail exactly like Kompis' "Read-only file
// system" did.
func TestCheckDestProbeFailsReadOnly(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root bypasses directory permissions")
	}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })

	g := VolGuard{Runner: fakeRunner{jsonOut: volJSON(satechiUUID, true)}} // metadata lies: claims writable
	err := g.CheckDest(context.Background(), dir, satechiUUID)
	if !errors.Is(err, ErrProbeFailed) {
		t.Fatalf("want ErrProbeFailed, got %v", err)
	}
}

func TestCheckDestHappyPathMock(t *testing.T) {
	g := VolGuard{Runner: fakeRunner{jsonOut: volJSON(satechiUUID, true)}}
	dir := t.TempDir()
	if err := g.CheckDest(context.Background(), dir, satechiUUID); err != nil {
		t.Fatalf("want nil, got %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("probe file leaked: %v", entries)
	}
}

// TestCheckDestLiveSATECHI runs the REAL diskutil/plutil chain against
// /Volumes/SATECHI with its pinned UUID. Skips when the drive is not
// mounted (e.g. CI); on the target machine it must pass.
func TestCheckDestLiveSATECHI(t *testing.T) {
	const dest = "/Volumes/SATECHI"
	if info, err := os.Stat(dest); err != nil || !info.IsDir() {
		t.Skipf("%s not mounted; live check skipped", dest)
	}
	g := VolGuard{} // production ExecOutputRunner
	if err := g.CheckDest(context.Background(), dest, satechiUUID); err != nil {
		t.Fatalf("live CheckDest(%s, %s) = %v, want nil", dest, satechiUUID, err)
	}
	t.Logf("live CheckDest passed: %s UUID=%s, write-probe ok", dest, satechiUUID)
}
