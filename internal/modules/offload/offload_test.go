package offload

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/FRIKKern/noo-noo/internal/modules"
)

// --- fakes -----------------------------------------------------------------

type fakeGuard struct {
	err   error
	calls int
}

func (g *fakeGuard) CheckDest(_ context.Context, _, _ string) error {
	g.calls++
	return g.err
}

type fakeProcs struct {
	running map[string]bool
	calls   []string
}

func (p *fakeProcs) Running(_ context.Context, pattern string) (bool, error) {
	p.calls = append(p.calls, pattern)
	return p.running[pattern], nil
}

// fakePathProbe is an injectable leaks.Prober for path-gate tests: it records
// the dirs it was asked about and returns a fixed liveness verdict.
type fakePathProbe struct {
	live  bool
	proof string
	calls []string
}

func (f *fakePathProbe) probe(_ context.Context, dir string) (bool, string) {
	f.calls = append(f.calls, dir)
	return f.live, f.proof
}

// goCopy is a pure-Go recursive Copier for hermetic tests.
func goCopy(_ context.Context, src, dst string) error {
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		out := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(out, 0o755)
		}
		in, err := os.Open(p)
		if err != nil {
			return err
		}
		defer func() { _ = in.Close() }()
		f, err := os.Create(out)
		if err != nil {
			return err
		}
		if _, err := io.Copy(f, in); err != nil {
			_ = f.Close()
			return err
		}
		return f.Close()
	})
}

// --- fixtures ---------------------------------------------------------------

// fixture builds a fake home with one relocate-class asset and returns
// (module deps ready to override, home, assetPath, destRoot, cfg, playbooks).
func fixture(t *testing.T, stopGate string, neverDelete bool) (home, asset, destRoot string, cfg Config, pbs []Playbook) {
	t.Helper()
	home = t.TempDir()
	asset = filepath.Join(home, "asset")
	if err := os.MkdirAll(filepath.Join(asset, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	for i, f := range []string{"a.txt", "sub/b.txt", "sub/c.bin"} {
		if err := os.WriteFile(filepath.Join(asset, f), []byte(strings.Repeat("x", 100*(i+1))), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	destRoot = filepath.Join(t.TempDir(), "offload")
	if err := os.MkdirAll(destRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg = Config{DestRoot: destRoot, DestVolumeUUID: "0DBD1B63-0377-450B-A340-7E72D0925EBC"}
	pbs = []Playbook{{
		AssetID:     "asset",
		Path:        "~/asset",
		Class:       ClassRelocate,
		StopGate:    stopGate,
		NeverDelete: neverDelete,
	}}
	return home, asset, destRoot, cfg, pbs
}

func mustReadDirNames(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

// --- NeverDelete law ---------------------------------------------------------

// TestNeverDeleteLaw proves the localwp-blueprints playbook can never produce
// a delete action from Plan under any input, and Apply refuses delete
// outright. The guarantee is structural: "relocate" is the only op Plan ever
// writes and the only op Apply ever executes.
func TestNeverDeleteLaw(t *testing.T) {
	home := t.TempDir()
	bp := filepath.Join(home, "Library", "Application Support", "Local", "blueprints")
	if err := os.MkdirAll(bp, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bp, "Polyflor.zip"), []byte("irreplaceable"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := Config{DestRoot: t.TempDir(), DestVolumeUUID: "0DBD1B63-0377-450B-A340-7E72D0925EBC"}
	m := New(cfg, nil, Deps{Home: home, Guard: &fakeGuard{}, Procs: &fakeProcs{}, Copy: goCopy})

	// Adversarial reports: evidence claiming delete, foreign items, huge
	// sizes, the blueprint path itself, empty items.
	reports := []modules.Report{
		{Module: "offload", Items: []modules.Item{{Path: bp, Size: 1 << 40, Evidence: map[string]string{"class": "delete", "op": "delete"}}}},
		{Module: "offload", Items: []modules.Item{{Path: bp}, {Path: "/tmp/foreign"}, {Path: ""}}},
		{Module: "offload"},
	}
	if rep, err := m.Scan(context.Background()); err == nil {
		reports = append(reports, rep) // the real scan too
	}
	for i, rep := range reports {
		for _, a := range m.Plan(rep) {
			if a.Op != "relocate" {
				t.Fatalf("report %d: Plan emitted op %q — only \"relocate\" is legal", i, a.Op)
			}
		}
	}

	// Apply refuses delete no matter the target.
	for _, target := range []string{bp, "/anything/else"} {
		_, err := m.Apply(context.Background(), modules.Action{Module: "offload", Op: "delete", Target: target})
		if !errors.Is(err, ErrDeleteForbidden) {
			t.Fatalf("Apply(delete %q): want ErrDeleteForbidden, got %v", target, err)
		}
	}
}

// --- relocate Apply -----------------------------------------------------------

func TestApplyRelocateHappyPath(t *testing.T) {
	home, asset, destRoot, cfg, pbs := fixture(t, "", false)
	guard := &fakeGuard{}
	m := New(cfg, pbs, Deps{Home: home, Guard: guard, Procs: &fakeProcs{}, Copy: goCopy})

	dest := filepath.Join(destRoot, "asset")
	res, err := m.Apply(context.Background(), modules.Action{
		Module: "offload", Op: "relocate", Target: asset, Destination: dest,
	})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if guard.calls != 1 {
		t.Fatalf("guard consulted %d times at apply, want 1", guard.calls)
	}
	// Target is now a symlink resolving to dest.
	fi, err := os.Lstat(asset)
	if err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("target is not a symlink: %v %v", fi, err)
	}
	resolved, err := filepath.EvalSymlinks(asset)
	if err != nil {
		t.Fatal(err)
	}
	wantDest, _ := filepath.EvalSymlinks(dest)
	if resolved != wantDest {
		t.Fatalf("symlink resolves to %q, want %q", resolved, wantDest)
	}
	// Content is reachable through the old path.
	b, err := os.ReadFile(filepath.Join(asset, "sub", "b.txt"))
	if err != nil || len(b) != 200 {
		t.Fatalf("content through symlink: %d bytes, err %v", len(b), err)
	}
	// The bak is gone.
	if _, err := os.Lstat(asset + ".noo-noo-bak"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("backup still present: %v", err)
	}
	if res.BytesFreed <= 0 {
		t.Fatalf("BytesFreed = %d, want > 0 (local bytes reclaimed)", res.BytesFreed)
	}
}

// TestApplyMidCopyFailureRestoresOriginal: a copier that dies partway leaves
// the original path intact and cleans up the partial copy.
func TestApplyMidCopyFailureRestoresOriginal(t *testing.T) {
	home, asset, destRoot, cfg, pbs := fixture(t, "", false)
	boom := errors.New("disk yanked mid-copy")
	partial := func(ctx context.Context, src, dst string) error {
		// Copy one file, then fail.
		if err := os.MkdirAll(dst, 0o755); err != nil {
			return err
		}
		if err := goCopy(ctx, filepath.Join(src, "sub"), filepath.Join(dst, "sub")); err != nil {
			return err
		}
		return boom
	}
	m := New(cfg, pbs, Deps{Home: home, Guard: &fakeGuard{}, Procs: &fakeProcs{}, Copy: partial})

	dest := filepath.Join(destRoot, "asset")
	_, err := m.Apply(context.Background(), modules.Action{Module: "offload", Op: "relocate", Target: asset, Destination: dest})
	if !errors.Is(err, boom) {
		t.Fatalf("want mid-copy error surfaced, got %v", err)
	}
	// Original path intact: still a real directory with all three files.
	fi, err := os.Lstat(asset)
	if err != nil || !fi.IsDir() {
		t.Fatalf("original is not a directory anymore: %v %v", fi, err)
	}
	n, bytes, err := countAndBytes(asset)
	if err != nil || n != 3 || bytes != 100+200+300 {
		t.Fatalf("original content damaged: %d files %d bytes err %v", n, bytes, err)
	}
	// No bak, no partial copy left behind.
	if _, err := os.Lstat(asset + ".noo-noo-bak"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("stray backup left: %v", err)
	}
	if _, err := os.Lstat(dest); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("partial copy left at destination: %v", err)
	}
}

// TestApplyCopyVerifyMismatch: a copier that silently drops a file must be
// caught by the count+bytes verify, leaving the source untouched.
func TestApplyCopyVerifyMismatch(t *testing.T) {
	home, asset, destRoot, cfg, pbs := fixture(t, "", false)
	lossy := func(ctx context.Context, src, dst string) error {
		if err := goCopy(ctx, src, dst); err != nil {
			return err
		}
		return os.Remove(filepath.Join(dst, "a.txt")) // silent loss
	}
	m := New(cfg, pbs, Deps{Home: home, Guard: &fakeGuard{}, Procs: &fakeProcs{}, Copy: lossy})

	dest := filepath.Join(destRoot, "asset")
	_, err := m.Apply(context.Background(), modules.Action{Module: "offload", Op: "relocate", Target: asset, Destination: dest})
	if err == nil || !strings.Contains(err.Error(), "copy verify FAILED") {
		t.Fatalf("want copy-verify failure, got %v", err)
	}
	if fi, err := os.Lstat(asset); err != nil || !fi.IsDir() {
		t.Fatalf("source touched after verify failure: %v %v", fi, err)
	}
	if _, err := os.Lstat(dest); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("bad copy left at destination: %v", err)
	}
}

// TestApplyStopGateRefusesWhileRunning: colima/claude-vm-bundles style
// relocations are refused while the owning process is reported running.
func TestApplyStopGateRefusesWhileRunning(t *testing.T) {
	for _, gate := range []string{"colima", "Claude"} {
		t.Run(gate, func(t *testing.T) {
			home, asset, destRoot, cfg, pbs := fixture(t, gate, false)
			procs := &fakeProcs{running: map[string]bool{gate: true}}
			m := New(cfg, pbs, Deps{Home: home, Guard: &fakeGuard{}, Procs: procs, Copy: goCopy})

			dest := filepath.Join(destRoot, "asset")
			_, err := m.Apply(context.Background(), modules.Action{Module: "offload", Op: "relocate", Target: asset, Destination: dest})
			if err == nil || !strings.Contains(err.Error(), "stop-gate") {
				t.Fatalf("want stop-gate refusal, got %v", err)
			}
			if len(procs.calls) != 1 || procs.calls[0] != gate {
				t.Fatalf("process checker calls = %v, want [%s]", procs.calls, gate)
			}
			// Nothing moved.
			if fi, err := os.Lstat(asset); err != nil || !fi.IsDir() {
				t.Fatalf("source touched despite stop-gate: %v %v", fi, err)
			}

			// Process stopped → the same action proceeds.
			procs.running[gate] = false
			if _, err := m.Apply(context.Background(), modules.Action{Module: "offload", Op: "relocate", Target: asset, Destination: dest}); err != nil {
				t.Fatalf("apply after stop: %v", err)
			}
		})
	}
}

// TestApplyGuardRefusalBlocksBeforeAnyMutation: the volume guard runs AT
// apply and a refusal leaves everything untouched.
func TestApplyGuardRefusalBlocksBeforeAnyMutation(t *testing.T) {
	home, asset, destRoot, cfg, pbs := fixture(t, "", false)
	guard := &fakeGuard{err: fmt.Errorf("wrong disk mounted")}
	m := New(cfg, pbs, Deps{Home: home, Guard: guard, Procs: &fakeProcs{}, Copy: goCopy})

	dest := filepath.Join(destRoot, "asset")
	_, err := m.Apply(context.Background(), modules.Action{Module: "offload", Op: "relocate", Target: asset, Destination: dest})
	if err == nil || !strings.Contains(err.Error(), "destination refused") {
		t.Fatalf("want guard refusal, got %v", err)
	}
	if names := mustReadDirNames(t, destRoot); len(names) != 0 {
		t.Fatalf("destination mutated despite refusal: %v", names)
	}
	if fi, err := os.Lstat(asset); err != nil || !fi.IsDir() {
		t.Fatalf("source touched despite refusal: %v %v", fi, err)
	}
}

func TestApplyRefusesNativeConfigAsset(t *testing.T) {
	home := t.TempDir()
	pn := filepath.Join(home, "Library", "pnpm")
	if err := os.MkdirAll(pn, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := Config{DestRoot: t.TempDir(), DestVolumeUUID: "0DBD1B63-0377-450B-A340-7E72D0925EBC"}
	m := New(cfg, nil, Deps{Home: home, Guard: &fakeGuard{}, Procs: &fakeProcs{}, Copy: goCopy})
	_, err := m.Apply(context.Background(), modules.Action{Module: "offload", Op: "relocate", Target: pn, Destination: filepath.Join(cfg.DestRoot, "pnpm-store")})
	if err == nil || !strings.Contains(err.Error(), "native mechanism") {
		t.Fatalf("want native-mechanism refusal, got %v", err)
	}
}

func TestApplyRefusesDestinationEscape(t *testing.T) {
	home, asset, _, cfg, pbs := fixture(t, "", false)
	m := New(cfg, pbs, Deps{Home: home, Guard: &fakeGuard{}, Procs: &fakeProcs{}, Copy: goCopy})
	_, err := m.Apply(context.Background(), modules.Action{Module: "offload", Op: "relocate", Target: asset, Destination: "/tmp/elsewhere"})
	if err == nil || !strings.Contains(err.Error(), "escapes dest_root") {
		t.Fatalf("want dest-escape refusal, got %v", err)
	}
}

// --- Scan ---------------------------------------------------------------------

// TestScanNativeConfigEvidence: native-config assets surface the EXACT
// supported command in Evidence and never become file actions.
func TestScanNativeConfigEvidence(t *testing.T) {
	home := t.TempDir()
	for _, d := range []string{"Library/pnpm", ".colima", ".claude", "Library/Application Support/Local/blueprints"} {
		if err := os.MkdirAll(filepath.Join(home, d), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(home, d, "f"), []byte("data"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	destRoot := t.TempDir()
	cfg := Config{DestRoot: destRoot, DestVolumeUUID: "0DBD1B63-0377-450B-A340-7E72D0925EBC"}
	m := New(cfg, nil, Deps{Home: home, Guard: &fakeGuard{}, Procs: &fakeProcs{}, Copy: goCopy})
	rep, err := m.Scan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	byAsset := map[string]modules.Item{}
	for _, it := range rep.Items {
		byAsset[it.Evidence["asset_id"]] = it
	}

	// Expectations are built from the test's OWN destRoot fixture — the
	// command is a machine-agnostic template rendered against the configured
	// destination, never a literal from another machine.
	wantCmds := map[string]string{
		"pnpm-store":    "pnpm config set store-dir " + destRoot + "/.pnpm-store --global",
		"colima":        "COLIMA_HOME",
		"claude-config": "CLAUDE_CONFIG_DIR",
	}
	for asset, want := range wantCmds {
		it, ok := byAsset[asset]
		if !ok {
			t.Fatalf("asset %s missing from scan (items: %v)", asset, rep.Items)
		}
		if it.Evidence["class"] != string(ClassNativeConfig) {
			t.Fatalf("%s class = %s, want native-config", asset, it.Evidence["class"])
		}
		if !strings.Contains(it.Evidence["native_command"], want) {
			t.Fatalf("%s native_command %q does not carry %q", asset, it.Evidence["native_command"], want)
		}
	}
	// pnpm-store must carry the verbatim command, not a paraphrase.
	if got := byAsset["pnpm-store"].Evidence["native_command"]; got != wantCmds["pnpm-store"] {
		t.Fatalf("pnpm-store command = %q, want verbatim %q", got, wantCmds["pnpm-store"])
	}

	// Blueprints: relocate class, NeverDelete surfaced.
	bpItem := byAsset["localwp-blueprints"]
	if bpItem.Evidence["class"] != string(ClassRelocate) || bpItem.Evidence["never_delete"] != "true" {
		t.Fatalf("localwp-blueprints evidence wrong: %v", bpItem.Evidence)
	}

	// Manual generic entry: refuses auto, explains the smoke-test need.
	el := byAsset["electron-userdata"]
	if el.Evidence["class"] != string(ClassManual) || !strings.Contains(el.Evidence["note"], "smoke-test") {
		t.Fatalf("electron-userdata evidence wrong: %v", el.Evidence)
	}

	// Native-config and manual assets never become actions.
	for _, a := range m.Plan(rep) {
		if a.Op != "relocate" {
			t.Fatalf("non-relocate op planned: %v", a)
		}
		if strings.Contains(a.Target, "pnpm") || strings.Contains(a.Target, ".colima") || strings.Contains(a.Target, ".claude") {
			t.Fatalf("file action planned for a native-config asset: %v", a)
		}
	}
}

// TestScanDetectsUnguardedExternalSymlinks: top-level $HOME symlinks
// resolving under /Volumes/ are reported as standing risks (report-only).
func TestScanDetectsUnguardedExternalSymlinks(t *testing.T) {
	home := t.TempDir()
	for _, name := range []string{"Desktop", "Documents", "Downloads"} {
		if err := os.Symlink("/Volumes/SATECHI/"+name, filepath.Join(home, name)); err != nil {
			t.Fatal(err)
		}
	}
	// A benign internal symlink must NOT be flagged.
	if err := os.MkdirAll(filepath.Join(home, "real"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(home, "real"), filepath.Join(home, "alias")); err != nil {
		t.Fatal(err)
	}

	m := New(Config{}, nil, Deps{Home: home, Guard: &fakeGuard{}, Procs: &fakeProcs{}, Copy: goCopy})
	rep, err := m.Scan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	flagged := map[string]string{}
	for _, it := range rep.Items {
		if it.Evidence["class"] == "unguarded-external-symlink" {
			flagged[filepath.Base(it.Path)] = it.Evidence["target"]
		}
	}
	for _, name := range []string{"Desktop", "Documents", "Downloads"} {
		if target, ok := flagged[name]; !ok || !strings.HasPrefix(target, "/Volumes/") {
			t.Fatalf("%s not flagged as unguarded external symlink (flagged: %v)", name, flagged)
		}
	}
	if _, ok := flagged["alias"]; ok {
		t.Fatal("internal symlink wrongly flagged")
	}
	// Report-only: no action may come out of a risk item.
	m2 := New(Config{DestRoot: t.TempDir(), DestVolumeUUID: "u"}, nil, Deps{Home: home, Guard: &fakeGuard{}, Procs: &fakeProcs{}, Copy: goCopy})
	rep2, _ := m2.Scan(context.Background())
	for _, a := range m2.Plan(rep2) {
		if strings.Contains(a.Target, "Desktop") || strings.Contains(a.Target, "Documents") || strings.Contains(a.Target, "Downloads") {
			t.Fatalf("risk item produced an action: %v", a)
		}
	}
}

func TestScanUnconfiguredVerdictAndEmptyPlan(t *testing.T) {
	home := t.TempDir()
	bp := filepath.Join(home, "Library", "Application Support", "Local", "blueprints")
	if err := os.MkdirAll(bp, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bp, "f"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	m := New(Config{}, nil, Deps{Home: home, Guard: &fakeGuard{err: errors.New("must not be called")}, Procs: &fakeProcs{}, Copy: goCopy})
	rep, err := m.Scan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, it := range rep.Items {
		if it.Evidence["asset_id"] == "localwp-blueprints" {
			found = true
			if !strings.Contains(it.Evidence["guard_verdict"], "offload disabled") {
				t.Fatalf("verdict = %q, want disabled note", it.Evidence["guard_verdict"])
			}
		}
	}
	if !found {
		t.Fatal("blueprints item missing")
	}
	if actions := m.Plan(rep); len(actions) != 0 {
		t.Fatalf("unconfigured offload planned actions: %v", actions)
	}
}

// --- {dest_root} templating -------------------------------------------------

// TestScanNativeCommandRendersConfiguredDestRoot: a configured dest_root is
// substituted into the NativeCommand template at read time — no {dest_root}
// token and no foreign machine's absolute path leaks through.
func TestScanNativeCommandRendersConfiguredDestRoot(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, "Library", "pnpm"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "Library", "pnpm", "f"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	destRoot := t.TempDir()
	cfg := Config{DestRoot: destRoot, DestVolumeUUID: "u"}
	m := New(cfg, nil, Deps{Home: home, Guard: &fakeGuard{}, Procs: &fakeProcs{}, Copy: goCopy})
	rep, err := m.Scan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var cmd string
	for _, it := range rep.Items {
		if it.Evidence["asset_id"] == "pnpm-store" {
			cmd = it.Evidence["native_command"]
		}
	}
	if !strings.Contains(cmd, destRoot) {
		t.Fatalf("native_command %q does not carry configured destRoot %q", cmd, destRoot)
	}
	if strings.Contains(cmd, destRootToken) {
		t.Fatalf("native_command %q still carries the unrendered %s token", cmd, destRootToken)
	}
	if strings.Contains(cmd, "/Volumes/SATECHI") {
		t.Fatalf("native_command %q still carries a hardcoded machine path", cmd)
	}
}

// TestScanNativeCommandPlaceholderWhenUnconfigured: with no dest_root, the
// command renders the generic placeholder (a fill-in-the-blank template) and
// the guard verdict carries the "configure [offload] dest_root" guidance.
func TestScanNativeCommandPlaceholderWhenUnconfigured(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, "Library", "pnpm"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "Library", "pnpm", "f"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Unconfigured: the guard must NOT be consulted.
	m := New(Config{}, nil, Deps{Home: home, Guard: &fakeGuard{err: errors.New("must not be called")}, Procs: &fakeProcs{}, Copy: goCopy})
	rep, err := m.Scan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var cmd, verdict string
	for _, it := range rep.Items {
		if it.Evidence["asset_id"] == "pnpm-store" {
			cmd = it.Evidence["native_command"]
			verdict = it.Evidence["guard_verdict"]
		}
	}
	if !strings.Contains(cmd, destRootPlaceholder) {
		t.Fatalf("unconfigured native_command %q does not carry placeholder %q", cmd, destRootPlaceholder)
	}
	if strings.Contains(cmd, destRootToken) {
		t.Fatalf("unconfigured native_command %q still carries the unrendered token", cmd)
	}
	if !strings.Contains(verdict, "offload disabled") || !strings.Contains(verdict, "dest_root") {
		t.Fatalf("verdict %q does not carry the configure-dest_root guidance", verdict)
	}
}

// --- path-gated Apply (sub-asset gate) ---------------------------------------

// pathGatedFixture builds the standard relocate fixture but flips the asset to
// a path-gated (sub-asset) entry.
func pathGatedFixture(t *testing.T) (home, asset, destRoot string, cfg Config, pbs []Playbook) {
	t.Helper()
	home, asset, destRoot, cfg, pbs = fixture(t, "", false)
	pbs[0].Gate = GatePath
	return home, asset, destRoot, cfg, pbs
}

// TestApplyPathGateBlocksWhenLive: a path-gated entry consults the injected
// PathProber (NOT pgrep) and, on a live verdict (or any fail-safe doubt),
// refuses the move with the source untouched.
func TestApplyPathGateBlocksWhenLive(t *testing.T) {
	home, asset, destRoot, cfg, pbs := pathGatedFixture(t)
	probe := &fakePathProbe{live: true, proof: "lsof +D: 3 open file(s) — fail-safe LIVE"}
	procs := &fakeProcs{}
	m := New(cfg, pbs, Deps{Home: home, Guard: &fakeGuard{}, Procs: procs, Copy: goCopy, PathProbe: probe.probe})

	dest := filepath.Join(destRoot, "asset")
	_, err := m.Apply(context.Background(), modules.Action{Module: "offload", Op: "relocate", Target: asset, Destination: dest})
	if err == nil || !strings.Contains(err.Error(), "path-gate") {
		t.Fatalf("want path-gate refusal, got %v", err)
	}
	if !strings.Contains(err.Error(), probe.proof) {
		t.Fatalf("path-gate error %q does not surface the prober proof", err)
	}
	if len(probe.calls) != 1 || probe.calls[0] != asset {
		t.Fatalf("path prober calls = %v, want [%s]", probe.calls, asset)
	}
	if len(procs.calls) != 0 {
		t.Fatalf("pgrep consulted for a path-gated entry: %v", procs.calls)
	}
	if fi, err := os.Lstat(asset); err != nil || !fi.IsDir() {
		t.Fatalf("source touched despite path-gate: %v %v", fi, err)
	}
	if names := mustReadDirNames(t, destRoot); len(names) != 0 {
		t.Fatalf("destination mutated despite path-gate: %v", names)
	}
}

// TestApplyPathGateProceedsWhenClear: an lsof-clean verdict lets the same
// relocate proceed to a verified symlink swap.
func TestApplyPathGateProceedsWhenClear(t *testing.T) {
	home, asset, destRoot, cfg, pbs := pathGatedFixture(t)
	probe := &fakePathProbe{live: false, proof: "no process holds any file open"}
	procs := &fakeProcs{}
	m := New(cfg, pbs, Deps{Home: home, Guard: &fakeGuard{}, Procs: procs, Copy: goCopy, PathProbe: probe.probe})

	dest := filepath.Join(destRoot, "asset")
	if _, err := m.Apply(context.Background(), modules.Action{Module: "offload", Op: "relocate", Target: asset, Destination: dest}); err != nil {
		t.Fatalf("path-gate clear should proceed: %v", err)
	}
	if len(probe.calls) != 1 || probe.calls[0] != asset {
		t.Fatalf("path prober calls = %v, want [%s]", probe.calls, asset)
	}
	if len(procs.calls) != 0 {
		t.Fatalf("pgrep consulted for a path-gated entry: %v", procs.calls)
	}
	fi, err := os.Lstat(asset)
	if err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("target not relocated to a symlink: %v %v", fi, err)
	}
}

// TestClaudeJobsRelocatesIndependentlyOfParent: the shipped claude-jobs
// exemplar is a flat sibling ClassRelocate path-gated entry that plans and
// applies on its own, while its ClassNativeConfig parent (~/.claude) is never
// planned and stays a real directory — the live 2026-07-15 win.
func TestClaudeJobsRelocatesIndependentlyOfParent(t *testing.T) {
	home := t.TempDir()
	claude := filepath.Join(home, ".claude")
	jobs := filepath.Join(claude, "jobs")
	if err := os.MkdirAll(jobs, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(claude, "config.json"), []byte("cfg"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(jobs, "job1.log"), []byte("stale job scratch"), 0o644); err != nil {
		t.Fatal(err)
	}
	destRoot := filepath.Join(t.TempDir(), "offload")
	if err := os.MkdirAll(destRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := Config{DestRoot: destRoot, DestVolumeUUID: "u"}
	probe := &fakePathProbe{live: false}
	m := New(cfg, nil, Deps{Home: home, Guard: &fakeGuard{}, Procs: &fakeProcs{}, Copy: goCopy, PathProbe: probe.probe})

	rep, err := m.Scan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	byAsset := map[string]modules.Item{}
	for _, it := range rep.Items {
		byAsset[it.Evidence["asset_id"]] = it
	}

	// Parent is native-config and never a file action; sub-asset is
	// relocate + path-gated with the parent named cosmetically.
	if byAsset["claude-config"].Evidence["class"] != string(ClassNativeConfig) {
		t.Fatalf("claude-config not native-config: %v", byAsset["claude-config"].Evidence)
	}
	ji, ok := byAsset["claude-jobs"]
	if !ok {
		t.Fatalf("claude-jobs missing from scan (items: %v)", rep.Items)
	}
	if ji.Evidence["class"] != string(ClassRelocate) {
		t.Fatalf("claude-jobs class = %q, want relocate", ji.Evidence["class"])
	}
	if ji.Evidence["gate"] != "path" {
		t.Fatalf("claude-jobs gate = %q, want path", ji.Evidence["gate"])
	}
	if ji.Evidence["parent_asset"] != "claude-config" {
		t.Fatalf("claude-jobs parent_asset = %q, want claude-config", ji.Evidence["parent_asset"])
	}

	// Plan yields the jobs relocate and NEVER the parent.
	plan := m.Plan(rep)
	var jobsAction *modules.Action
	for i := range plan {
		if filepath.Clean(plan[i].Target) == filepath.Clean(claude) {
			t.Fatalf("parent ~/.claude planned for relocation: %v", plan[i])
		}
		if filepath.Clean(plan[i].Target) == filepath.Clean(jobs) {
			jobsAction = &plan[i]
		}
	}
	if jobsAction == nil {
		t.Fatalf("claude-jobs not planned: %v", plan)
	}

	// Apply the sub-asset independently: jobs becomes a symlink, parent stays.
	if _, err := m.Apply(context.Background(), *jobsAction); err != nil {
		t.Fatalf("apply claude-jobs: %v", err)
	}
	if fi, err := os.Lstat(jobs); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("jobs not relocated to a symlink: %v %v", fi, err)
	}
	if fi, err := os.Lstat(claude); err != nil || !fi.IsDir() {
		t.Fatalf("parent ~/.claude damaged by sub-asset relocate: %v %v", fi, err)
	}
	if b, err := os.ReadFile(filepath.Join(claude, "config.json")); err != nil || string(b) != "cfg" {
		t.Fatalf("parent config lost: %q err %v", b, err)
	}
	// The probe gated the sub-asset path, not a process.
	if len(probe.calls) != 1 || probe.calls[0] != jobs {
		t.Fatalf("path prober calls = %v, want [%s]", probe.calls, jobs)
	}
}

// --- production Copier -----------------------------------------------------

func TestDittoCopy(t *testing.T) {
	if _, err := os.Stat("/usr/bin/ditto"); err != nil {
		t.Skip("ditto unavailable (not macOS)")
	}
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "f.txt"), []byte("hello"), 0o640); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(t.TempDir(), "copy")
	if err := DittoCopy(context.Background(), src, dst); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(dst, "f.txt"))
	if err != nil || string(b) != "hello" {
		t.Fatalf("ditto copy content: %q err %v", b, err)
	}
}

// TestAllocatedBlocksSurvivesPermissionDenied: one unreadable subdirectory
// must not collapse the size to 0 — count the rest, report the gap.
func TestAllocatedBlocksSurvivesPermissionDenied(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root bypasses directory permissions")
	}
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "readable.bin"), make([]byte, 4096), 0o644); err != nil {
		t.Fatal(err)
	}
	locked := filepath.Join(root, "locked")
	if err := os.MkdirAll(locked, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(locked, "hidden.bin"), make([]byte, 4096), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(locked, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })

	size, err := allocatedBlocks(root)
	if size < 4096 {
		t.Fatalf("size = %d, want at least the readable 4096 bytes", size)
	}
	if err == nil || !strings.Contains(err.Error(), "partial") {
		t.Fatalf("want partial-size error, got %v", err)
	}
}
