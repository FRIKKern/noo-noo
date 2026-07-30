package leaks

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestParseLsofRows(t *testing.T) {
	rows := []string{
		"Google    22057 pelle  txt    REG   1,15   367696 362428846 /x/a",
		"go-test   31337 pelle    3r   REG   1,15       10       99 /x/b",
		"badline",
	}
	got := parseLsofRows(rows)
	if len(got) != 3 {
		t.Fatalf("row count = %d, want 3", len(got))
	}
	if got[0].pid != "22057" || got[0].fd != "txt" {
		t.Fatalf("row 0 = %+v", got[0])
	}
	if got[1].pid != "31337" || got[1].fd != "3r" {
		t.Fatalf("row 1 = %+v", got[1])
	}
	if got[2].pid != "" { // unparseable → zero pid → LIVE downstream
		t.Fatalf("row 2 = %+v, want zero pid", got[2])
	}
}

func TestParseEtime(t *testing.T) {
	cases := map[string]time.Duration{
		"05:03":       5*time.Minute + 3*time.Second,
		"01:02:03":    time.Hour + 2*time.Minute + 3*time.Second,
		"04-01:02:03": 4*24*time.Hour + time.Hour + 2*time.Minute + 3*time.Second,
	}
	for in, want := range cases {
		got, err := parseEtime(in)
		if err != nil || got != want {
			t.Fatalf("parseEtime(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "42", "x:y", "1:2:3:4"} {
		if _, err := parseEtime(bad); err == nil {
			t.Fatalf("parseEtime(%q) accepted", bad)
		}
	}
}

func TestCausalVerdict(t *testing.T) {
	now := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	birth := now.Add(-72 * time.Hour) // a three-day-old leaked tree
	startFn := func(m map[string]time.Time) func(string) (time.Time, error) {
		return func(pid string) (time.Time, error) {
			t, ok := m[pid]
			if !ok {
				return time.Time{}, fmt.Errorf("no such pid")
			}
			return t, nil
		}
	}
	txt := func(pid string) lsofRow { return lsofRow{pid: pid, fd: "txt"} }

	// The founding shape: a long-lived main (started before the tree) and a
	// young helper (started after) hold the shared inode, but neither was
	// BORN with this tree → stale.
	stale, proof := causalVerdict([]lsofRow{txt("1"), txt("2")},
		startFn(map[string]time.Time{"1": birth.Add(-7 * 24 * time.Hour), "2": birth.Add(48 * time.Hour)}), birth, now)
	if !stale {
		t.Fatalf("dead-creator tree not dismissed: %s", proof)
	}

	// A matched process born WITH the tree (the active clone's instance) →
	// LIVE, even with unrelated-age helpers alongside.
	stale, proof = causalVerdict([]lsofRow{txt("1"), txt("2")},
		startFn(map[string]time.Time{"1": birth.Add(30 * time.Second), "2": birth.Add(48 * time.Hour)}), birth, now)
	if stale {
		t.Fatalf("active tree dismissed: %s", proof)
	}

	// A non-txt handle is a real fd → LIVE regardless of ages.
	stale, proof = causalVerdict([]lsofRow{txt("1"), {pid: "2", fd: "3r"}},
		startFn(map[string]time.Time{"1": birth.Add(-time.Hour), "2": birth.Add(-time.Hour)}), birth, now)
	if stale {
		t.Fatalf("real fd dismissed as alias: %s", proof)
	}

	// A tree younger than the margin is never judged (launch mid-flight).
	stale, proof = causalVerdict([]lsofRow{txt("1")},
		startFn(map[string]time.Time{"1": now.Add(-30 * 24 * time.Hour)}), now.Add(-time.Minute), now)
	if stale {
		t.Fatalf("mid-flight tree dismissed: %s", proof)
	}

	// Unknown start time → LIVE (fail-safe).
	stale, proof = causalVerdict([]lsofRow{txt("404")}, startFn(map[string]time.Time{}), birth, now)
	if stale {
		t.Fatalf("unprovable start dismissed: %s", proof)
	}

	// Unparseable row → LIVE (fail-safe).
	stale, proof = causalVerdict([]lsofRow{{}}, startFn(map[string]time.Time{}), birth, now)
	if stale {
		t.Fatalf("unparseable row dismissed: %s", proof)
	}
}

// TestHardlinkAliasFdStaysLive reproduces the shape of the second founding
// incident at the filesystem level and pins the SAFE side: a tree whose file
// is held via a real fd (this test process) must classify LIVE even when the
// signature opts into launch causality — fd holds are never dismissed.
func TestHardlinkAliasFdStaysLive(t *testing.T) {
	if _, err := exec.LookPath("lsof"); err != nil {
		t.Skip("lsof not available")
	}
	root := t.TempDir()
	leakDir := filepath.Join(root, "leak")
	if err := os.Mkdir(leakDir, 0o755); err != nil {
		t.Fatal(err)
	}
	held := filepath.Join(leakDir, "binary")
	if err := os.WriteFile(held, []byte("payload"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Backdate the tree so ONLY the fd-type gate can keep it live.
	past := time.Now().Add(-48 * time.Hour)
	for _, p := range []string{held, leakDir} {
		if err := os.Chtimes(p, past, past); err != nil {
			t.Fatal(err)
		}
	}
	f, err := os.Open(held)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()

	stale, proof := causalCheck(context.Background(), leakDir)
	if stale {
		t.Fatalf("fd-held tree dismissed as alias: %s", proof)
	}
}

// TestClassifyCausalDowngrade wires a fake probe (LIVE) plus a fake causal
// check through Module.classify and asserts the signature opt-in is honored
// in both directions, with the proof trail landing in evidence.
func TestClassifyCausalDowngrade(t *testing.T) {
	sigCausal := Signature{ID: "s", Staleness: StaleWhenLsofEmpty, Alias: AliasLaunchCausality}
	sigPlain := Signature{ID: "p", Staleness: StaleWhenLsofEmpty}

	m := &Module{
		probe:  func(context.Context, string) (bool, string) { return true, "inode match" },
		causal: func(context.Context, string) (bool, string) { return true, "alias dismissed" },
		now:    time.Now,
	}
	ev := map[string]string{}
	if !m.classify(context.Background(), sigCausal, "/x", ev) {
		t.Fatal("causal downgrade not applied")
	}
	if ev["causality"] != "alias dismissed" {
		t.Fatalf("causality evidence = %q", ev["causality"])
	}
	ev = map[string]string{}
	if m.classify(context.Background(), sigPlain, "/x", ev) {
		t.Fatal("AliasNone signature downgraded — opt-in violated")
	}
	if _, ok := ev["causality"]; ok {
		t.Fatal("causality evidence written for AliasNone signature")
	}

	m.causal = func(context.Context, string) (bool, string) { return false, "still live" }
	ev = map[string]string{}
	if m.classify(context.Background(), sigCausal, "/x", ev) {
		t.Fatal("live verdict overridden")
	}
	if ev["causality"] != "still live" {
		t.Fatalf("causality evidence = %q", ev["causality"])
	}
}

// TestLsofProbeStaleEmpty: a directory nothing holds open is provably stale.
func TestLsofProbeStaleEmpty(t *testing.T) {
	if _, err := exec.LookPath("lsof"); err != nil {
		t.Skip("lsof not available")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "cold"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	live, proof := LsofProbe(context.Background(), dir)
	if live {
		t.Fatalf("empty-liveness dir classified LIVE; proof: %s", proof)
	}
}
