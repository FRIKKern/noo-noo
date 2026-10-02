package orphanapps

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/FRIKKern/noo-noo/internal/modules"
)

func TestIndexLookup(t *testing.T) {
	idx := NewIndex([]App{
		{Path: "/Applications/Docker.app", BundleID: "com.docker.docker"},
		{Path: "/Applications/Visual Studio Code.app", BundleID: "com.microsoft.VSCode"},
		{Path: "/Applications/Google Chrome.app", BundleID: "com.google.Chrome"},
		{Path: "/Applications/Local.app"},
		{Path: "/Applications/zoom.us.app", BundleID: "us.zoom.xos"},
	})
	for _, want := range []string{"com.docker.docker", "Docker Desktop", "Code", "Google", "Local", "Local Sites", "zoom.us", "COM.GOOGLE.CHROME", "com.docker.helper"} {
		if _, ok := idx.Lookup(want); !ok {
			t.Errorf("Lookup(%q) should resolve", want)
		}
	}
	// "Microsoft" DOES resolve (vendor component of com.microsoft.VSCode):
	// a shared vendor dir stays claimed while any vendor app is installed.
	if _, ok := idx.Lookup("Microsoft"); !ok {
		t.Error("vendor component of an installed app's bundle id should claim the vendor dir")
	}
	for _, want := range []string{"DevKinsta", "com.getlocal.local", "Electron", "", "com"} {
		if p, ok := idx.Lookup(want); ok {
			t.Errorf("Lookup(%q) resolved to %s, want none", want, p)
		}
	}
}

func TestParseMdfind(t *testing.T) {
	out := "/Applications/Safari.app   kMDItemCFBundleIdentifier = com.apple.Safari\n" +
		"/Applications/Odd.app   kMDItemCFBundleIdentifier = (null)\n" +
		"garbage line\n"
	apps := parseMdfind(out)
	if len(apps) != 2 || apps[0].BundleID != "com.apple.Safari" || apps[1].BundleID != "" || apps[1].Path != "/Applications/Odd.app" {
		t.Errorf("parseMdfind = %+v", apps)
	}
}

// fixture: a fake home with Application Support / Containers / Caches
// children and a curated DevKinsta folder; sizes above/below the floor;
// mtimes idle or fresh. The resolver knows Docker only.
func fixture(t *testing.T) (string, *Module) {
	t.Helper()
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	stale := time.Now().Add(-45 * 24 * time.Hour)
	mk := func(rel string, size int, when time.Time) {
		d := filepath.Join(home, rel)
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(d, "data"), make([]byte, size), 0o644); err != nil {
			t.Fatal(err)
		}
		// Stamp the file and every dir of rel (parents included): a dir's
		// own mtime counts, exactly as it does on a real abandoned tree.
		stamp := []string{filepath.Join(d, "data")}
		for p := d; p != home; p = filepath.Dir(p) {
			stamp = append(stamp, p)
		}
		for _, p := range stamp {
			if err := os.Chtimes(p, when, when); err != nil {
				t.Fatal(err)
			}
		}
	}
	mk("Library/Application Support/Local", 1<<20, stale)           // orphaned, idle, big
	mk("Library/Containers/com.docker.docker", 1<<20, stale)        // Docker installed -> claimed
	mk("Library/Containers/com.gone.app", 1<<20, stale)             // orphaned bundle id
	mk("Library/Containers/com.apple.Safari", 1<<20, stale)         // Apple: never
	mk("Library/Application Support/MobileSync", 1<<20, stale)      // system store: never
	mk("Library/Caches/Tiny", 10, stale)                            // under the floor
	mk("Library/Application Support/FreshThing", 1<<20, time.Now()) // not idle
	mk("DevKinsta/public/site", 1<<20, stale)                       // curated, app gone
	mk("Library/Application Support/.hidden", 1<<20, stale)         // hidden: never

	idx := NewIndex([]App{{Path: "/Applications/Docker.app", BundleID: "com.docker.docker"}})
	m := New(DefaultLibraryRoots(home), DefaultCurated(home), func(context.Context) *Index { return idx })
	m.minSize = 8 << 10 // 8 KiB floor for the fixture (one allocated block is 4 KiB)
	return home, m
}

func TestScanFlagsOrphansOnly(t *testing.T) {
	home, m := fixture(t)
	rep, err := m.Scan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]modules.Item{}
	for _, it := range rep.Items {
		got[it.Path] = it
	}
	want := []string{
		filepath.Join(home, "Library", "Application Support", "Local"),
		filepath.Join(home, "Library", "Containers", "com.gone.app"),
		filepath.Join(home, "DevKinsta"),
	}
	if len(got) != len(want) {
		t.Errorf("got %d items, want %d: %v", len(got), len(want), rep.Items)
	}
	for _, p := range want {
		it, ok := got[p]
		if !ok {
			t.Errorf("missing %s", p)
			continue
		}
		ev := it.Evidence
		if ev["reason"] == "" || ev["newest_mtime"] == "" || ev["unique_allocated_bytes"] == "" || ev["suggestion"] == "" {
			t.Errorf("%s evidence incomplete: %+v", p, ev)
		}
		if it.Size < 1<<20 {
			t.Errorf("%s size %d, want truth-sized >= 1 MiB", p, it.Size)
		}
	}
	if ev := got[filepath.Join(home, "DevKinsta")].Evidence; ev["keyed_by"] != "curated" || ev["expected_app"] != "DevKinsta" {
		t.Errorf("curated evidence = %+v", ev)
	}
	if ev := got[filepath.Join(home, "Library", "Containers", "com.gone.app")].Evidence; ev["keyed_by"] != "bundle-id" {
		t.Errorf("bundle-id keyed evidence = %+v", ev)
	}
	for _, a := range m.Plan(rep) {
		if a.Op != "delete" || a.Risk != modules.RiskMedium {
			t.Errorf("plan action %+v, want delete at medium risk", a)
		}
	}
}

func TestApplyRefusesReinstalledAndOffShape(t *testing.T) {
	home, m := fixture(t)
	rep, err := m.Scan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	actions := m.Plan(rep)
	if len(actions) != 3 {
		t.Fatalf("want 3 actions, got %d", len(actions))
	}
	// App reinstalled between plan and apply: refused.
	local := filepath.Join(home, "Library", "Application Support", "Local")
	m.index = func(context.Context) *Index {
		return NewIndex([]App{{Path: "/Applications/Local.app", BundleID: "com.getflywheel.local"}})
	}
	if _, err := m.Apply(context.Background(), modules.Action{Module: "apps", Op: "delete", Target: local}); err == nil {
		t.Error("reinstalled app's data deleted")
	}
	if _, err := os.Stat(local); err != nil {
		t.Errorf("refused target must survive: %v", err)
	}
	// Off-shape targets: a nested path, an Apple id, a random dir.
	for _, bad := range []string{
		filepath.Join(home, "DevKinsta", "public"),
		filepath.Join(home, "Library", "Containers", "com.apple.Safari"),
		filepath.Join(home, "Library"),
	} {
		if _, err := m.Apply(context.Background(), modules.Action{Module: "apps", Op: "delete", Target: bad}); err == nil {
			t.Errorf("off-shape target %s accepted", bad)
		}
	}
	// Still orphaned: curated DevKinsta goes, measured.
	m.index = func(context.Context) *Index { return NewIndex(nil) }
	dk := filepath.Join(home, "DevKinsta")
	res, err := m.Apply(context.Background(), modules.Action{Module: "apps", Op: "delete", Target: dk})
	if err != nil {
		t.Fatalf("Apply DevKinsta: %v", err)
	}
	if _, err := os.Stat(dk); !os.IsNotExist(err) {
		t.Errorf("DevKinsta should be gone, stat err: %v", err)
	}
	if res.BytesFreed < 0 {
		t.Errorf("BytesFreed = %d, want statfs-measured >= 0", res.BytesFreed)
	}
}
