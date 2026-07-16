package vmdisk

import (
	"bytes"
	"context"
	"errors"
	"os"
	"strings"
	"testing"
)

// fakeRunner scripts CLI output by command signature and records every call,
// including the env — so a test can prove which LIMA_HOME was probed. It
// never execs anything; no test starts, stops, or touches a real VM.
type fakeRunner struct {
	fn    func(env []string, name string, args []string) ([]byte, error)
	calls []fakeCall
}

type fakeCall struct {
	env  []string
	name string
	args []string
}

func (f *fakeRunner) Output(ctx context.Context, stdin []byte, name string, args ...string) ([]byte, error) {
	return f.OutputEnv(ctx, nil, name, args...)
}

func (f *fakeRunner) OutputEnv(ctx context.Context, env []string, name string, args ...string) ([]byte, error) {
	f.calls = append(f.calls, fakeCall{env: env, name: name, args: args})
	return f.fn(env, name, args)
}

func joinArgs(args []string) string { return strings.Join(args, " ") }

func envHas(env []string, want string) bool {
	for _, e := range env {
		if e == want {
			return true
		}
	}
	return false
}

// lookPathSet builds a lookPath stub from a set of "installed" binaries.
func lookPathSet(installed ...string) func(string) (string, error) {
	set := map[string]bool{}
	for _, b := range installed {
		set[b] = true
	}
	return func(name string) (string, error) {
		if set[name] {
			return "/usr/local/bin/" + name, nil
		}
		return "", errors.New("not found")
	}
}

const dfDockerFull = `Filesystem     1B-blocks         Used   Available Use% Mounted on
overlay      67418161152  61000000000  6418161152  91% /
tmpfs           67108864            0    67108864   0% /dev
/dev/vdb1    64424509440  58982400000  5442109440  92% /var/lib/docker
`

// --- Detection ---

// colimaFakeFn scripts `colima list --json` (two profiles, disk in bytes =
// 64 GiB) and `colima ssh` (df output). Split out so the detection test stays
// flat (gocyclo).
func colimaFakeFn(_ []string, name string, args []string) ([]byte, error) {
	switch {
	case name == "colima" && joinArgs(args) == "list --json":
		return []byte(`{"name":"default","status":"Running","disk":68719476736}
{"name":"builder","status":"Stopped","disk":10737418240}
`), nil
	case name == "colima" && args[0] == "ssh":
		return []byte(dfDockerFull), nil
	}
	return nil, errors.New("unexpected call: " + name + " " + joinArgs(args))
}

// countColimaSSH counts the `colima ssh` calls — the proof the df read never
// touched a stopped VM.
func countColimaSSH(calls []fakeCall) int {
	n := 0
	for _, c := range calls {
		if c.name == "colima" && len(c.args) > 0 && c.args[0] == "ssh" {
			n++
		}
	}
	return n
}

// requireDatadiskPct fails unless dd is present at the given usage percentage.
func requireDatadiskPct(t *testing.T, dd DiskUsage, wantPct int) {
	t.Helper()
	if !dd.Present {
		t.Fatalf("datadisk not present")
	}
	if pct := dd.Pct(); pct != wantPct {
		t.Errorf("datadisk pct = %d, want %d", pct, wantPct)
	}
}

func TestDetectColimaParsesProfilesAndDatadisk(t *testing.T) {
	fr := &fakeRunner{fn: colimaFakeFn}
	d := &Detector{Runner: fr, lookPath: lookPathSet("colima")}
	vms, err := d.Detect(context.Background())
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if len(vms) != 2 {
		t.Fatalf("want 2 vms, got %d: %+v", len(vms), vms)
	}
	def := vms[0]
	if def.Kind != "colima" || def.Name != "default" || !def.Running {
		t.Fatalf("bad default vm: %+v", def)
	}
	if def.ConfiguredDiskGiB != 64 {
		t.Errorf("ConfiguredDiskGiB = %d, want 64", def.ConfiguredDiskGiB)
	}
	requireDatadiskPct(t, def.Datadisk, 91)
	if def.Datadisk.Mount != "/var/lib/docker" {
		t.Errorf("datadisk mount = %q, want /var/lib/docker", def.Datadisk.Mount)
	}
	// Stopped profile: no df read, datadisk absent.
	if vms[1].Running || vms[1].Datadisk.Present {
		t.Errorf("stopped profile should have no datadisk: %+v", vms[1])
	}
	// Prove the df read never touched a stopped VM: exactly one ssh call.
	if sshCalls := countColimaSSH(fr.calls); sshCalls != 1 {
		t.Errorf("want exactly 1 ssh (running only), got %d", sshCalls)
	}
}

// TestDetectLimaInvisibilityTrap is the criterion-0 heart: a colima guest is
// invisible to a bare `limactl list` (default LIMA_HOME) and surfaces ONLY
// when limactl is probed with LIMA_HOME=$COLIMA_HOME/_lima. Here colima the
// binary is NOT installed, so the private-tree probe is the sole path.
func TestDetectLimaInvisibilityTrap(t *testing.T) {
	tmp := t.TempDir()
	// The _lima tree must exist for the probe to run.
	limaTree := tmp + "/_lima"
	if err := os.MkdirAll(limaTree, 0o755); err != nil {
		t.Fatal(err)
	}
	wantEnv := "LIMA_HOME=" + limaTree

	fr := &fakeRunner{fn: limaFakeFn(t, wantEnv)}
	d := &Detector{Runner: fr, lookPath: lookPathSet("limactl"), ColimaHome: tmp}
	vms, err := d.Detect(context.Background())
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if len(vms) != 1 {
		t.Fatalf("want 1 vm via _lima probe, got %d: %+v", len(vms), vms)
	}
	if vms[0].Kind != "lima" || vms[0].Name != "colima" {
		t.Fatalf("bad vm: %+v", vms[0])
	}
	if !vms[0].Datadisk.Present || vms[0].Datadisk.Pct() != 91 {
		t.Errorf("datadisk wrong: %+v", vms[0].Datadisk)
	}
	// Prove BOTH homes were probed (default first, then the colima tree).
	sawDefault, sawTree := sawBothLimaHomes(fr.calls, wantEnv)
	if !sawDefault || !sawTree {
		t.Errorf("expected both LIMA_HOME probes: default=%v tree=%v", sawDefault, sawTree)
	}
}

// limaFakeFn scripts `limactl list --json` (the colima guest visible ONLY under
// the _lima LIMA_HOME) and `limactl shell` (df output). Returning the closure
// from a helper keeps its branches off the test's gocyclo count.
func limaFakeFn(t *testing.T, wantEnv string) func([]string, string, []string) ([]byte, error) {
	return func(env []string, name string, args []string) ([]byte, error) {
		if name == "limactl" && joinArgs(args) == "list --json" {
			if envHas(env, wantEnv) {
				return []byte(`{"name":"colima","status":"Running","disk":64424509440}` + "\n"), nil
			}
			// Default LIMA_HOME: colima's guest is invisible.
			return []byte(""), nil
		}
		if name == "limactl" && args[0] == "shell" {
			if !envHas(env, wantEnv) {
				t.Errorf("df probe ran under wrong LIMA_HOME: %v", env)
			}
			return []byte(dfDockerFull), nil
		}
		return nil, errors.New("unexpected: " + name + " " + joinArgs(args))
	}
}

// sawBothLimaHomes reports whether both the default LIMA_HOME and the colima
// _lima tree were probed by `limactl list`.
func sawBothLimaHomes(calls []fakeCall, wantEnv string) (sawDefault, sawTree bool) {
	for _, c := range calls {
		if c.name == "limactl" && joinArgs(c.args) == "list --json" {
			if len(c.env) == 0 {
				sawDefault = true
			}
			if envHas(c.env, wantEnv) {
				sawTree = true
			}
		}
	}
	return sawDefault, sawTree
}

// TestDetectDedupColimaAcrossSources: when colima IS installed, its guest is
// found via `colima list` and must NOT be double-counted by the _lima probe.
func TestDetectDedupColimaAcrossSources(t *testing.T) {
	tmp := t.TempDir()
	if err := os.MkdirAll(tmp+"/_lima", 0o755); err != nil {
		t.Fatal(err)
	}
	fr := &fakeRunner{fn: func(env []string, name string, args []string) ([]byte, error) {
		switch {
		case name == "colima" && joinArgs(args) == "list --json":
			return []byte(`{"name":"default","status":"Running","disk":64424509440}` + "\n"), nil
		case name == "colima" && args[0] == "ssh":
			return []byte(dfDockerFull), nil
		case name == "limactl" && joinArgs(args) == "list --json":
			// colima registers its default profile as the LIMA instance
			// "colima" in its private tree — the dedup must map default→colima.
			return []byte(`{"name":"colima","status":"Running","disk":64424509440}` + "\n"), nil
		case name == "limactl" && args[0] == "shell":
			return []byte(dfDockerFull), nil
		}
		return nil, errors.New("unexpected: " + name)
	}}
	d := &Detector{Runner: fr, lookPath: lookPathSet("colima", "limactl"), ColimaHome: tmp}
	vms, err := d.Detect(context.Background())
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if len(vms) != 1 {
		t.Fatalf("dedup failed: want 1 vm, got %d: %+v", len(vms), vms)
	}
	if vms[0].Kind != "colima" {
		t.Errorf("colima should own the name, got kind %q", vms[0].Kind)
	}
}

func TestDetectMissingBinariesSilent(t *testing.T) {
	fr := &fakeRunner{fn: func(env []string, name string, args []string) ([]byte, error) {
		t.Fatalf("runner must not be called when no manager is installed: %s", name)
		return nil, nil
	}}
	d := &Detector{Runner: fr, lookPath: lookPathSet()} // nothing installed
	vms, err := d.Detect(context.Background())
	if err != nil {
		t.Fatalf("missing binaries must not error: %v", err)
	}
	if len(vms) != 0 {
		t.Fatalf("want no vms, got %+v", vms)
	}
}

func TestDetectColimaUnhappyIsNotAnError(t *testing.T) {
	fr := &fakeRunner{fn: func(env []string, name string, args []string) ([]byte, error) {
		return nil, errors.New("colima daemon not responding")
	}}
	d := &Detector{Runner: fr, lookPath: lookPathSet("colima")}
	vms, err := d.Detect(context.Background())
	if err != nil {
		t.Fatalf("a broken colima must degrade to no-VMs, not error: %v", err)
	}
	if len(vms) != 0 {
		t.Fatalf("want no vms, got %+v", vms)
	}
}

// --- Advice: grow vs relocate-then-grow vs blocked ---

// atRiskVM builds a running colima guest at ~91% of a 60 GiB datadisk.
func atRiskVM() VM {
	return VM{
		Kind:              "colima",
		Name:              "default",
		Running:           true,
		ConfiguredDiskGiB: 60,
		Datadisk:          DiskUsage{Mount: "/var/lib/docker", TotalBytes: 60 * gib, UsedBytes: 55 * gib, Present: true},
	}
}

// fixedStatfs returns a statfsFree stub keyed by path.
func fixedStatfs(free map[string]uint64) func(string) (uint64, error) {
	return func(p string) (uint64, error) {
		if v, ok := free[p]; ok {
			return v, nil
		}
		return 0, errors.New("statfs: no such path " + p)
	}
}

func identityEval(p string) (string, error) { return p, nil }

func TestAdviseGrowWhenHostHasHeadroom(t *testing.T) {
	d := &Detector{
		ColimaHome:   "/host/colima",
		evalSymlinks: identityEval,
		statfsFree:   fixedStatfs(map[string]uint64{"/host/colima": 500 * gib}),
	}
	adv, ok := d.Advise(atRiskVM(), "")
	if !ok {
		t.Fatal("expected advice for an at-risk datadisk")
	}
	if adv.Level != AdviceGrow {
		t.Fatalf("level = %q, want grow", adv.Level)
	}
	// target = ceil(2*55 GiB) = 110 GiB; must be a real grow above 60.
	if adv.TargetGiB != 110 {
		t.Errorf("TargetGiB = %d, want 110", adv.TargetGiB)
	}
	if !strings.Contains(adv.Command, "colima stop") {
		t.Errorf("grow must stop first: %q", adv.Command)
	}
	if !strings.Contains(adv.Command, "--disk 110") {
		t.Errorf("grow must target --disk N: %q", adv.Command)
	}
	if strings.Contains(adv.Command, "--root-disk") {
		t.Errorf("grow must NEVER target --root-disk: %q", adv.Command)
	}
}

func TestAdviseRelocateThenGrowWhenHostFull(t *testing.T) {
	d := &Detector{
		ColimaHome:   "/host/colima",
		evalSymlinks: identityEval,
		statfsFree: fixedStatfs(map[string]uint64{
			"/host/colima": 5 * gib,    // internal disk is full
			"/Volumes/BIG": 1700 * gib, // roomy external
		}),
	}
	adv, ok := d.Advise(atRiskVM(), "/Volumes/BIG")
	if !ok {
		t.Fatal("expected advice")
	}
	if adv.Level != AdviceRelocateThenGrow {
		t.Fatalf("level = %q, want relocate-then-grow", adv.Level)
	}
	if !strings.Contains(adv.Command, "/Volumes/BIG") {
		t.Errorf("relocate command must reference dest_root: %q", adv.Command)
	}
	if !strings.Contains(adv.Command, "--disk 110") {
		t.Errorf("must still grow to target after relocate: %q", adv.Command)
	}
	if strings.Contains(adv.Command, "--root-disk") {
		t.Errorf("must never target --root-disk: %q", adv.Command)
	}
}

func TestAdviseBlockedWhenNoHeadroomNoDest(t *testing.T) {
	d := &Detector{
		ColimaHome:   "/host/colima",
		evalSymlinks: identityEval,
		statfsFree:   fixedStatfs(map[string]uint64{"/host/colima": 5 * gib}),
	}
	adv, ok := d.Advise(atRiskVM(), "") // no offload dest configured
	if !ok {
		t.Fatal("expected advice")
	}
	if adv.Level != AdviceBlocked {
		t.Fatalf("level = %q, want blocked", adv.Level)
	}
	if adv.Command != "" {
		t.Errorf("blocked advice must carry no runnable command: %q", adv.Command)
	}
}

func TestAdviseRelocateFallsBackToBlockedWhenDestAlsoFull(t *testing.T) {
	d := &Detector{
		ColimaHome:   "/host/colima",
		evalSymlinks: identityEval,
		statfsFree: fixedStatfs(map[string]uint64{
			"/host/colima": 5 * gib,
			"/Volumes/BIG": 1 * gib, // dest can't absorb the growth either
		}),
	}
	adv, _ := d.Advise(atRiskVM(), "/Volumes/BIG")
	if adv.Level != AdviceBlocked {
		t.Fatalf("dest without room must fall back to blocked, got %q", adv.Level)
	}
}

// TestAdviseUsesVMHomeForHeadroom: a colima guest surfaced via the private
// _lima tree carries Home=$COLIMA_HOME (not ~/.lima). Headroom must resolve
// against THAT volume — here only the colima tree path has free space, so a
// grow is possible; resolving ~/.lima instead would have found nothing and
// wrongly blocked.
func TestAdviseUsesVMHomeForHeadroom(t *testing.T) {
	vm := VM{
		Kind:              "lima",
		Name:              "colima",
		Running:           true,
		ConfiguredDiskGiB: 60,
		Datadisk:          DiskUsage{Mount: "/mnt/lima", TotalBytes: 60 * gib, UsedBytes: 55 * gib, Present: true},
		Home:              "/Volumes/EXT/.colima", // found under $COLIMA_HOME/_lima
	}
	d := &Detector{
		evalSymlinks: identityEval,
		statfsFree:   fixedStatfs(map[string]uint64{"/Volumes/EXT/.colima": 500 * gib}),
	}
	adv, ok := d.Advise(vm, "")
	if !ok || adv.Level != AdviceGrow {
		t.Fatalf("expected grow via VM.Home volume, got ok=%v level=%q", ok, adv.Level)
	}
}

func TestAdviseNoneBelowThreshold(t *testing.T) {
	vm := atRiskVM()
	vm.Datadisk = DiskUsage{Mount: "/var/lib/docker", TotalBytes: 60 * gib, UsedBytes: 30 * gib, Present: true} // 50%
	d := &Detector{ColimaHome: "/host/colima", evalSymlinks: identityEval, statfsFree: fixedStatfs(map[string]uint64{"/host/colima": 500 * gib})}
	if _, ok := d.Advise(vm, ""); ok {
		t.Fatal("no advice expected below the warn threshold")
	}
}

func TestAdviseNoneWhenNotRunningOrUnknown(t *testing.T) {
	d := &Detector{ColimaHome: "/host/colima", evalSymlinks: identityEval, statfsFree: fixedStatfs(map[string]uint64{"/host/colima": 500 * gib})}
	stopped := atRiskVM()
	stopped.Running = false
	if _, ok := d.Advise(stopped, ""); ok {
		t.Error("no advice for a stopped VM")
	}
	unknown := atRiskVM()
	unknown.Datadisk.Present = false
	if _, ok := d.Advise(unknown, ""); ok {
		t.Error("no advice when datadisk usage is unknown")
	}
}

func TestAdviseBlockedWhenHostVolumeUnresolvable(t *testing.T) {
	// statfs fails for the colima home → we must NOT suggest an in-place grow
	// on an unproven volume; with no dest it is blocked.
	d := &Detector{
		ColimaHome:   "/host/colima",
		evalSymlinks: identityEval,
		statfsFree:   fixedStatfs(map[string]uint64{}), // every path errors
	}
	adv, ok := d.Advise(atRiskVM(), "")
	if !ok {
		t.Fatal("expected advice")
	}
	if adv.Level != AdviceBlocked {
		t.Fatalf("unresolved host volume must not grow-in-place, got %q", adv.Level)
	}
}

// --- Report + RenderSection surface ---

func TestRenderSectionEmptyWritesNothing(t *testing.T) {
	var buf bytes.Buffer
	RenderSection(&buf, Report{})
	if buf.Len() != 0 {
		t.Fatalf("empty report must render nothing, got %q", buf.String())
	}
}

func TestRenderSectionShowsVMsAndAdvice(t *testing.T) {
	d := &Detector{ColimaHome: "/host/colima", evalSymlinks: identityEval, statfsFree: fixedStatfs(map[string]uint64{"/host/colima": 500 * gib})}
	vm := atRiskVM()
	adv, _ := d.Advise(vm, "")
	r := Report{VMs: []VM{vm}, Advices: []Advice{adv}}
	var buf bytes.Buffer
	RenderSection(&buf, r)
	out := buf.String()
	for _, want := range []string{"VM datadisks", "colima default", "91%", "fix:", "--disk 110", "dockerd at risk"} {
		if !strings.Contains(out, want) {
			t.Errorf("status section missing %q\n---\n%s", want, out)
		}
	}
}

func TestRenderSectionStoppedAndUnknown(t *testing.T) {
	stopped := VM{Kind: "colima", Name: "old", Running: false}
	unknown := VM{Kind: "lima", Name: "x", Running: true, Datadisk: DiskUsage{Present: false}}
	var buf bytes.Buffer
	RenderSection(&buf, Report{VMs: []VM{stopped, unknown}})
	out := buf.String()
	if !strings.Contains(out, "colima old: stopped") {
		t.Errorf("stopped VM line missing: %s", out)
	}
	if !strings.Contains(out, "usage unknown") {
		t.Errorf("unknown-usage line missing: %s", out)
	}
}

func TestReportComputesAdviceForAtRiskVMs(t *testing.T) {
	fr := &fakeRunner{fn: func(env []string, name string, args []string) ([]byte, error) {
		switch {
		case name == "colima" && joinArgs(args) == "list --json":
			return []byte(`{"name":"default","status":"Running","disk":64424509440}` + "\n"), nil
		case name == "colima" && args[0] == "ssh":
			return []byte(dfDockerFull), nil
		}
		return nil, errors.New("unexpected")
	}}
	d := &Detector{
		Runner:       fr,
		lookPath:     lookPathSet("colima"),
		ColimaHome:   "/host/colima",
		evalSymlinks: identityEval,
		statfsFree:   fixedStatfs(map[string]uint64{"/host/colima": 500 * gib}),
	}
	r, err := d.Report(context.Background(), "")
	if err != nil {
		t.Fatalf("Report: %v", err)
	}
	if len(r.VMs) != 1 || len(r.Advices) != 1 {
		t.Fatalf("want 1 vm + 1 advice, got %d/%d", len(r.VMs), len(r.Advices))
	}
	if r.Advices[0].Level != AdviceGrow {
		t.Errorf("advice level = %q", r.Advices[0].Level)
	}
}

// --- df + list parsing ---

func TestParseDatadiskDFPrefersDockerMount(t *testing.T) {
	u := parseDatadiskDF([]byte(dfDockerFull))
	if !u.Present || u.Mount != "/var/lib/docker" {
		t.Fatalf("want /var/lib/docker row, got %+v", u)
	}
	if u.TotalBytes != 64424509440 || u.UsedBytes != 58982400000 {
		t.Errorf("byte columns wrong: %+v", u)
	}
}

func TestParseDatadiskDFFallsBackToLargestVirtioDisk(t *testing.T) {
	// No /var/lib/docker mount; the additional disk mounts elsewhere.
	df := `Filesystem     1B-blocks         Used   Available Use% Mounted on
/dev/vda1     8000000000   4000000000  4000000000  50% /
/dev/vdb1    64424509440  60000000000  4424509440  93% /mnt/lima
`
	u := parseDatadiskDF([]byte(df))
	if !u.Present || u.Mount != "/mnt/lima" {
		t.Fatalf("want /mnt/lima fallback, got %+v", u)
	}
	if u.Pct() != 93 {
		t.Errorf("pct = %d, want 93", u.Pct())
	}
}

func TestParseDatadiskDFEmpty(t *testing.T) {
	if u := parseDatadiskDF([]byte("garbage\n")); u.Present {
		t.Errorf("garbage df must not be Present: %+v", u)
	}
}

func TestParseColimaListNDJSONAndArray(t *testing.T) {
	nd := parseColimaList([]byte(`{"name":"a","status":"Running","disk":1073741824}
{"name":"b","status":"Stopped","disk":2147483648}
`))
	if len(nd) != 2 || nd[0].Name != "a" || nd[1].Name != "b" {
		t.Fatalf("NDJSON parse wrong: %+v", nd)
	}
	arr := parseColimaList([]byte(`[{"name":"c","status":"Running","disk":1073741824}]`))
	if len(arr) != 1 || arr[0].Name != "c" {
		t.Fatalf("array parse wrong: %+v", arr)
	}
	if parseColimaList([]byte("  \n")) != nil {
		t.Error("empty input must parse to nil")
	}
}

func TestDiskUsagePct(t *testing.T) {
	if (DiskUsage{}).Pct() != 0 {
		t.Error("zero total → 0 pct, no divide-by-zero")
	}
	if (DiskUsage{TotalBytes: 100, UsedBytes: 91}).Pct() != 91 {
		t.Error("pct math")
	}
}
