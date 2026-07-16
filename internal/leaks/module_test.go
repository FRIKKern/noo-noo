package leaks

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/FRIKKern/noo-noo/internal/core"
	"github.com/FRIKKern/noo-noo/internal/modules"
)

// fakeProbe returns a Prober with a fixed answer, for tests that isolate
// registry/age logic from the real lsof binary.
func fakeProbe(live bool, proof string) Prober {
	return func(context.Context, string) (bool, string) { return live, proof }
}

// resolvedTempDir returns a symlink-resolved t.TempDir() — on darwin,
// /var/folders/... resolves to /private/var/folders/..., and both the
// scanner and the safety predicate work on resolved paths.
func resolvedTempDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

func requireLsof(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("lsof"); err != nil {
		t.Skip("lsof not available:", err)
	}
}

func writeFile(t *testing.T, path string, size int) {
	t.Helper()
	if err := os.WriteFile(path, make([]byte, size), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestScanPicksUpThirdRegistryEntry proves signatures are data: a brand-new
// third entry is found by the unmodified scanner.
func TestScanPicksUpThirdRegistryEntry(t *testing.T) {
	base := resolvedTempDir(t)
	hit := filepath.Join(base, "someapp-leak.000001")
	if err := os.MkdirAll(hit, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(hit, "junk"), 4096)

	// Two pre-existing entries (same shape as the shipped registry, but
	// pointed at this test's sandbox so the test never truth-sizes real
	// host trees) plus a brand-new third one.
	registry := []Signature{
		{ID: "first", Globs: []string{filepath.Join(base, "first-*")}, Staleness: StaleWhenLsofEmpty},
		{ID: "second", Globs: []string{filepath.Join(base, "second-*")}, Staleness: StaleWhenAgedAndLsofEmpty, MinAge: 7 * 24 * time.Hour},
		{
			ID:         "someapp-leak",
			Title:      "hypothetical future leak class",
			Globs:      []string{filepath.Join(base, "someapp-leak.??????")},
			Staleness:  StaleWhenLsofEmpty,
			Risk:       modules.RiskLow,
			Workaround: "upgrade someapp",
		},
	}
	m := New(registry, core.NewSafety(nil, nil))
	m.probe = fakeProbe(false, "fake: nothing open")

	rep, err := m.Scan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, it := range rep.Items {
		if it.Path == hit && it.Evidence["signature"] == "someapp-leak" {
			found = true
		}
	}
	if !found {
		t.Fatalf("third registry entry not scanned; items: %+v", rep.Items)
	}
}

// TestLsofStaleness drives the REAL lsof probe: a dir with a held-open file
// classifies LIVE (and Plan emits no delete for it); an unheld dir is stale
// (and Plan emits exactly one delete).
func TestLsofStaleness(t *testing.T) {
	requireLsof(t)
	base := resolvedTempDir(t)

	liveDir := filepath.Join(base, "leak.live01")
	staleDir := filepath.Join(base, "leak.stale1")
	for _, d := range []string{liveDir, staleDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		writeFile(t, filepath.Join(d, "payload"), 4096)
	}
	held, err := os.Open(filepath.Join(liveDir, "payload"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = held.Close() }()

	sig := Signature{
		ID:        "test-leak",
		Globs:     []string{filepath.Join(base, "leak.??????")},
		Staleness: StaleWhenLsofEmpty,
		Risk:      modules.RiskLow,
	}
	m := New([]Signature{sig}, core.NewSafety(nil, nil))

	rep, err := m.Scan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, it := range rep.Items {
		got[it.Path] = it.Evidence["staleness"]
	}
	if got[liveDir] != "live" {
		t.Errorf("dir with held-open file classified %q, want live", got[liveDir])
	}
	if got[staleDir] != "stale" {
		t.Errorf("unheld dir classified %q, want stale", got[staleDir])
	}

	actions := m.Plan(rep)
	if len(actions) != 1 {
		t.Fatalf("Plan emitted %d actions, want exactly 1 (the stale dir): %+v", len(actions), actions)
	}
	if actions[0].Target != staleDir || actions[0].Op != "delete" {
		t.Errorf("planned action %+v, want delete of %s", actions[0], staleDir)
	}
}

// TestApplyTOCTOUGuard proves Apply re-checks staleness at apply time: a hit
// that was stale at Plan but has a file held open by Apply time is refused,
// and succeeds once the file is released.
func TestApplyTOCTOUGuard(t *testing.T) {
	requireLsof(t)
	base := resolvedTempDir(t)
	dir := filepath.Join(base, "leak.toctou")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dir, "payload"), 1<<20)

	sig := Signature{
		ID:        "test-leak",
		Globs:     []string{filepath.Join(base, "leak.??????")},
		Staleness: StaleWhenLsofEmpty,
		Risk:      modules.RiskLow,
	}
	m := New([]Signature{sig}, core.NewSafety(nil, nil))

	rep, err := m.Scan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	actions := m.Plan(rep)
	if len(actions) != 1 {
		t.Fatalf("want 1 planned delete, got %d", len(actions))
	}

	// Between Plan and Apply, the hit goes live.
	held, err := os.Open(filepath.Join(dir, "payload"))
	if err != nil {
		t.Fatal(err)
	}
	res, applyErr := m.Apply(context.Background(), actions[0])
	if applyErr == nil {
		t.Fatal("Apply deleted a hit that went LIVE between plan and apply")
	}
	if !strings.Contains(applyErr.Error(), "LIVE") {
		t.Errorf("refusal should cite liveness, got: %v", applyErr)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("refused target must survive, stat: %v", err)
	}

	// Released again: Apply must now succeed and measure real freed bytes.
	if err := held.Close(); err != nil {
		t.Fatal(err)
	}
	res, applyErr = m.Apply(context.Background(), actions[0])
	if applyErr != nil {
		t.Fatalf("Apply after release: %v", applyErr)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("target should be gone, stat err: %v", err)
	}
	if res.BytesFreed < 0 {
		t.Errorf("BytesFreed = %d, want >= 0 (statfs ground truth)", res.BytesFreed)
	}
}

// TestAgeGateProtectsFreshScratch proves the aged class: even when nothing
// is held open (lsof-empty), a tree whose newest mtime is younger than
// MinAge stays LIVE; only a genuinely old tree is stale.
func TestAgeGateProtectsFreshScratch(t *testing.T) {
	base := resolvedTempDir(t)
	fresh := filepath.Join(base, "claude-fresh")
	aged := filepath.Join(base, "claude-aged1")
	for _, d := range []string{fresh, aged} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		writeFile(t, filepath.Join(d, "scratch"), 4096)
	}
	old := time.Now().Add(-8 * 24 * time.Hour)
	for _, p := range []string{filepath.Join(aged, "scratch"), aged} {
		if err := os.Chtimes(p, old, old); err != nil {
			t.Fatal(err)
		}
	}

	sig := Signature{
		ID:        "test-scratch",
		Globs:     []string{filepath.Join(base, "claude-*")},
		Staleness: StaleWhenAgedAndLsofEmpty,
		MinAge:    7 * 24 * time.Hour,
		Risk:      modules.RiskLow,
	}
	m := New([]Signature{sig}, core.NewSafety(nil, nil))
	m.probe = fakeProbe(false, "fake: nothing open") // isolate the age gate

	rep, err := m.Scan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]modules.Item{}
	for _, it := range rep.Items {
		got[it.Path] = it
	}
	if s := got[fresh].Evidence["staleness"]; s != "live" {
		t.Errorf("fresh scratch classified %q, want live (age-protected)", s)
	}
	if note := got[fresh].Evidence["age"]; !strings.Contains(note, "protecting") {
		t.Errorf("fresh scratch should carry the age-protection note, got %q", note)
	}
	if s := got[aged].Evidence["staleness"]; s != "stale" {
		t.Errorf("aged scratch classified %q, want stale", s)
	}

	// And the conjunction: aged but lsof-LIVE stays live.
	m.probe = fakeProbe(true, "fake: 1 open file")
	rep, err = m.Scan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range rep.Items {
		if it.Path == aged && it.Evidence["staleness"] != "live" {
			t.Errorf("aged+held scratch classified %q, want live (lsof wins)", it.Evidence["staleness"])
		}
	}
}

// TestEvidenceCitesTruth proves every hit carries the double sizing (unique
// allocated AND blocks), the lsof proof, and the signature's workaround, so
// a suggestion can never quietly ride du-fiction.
func TestEvidenceCitesTruth(t *testing.T) {
	base := resolvedTempDir(t)
	hit := filepath.Join(base, "leak.eviden")
	if err := os.MkdirAll(hit, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(hit, "payload"), 128*1024)

	sig := Signature{
		ID:         "test-leak",
		Globs:      []string{filepath.Join(base, "leak.??????")},
		Staleness:  StaleWhenLsofEmpty,
		Risk:       modules.RiskLow,
		Workaround: "launch with --no-leak",
	}
	m := New([]Signature{sig}, core.NewSafety(nil, nil))
	m.probe = fakeProbe(false, "probe-proof: nothing open")

	rep, err := m.Scan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Items) != 1 {
		t.Fatalf("want 1 item, got %d", len(rep.Items))
	}
	ev := rep.Items[0].Evidence
	uniq, err := strconv.ParseInt(ev["unique_allocated_bytes"], 10, 64)
	if err != nil {
		t.Fatalf("unique_allocated_bytes missing/invalid: %q (%v)", ev["unique_allocated_bytes"], err)
	}
	blocks, err := strconv.ParseInt(ev["blocks_bytes"], 10, 64)
	if err != nil {
		t.Fatalf("blocks_bytes missing/invalid: %q (%v)", ev["blocks_bytes"], err)
	}
	if uniq <= 0 || blocks <= 0 {
		t.Errorf("sizes must be positive for a non-empty dir: unique=%d blocks=%d", uniq, blocks)
	}
	if ev["lsof"] != "probe-proof: nothing open" {
		t.Errorf("lsof proof not carried: %q", ev["lsof"])
	}
	if ev["workaround"] != "launch with --no-leak" {
		t.Errorf("workaround not carried: %q", ev["workaround"])
	}
	if rep.Items[0].Size != core.Bytes(uniq) {
		t.Errorf("Item.Size must be the HONEST unique-allocated number, got %d want %d", rep.Items[0].Size, uniq)
	}
}

// requireGlobMatch asserts filepath.Match(glob, path) == want. Split out so
// TestDefaultSignaturesShape stays flat (gocyclo).
func requireGlobMatch(t *testing.T, glob, path string, want bool) {
	t.Helper()
	if ok, _ := filepath.Match(glob, path); ok != want {
		t.Errorf("glob %q match %q = %v, want %v", glob, path, ok, want)
	}
}

// TestDefaultSignaturesShape pins the shipped registry data: the founding
// Chrome class (lsof-only staleness + the documented workaround flag) and
// the /private/tmp agent-scratch class (7d age gate).
func TestDefaultSignaturesShape(t *testing.T) {
	sigs := DefaultSignatures()
	byID := map[string]Signature{}
	for _, s := range sigs {
		byID[s.ID] = s
	}

	chrome, ok := byID["chrome-code-sign-clone"]
	if !ok {
		t.Fatal("chrome-code-sign-clone signature missing")
	}
	if chrome.Staleness != StaleWhenLsofEmpty {
		t.Error("chrome staleness must be lsof-empty ONLY (mtime proven unsafe)")
	}
	if !strings.Contains(chrome.Workaround, "--disable-features=MacAppCodeSignClone") {
		t.Errorf("chrome workaround must cite the disable flag, got %q", chrome.Workaround)
	}
	wantGlob := "/private/var/folders/*/*/X/*.code_sign_clone/code_sign_clone.??????"
	if len(chrome.Globs) != 1 || chrome.Globs[0] != wantGlob {
		t.Errorf("chrome glob = %v, want [%s]", chrome.Globs, wantGlob)
	}
	// The founding path must match; the parent dir must not.
	leaf := "/private/var/folders/hb/18wmml0n5495w28s_1z_8flh0000gn/X/com.google.Chrome.code_sign_clone/code_sign_clone.abc123"
	requireGlobMatch(t, chrome.Globs[0], leaf, true)
	requireGlobMatch(t, chrome.Globs[0], filepath.Dir(leaf), false)

	scratch, ok := byID["private-tmp-agent-scratch"]
	if !ok {
		t.Fatal("private-tmp-agent-scratch signature missing")
	}
	if scratch.Staleness != StaleWhenAgedAndLsofEmpty {
		t.Error("scratch staleness must be aged+lsof-empty (age gate protects live sessions)")
	}
	if scratch.MinAge != 7*24*time.Hour {
		t.Errorf("scratch MinAge = %s, want 168h", scratch.MinAge)
	}
	for _, p := range []string{"/private/tmp/claude-501", "/private/tmp/foo-gocache-1", "/private/tmp/x.gocache.9"} {
		var matched bool
		for _, g := range scratch.Globs {
			if ok, _ := filepath.Match(g, p); ok {
				matched = true
			}
		}
		if !matched {
			t.Errorf("scratch globs %v must match %s", scratch.Globs, p)
		}
	}
}

// TestApplyRefusals pins the refusal space: unsupported ops and targets that
// match no registered signature are never deleted.
func TestApplyRefusals(t *testing.T) {
	base := resolvedTempDir(t)
	outside := filepath.Join(base, "not-a-leak")
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	m := New(DefaultSignatures(), core.NewSafety(nil, nil))
	m.probe = fakeProbe(false, "fake: nothing open")

	if _, err := m.Apply(context.Background(), modules.Action{Module: "leaks", Op: "clear", Target: outside}); err == nil {
		t.Error("unsupported op accepted")
	}
	if _, err := m.Apply(context.Background(), modules.Action{Module: "leaks", Op: "delete", Target: outside}); err == nil {
		t.Error("target matching no signature accepted")
	}
	if _, err := os.Stat(outside); err != nil {
		t.Fatalf("refused target must survive: %v", err)
	}
}
