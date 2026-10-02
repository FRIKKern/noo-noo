package leaks

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/FRIKKern/noo-noo/internal/core"
	"github.com/FRIKKern/noo-noo/internal/modules"
)

func TestFamilyKeyOf(t *testing.T) {
	cases := []struct {
		name   string
		prefix string
		sep    string
		ok     bool
	}{
		{"bd-switch-ui-7zOyaH", "bd-switch-ui", "-", true},
		{"bd-two-installs-eBU8d7", "bd-two-installs", "-", true},
		{"barkdown-shots-runtime-1a2b3c", "barkdown-shots-runtime", "-", true},
		{"bd-profile-preparation-0RKsWl", "bd-profile-preparation", "-", true},
		{"tmp.AbC1234", "tmp", ".", true},
		{"go-build123456789", "go-build", "", true},
		{"bd-migration-restore", "", "", false}, // a named dir, not a random suffix
		{"TemporaryItems", "", "", false},
		{"claude-501", "", "", false},
		{"shot.png", "", "", false},
		{"-abc123", "", "", false},
		{"com.apple.launchd.AbC123", "com.apple.launchd", ".", true}, // grouped — excluded by rule, not by parser
	}
	for _, c := range cases {
		key, ok := familyKeyOf(c.name)
		if ok != c.ok || key.prefix != c.prefix || key.sep != c.sep {
			t.Errorf("familyKeyOf(%q) = (%q,%q,%v), want (%q,%q,%v)", c.name, key.prefix, key.sep, ok, c.prefix, c.sep, c.ok)
		}
	}
}

func TestFamilyHandleRoundTrip(t *testing.T) {
	for _, f := range []family{
		{root: "/private/tmp", key: familyKey{"bp-codegen", "-"}},
		{root: "/private/var/folders/a/b/T", key: familyKey{"tmp", "."}},
		{root: "/private/tmp", key: familyKey{"go-build", ""}},
	} {
		root, key, ok := parseFamilyHandle(f.handle())
		if !ok || root != f.root || key != f.key {
			t.Errorf("handle %q round-trips to (%q,%+v,%v), want (%q,%+v)", f.handle(), root, key, ok, f.root, f.key)
		}
	}
	if _, _, ok := parseFamilyHandle("/private/tmp/plain-dir"); ok {
		t.Error("a plain path must not parse as a family handle")
	}
}

// familyFixture builds a $TMPDIR-shaped root with several sibling families
// and returns the root plus a module wired to it with fake probes.
func familyFixture(t *testing.T) (string, *Module, Signature) {
	t.Helper()
	base := resolvedTempDir(t)
	old := time.Now().Add(-3 * 24 * time.Hour)
	mk := func(name string, size int, when time.Time) string {
		d := filepath.Join(base, name)
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		writeFile(t, filepath.Join(d, "payload"), size)
		for _, p := range []string{filepath.Join(d, "payload"), d} {
			if err := os.Chtimes(p, when, when); err != nil {
				t.Fatal(err)
			}
		}
		return d
	}
	for i := 0; i < 7; i++ {
		mk(fmt.Sprintf("bd-switch-ui-%dzOy%02d", i, i), 4096, old) // stale family of 7
	}
	for i := 0; i < 3; i++ {
		mk(fmt.Sprintf("bd-two-installs-eB%02dd7", i), 4096, old) // too small
	}
	for i := 0; i < 6; i++ {
		mk(fmt.Sprintf("fresh-run-%dA1b2c", i), 4096, old)
	}
	mk("fresh-run-9Z9z9z", 4096, time.Now()) // one fresh sibling protects the family
	for i := 0; i < 6; i++ {
		mk(fmt.Sprintf("com.apple.launchd.AbC%03d", i), 16, old) // excluded by name
	}
	mk("bd-migration-restore", 4096, old) // named dir, no family
	mk("TemporaryItems", 4096, old)

	sig := Signature{
		ID:        "test-family",
		Globs:     []string{filepath.Join(base, "*")},
		Staleness: StaleWhenFamilyAgedAndLsofEmpty,
		Family:    &FamilyRule{Roots: []string{base}, MinMembers: 5, ExcludePrefixes: []string{"com.apple."}},
		MinAge:    12 * time.Hour,
		Risk:      modules.RiskLow,
	}
	m := New([]Signature{sig}, core.NewSafety(nil, nil))
	m.probe = fakeProbe(false, "fake: nothing open")
	m.openPaths = func(context.Context) ([]string, string, bool) { return nil, "", true }
	return base, m, sig
}

func TestFamilyScanGroupsAndGates(t *testing.T) {
	base, m, _ := familyFixture(t)
	rep, err := m.Scan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]modules.Item{}
	for _, it := range rep.Items {
		got[it.Path] = it
	}
	if len(got) != 2 {
		t.Fatalf("want exactly 2 family items (bd-switch-ui stale, fresh-run live), got %d: %v", len(got), keys(got))
	}
	stale := got[filepath.Join(base, "bd-switch-ui-*")]
	if stale.Evidence["staleness"] != "stale" {
		t.Errorf("bd-switch-ui family = %q, want stale; age=%q lsof=%q", stale.Evidence["staleness"], stale.Evidence["age"], stale.Evidence["lsof"])
	}
	ev := stale.Evidence
	if ev["family_prefix"] != "bd-switch-ui" || ev["family_members"] != "7" || ev["newest_mtime"] == "" ||
		ev["unique_allocated_bytes"] == "" {
		t.Errorf("family evidence incomplete: %+v", ev)
	}
	if stale.Size <= 0 {
		t.Errorf("family item must be truth-sized, got %d", stale.Size)
	}
	live := got[filepath.Join(base, "fresh-run-*")]
	if live.Evidence["staleness"] != "live" || !strings.Contains(live.Evidence["age"], "harness may still be running") {
		t.Errorf("fresh-run family = %q (%q), want live via newest-mtime gate", live.Evidence["staleness"], live.Evidence["age"])
	}
	actions := m.Plan(rep)
	if len(actions) != 1 || actions[0].Target != stale.Path {
		t.Fatalf("Plan = %+v, want one delete of the stale family handle", actions)
	}
}

func TestFamilyOpenMemberProtectsWholeFamily(t *testing.T) {
	base, m, _ := familyFixture(t)
	held := filepath.Join(base, "bd-switch-ui-0zOy00", "payload")
	m.openPaths = func(context.Context) ([]string, string, bool) { return []string{held}, "", true }
	rep, err := m.Scan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range rep.Items {
		if it.Path == filepath.Join(base, "bd-switch-ui-*") {
			if it.Evidence["staleness"] != "live" || !strings.Contains(it.Evidence["lsof"], held) {
				t.Errorf("family with an open member = %q (%q), want live naming the open path", it.Evidence["staleness"], it.Evidence["lsof"])
			}
		}
	}
	// A listing that cannot be trusted protects everything (fail-safe).
	m.openPaths = func(context.Context) ([]string, string, bool) { return nil, "fake: lsof broke", false }
	rep, _ = m.Scan(context.Background())
	for _, it := range rep.Items {
		if it.Evidence["staleness"] != "live" {
			t.Errorf("%s stale despite untrusted listing", it.Path)
		}
	}
}

func TestFamilyApplyDeletesStaleMembersOnly(t *testing.T) {
	base, m, _ := familyFixture(t)
	rep, err := m.Scan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	actions := m.Plan(rep)
	if len(actions) != 1 {
		t.Fatalf("want 1 action, got %d", len(actions))
	}
	// Between plan and apply: one member gets written to (harness came
	// back), one is held open per the inode-accurate probe.
	fresh := filepath.Join(base, "bd-switch-ui-1zOy01")
	writeFile(t, filepath.Join(fresh, "new"), 10)
	held := filepath.Join(base, "bd-switch-ui-2zOy02")
	m.probe = func(_ context.Context, dir string) (bool, string) {
		if dir == held {
			return true, "fake: 1 open file under " + dir
		}
		return false, "fake: nothing open"
	}
	res, err := m.Apply(context.Background(), actions[0])
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if res.BytesFreed < 0 {
		t.Errorf("BytesFreed = %d, want statfs-measured >= 0", res.BytesFreed)
	}
	for _, keep := range []string{fresh, held} {
		if _, err := os.Stat(keep); err != nil {
			t.Errorf("%s must survive (fresh/held), stat: %v", keep, err)
		}
	}
	for i := 3; i < 7; i++ {
		p := filepath.Join(base, fmt.Sprintf("bd-switch-ui-%dzOy%02d", i, i))
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("stale member %s should be gone, stat err: %v", p, err)
		}
	}
	// Non-members are untouched.
	if _, err := os.Stat(filepath.Join(base, "bd-migration-restore")); err != nil {
		t.Errorf("named dir deleted: %v", err)
	}
	// Family shrank to 2 members: a second apply refuses (no longer a family).
	if _, err := m.Apply(context.Background(), actions[0]); err == nil || !strings.Contains(err.Error(), "no longer a family") {
		t.Errorf("apply on a shrunken family should refuse, got %v", err)
	}
}

func keys(m map[string]modules.Item) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
