package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEndToEndDevList(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "repo", "node_modules"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "repo", "node_modules", "x.js"),
		make([]byte, 1024), 0o644); err != nil {
		t.Fatal(err)
	}

	var out, errOut bytes.Buffer
	app := &App{Out: &out, Err: &errOut}
	code := app.Run(context.Background(),
		[]string{"noo-noo", "dev", "-roots", root, "list"})
	if code != 0 {
		t.Fatalf("exit %d, stderr: %s", code, errOut.String())
	}
	if !strings.Contains(out.String(), "node_modules") {
		t.Errorf("expected node_modules in output, got: %s", out.String())
	}
	if !strings.Contains(out.String(), "1.0 KB") {
		t.Errorf("expected size in output, got: %s", out.String())
	}
}

func TestEndToEndDevCleanRequiresConfirm(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "repo", "node_modules")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "x.js"), make([]byte, 100), 0o644); err != nil {
		t.Fatal(err)
	}

	// Without -y the prompt comes from os.Stdin which we can't drive in a
	// unit test cleanly. Use --dry-run + -y for a non-interactive path.
	var out, errOut bytes.Buffer
	app := &App{Out: &out, Err: &errOut}
	code := app.Run(context.Background(),
		[]string{"noo-noo", "dev", "-roots", root, "-y", "-dry-run", "clean"})
	if code != 0 {
		t.Fatalf("exit %d, stderr: %s", code, errOut.String())
	}
	if !strings.Contains(out.String(), "would delete") {
		t.Errorf("expected 'would delete' in dry-run output, got: %s", out.String())
	}
	// File must still exist.
	if _, err := os.Stat(target); err != nil {
		t.Errorf("dry-run should not have deleted: %v", err)
	}
}

// Natural verb-then-flags order: `dev list -roots <tmp>`. This is RED on main
// today — the pre-fix code ran fs.Parse with the verb still at args[0], so
// flag.Parse stopped at "list" and silently dropped -roots, scanning the
// default ~/Documents/GitHub instead of the fixture. The companion above
// exercises the legacy flags-first order that masked the bug; both must pass.
func TestEndToEndDevListNaturalOrder(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "repo", "node_modules"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "repo", "node_modules", "x.js"),
		make([]byte, 1024), 0o644); err != nil {
		t.Fatal(err)
	}

	var out, errOut bytes.Buffer
	app := &App{Out: &out, Err: &errOut}
	code := app.Run(context.Background(),
		[]string{"noo-noo", "dev", "list", "-roots", root})
	if code != 0 {
		t.Fatalf("exit %d, stderr: %s", code, errOut.String())
	}
	if !strings.Contains(out.String(), "node_modules") {
		t.Errorf("verb-first -roots was dropped: expected node_modules from the fixture, got: %s", out.String())
	}
}

// Natural order with -y after the verb: the founding trust bug. Pre-fix,
// `dev clean -roots <tmp> -y -dry-run` dropped -y and -dry-run, so a
// non-interactive run would have blocked on the confirm prompt. Post-fix the
// dry-run plan prints and nothing is deleted.
func TestEndToEndDevCleanNaturalOrder(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "repo", "node_modules")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "x.js"), make([]byte, 100), 0o644); err != nil {
		t.Fatal(err)
	}

	var out, errOut bytes.Buffer
	app := &App{Out: &out, Err: &errOut}
	code := app.Run(context.Background(),
		[]string{"noo-noo", "dev", "clean", "-roots", root, "-y", "-dry-run"})
	if code != 0 {
		t.Fatalf("exit %d, stderr: %s", code, errOut.String())
	}
	if !strings.Contains(out.String(), "would delete") {
		t.Errorf("expected 'would delete' in dry-run output, got: %s", out.String())
	}
	if _, err := os.Stat(target); err != nil {
		t.Errorf("dry-run should not have deleted: %v", err)
	}
}

// b9 ride-along: Electron ShipIt updater caches are matched by glob (bundle IDs
// are build-generated). A fake HOME with a ShipIt dir must appear among the
// default cache targets; the static literal targets are unaffected.
func TestDefaultCacheTargetsIncludesShipIt(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	shipIt := filepath.Join(home, "Library", "Caches", "com.example.app.ShipIt")
	if err := os.MkdirAll(shipIt, 0o755); err != nil {
		t.Fatal(err)
	}
	// A non-ShipIt cache dir must NOT be swept in by the glob.
	other := filepath.Join(home, "Library", "Caches", "com.example.other")
	if err := os.MkdirAll(other, 0o755); err != nil {
		t.Fatal(err)
	}

	targets := defaultCacheTargets()
	var foundShipIt, foundOther bool
	for _, tg := range targets {
		if tg == shipIt {
			foundShipIt = true
		}
		if tg == other {
			foundOther = true
		}
	}
	if !foundShipIt {
		t.Errorf("expected %s among cache targets, got: %v", shipIt, targets)
	}
	if foundOther {
		t.Errorf("glob should match only *.ShipIt, not %s", other)
	}
	// The static literal targets must still be present.
	if !strings.Contains(strings.Join(targets, "\n"), filepath.Join(home, "Library", "Caches", "go-build")) {
		t.Errorf("static cache targets missing after glob append: %v", targets)
	}
}
