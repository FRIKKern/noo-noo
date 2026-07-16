// Package vmdisk detects VM inner-disk fullness — the failure the founding
// incident named verbatim ("performance halt because of storage"): colima's
// docker datadisk hit 100% INSIDE the guest and dockerd died silently while
// the host still reported plenty of free space. The host has no visibility
// into a guest filesystem, so noo-noo has to look inside.
//
// The disk that fills and kills dockerd is the DATADISK (`/dev/vdb1`
// bind-mounted at /var/lib/docker, sized by colima's `--disk`), NOT the
// root disk. Growing it is `colima stop && colima start --disk N`; colima
// bakes a resize2fs provision step into every boot so the guest ext4 grows
// automatically (live-proven 5G->8G, zero manual steps). But growing the
// datadisk image also consumes (N - current) GiB on the HOST volume where
// COLIMA_HOME physically lives — so a grow is only safe once that host
// volume is proven to have the headroom. When it doesn't and an offload
// destination is configured, the honest advice is relocate-then-grow.
//
// Everything here is diagnostic-only. The grow/relocate suggestion is
// RiskHigh and NEVER auto-applied — tearing down a running VM is the user's
// call. Detection is cheap and read-only (`colima list --json` +
// `colima ssh -- df -B1`, ~0.4s); it never starts, stops, or mutates a VM.
package vmdisk

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"strings"
)

// DatadiskWarnPct is the inner-fill fraction at or above which the datadisk
// is judged at risk of killing dockerd and an advisory is emitted.
const DatadiskWarnPct = 85

// gib is one gibibyte in bytes.
const gib = 1 << 30

// DiskUsage is one filesystem's usage as reported by `df -B1` inside a guest.
type DiskUsage struct {
	// Mount is the guest mount point (e.g. "/var/lib/docker").
	Mount string
	// TotalBytes / UsedBytes come straight from df's 1-byte-block columns.
	TotalBytes uint64
	UsedBytes  uint64
	// Present is true only when a real datadisk df row was parsed. A running
	// VM whose df we could not read leaves this false so the surface can say
	// "unknown" rather than "0% full".
	Present bool
}

// Pct returns integer fill percentage 0..100 (0 when total is unknown).
func (d DiskUsage) Pct() int {
	if d.TotalBytes == 0 {
		return 0
	}
	return int((d.UsedBytes * 100) / d.TotalBytes)
}

// VM is one detected guest — a colima profile or a standalone lima instance —
// with its datadisk usage when running.
type VM struct {
	// Kind is "colima" or "lima".
	Kind string
	// Name is the profile / instance name ("default", "colima", ...).
	Name string
	// Running reflects the manager's status; df is only meaningful when true.
	Running bool
	// ConfiguredDiskGiB is the datadisk size the manager was told to allocate
	// (colima `--disk`, lima disk). 0 when unknown.
	ConfiguredDiskGiB int
	// Datadisk is the inner /var/lib/docker (or additional-disk) usage.
	Datadisk DiskUsage
	// Home is the host directory whose backing volume must have headroom for a
	// grow — COLIMA_HOME for colima, and the exact LIMA_HOME the instance was
	// found under for lima (a colima guest surfaced via $COLIMA_HOME/_lima
	// lives on COLIMA_HOME's volume, NOT ~/.lima — resolving the wrong volume
	// would misjudge headroom). Empty falls back to the manager default.
	Home string
}

// AdviceLevel names the remediation branch.
type AdviceLevel string

const (
	// AdviceNone: the datadisk is below the warn threshold, nothing to do.
	AdviceNone AdviceLevel = "none"
	// AdviceGrow: the host volume has headroom — grow the datadisk in place.
	AdviceGrow AdviceLevel = "grow"
	// AdviceRelocateThenGrow: the host volume (the small internal disk) lacks
	// headroom but an offload destination is configured and has room — move
	// COLIMA_HOME to the external volume first, then grow (proactive after
	// offload).
	AdviceRelocateThenGrow AdviceLevel = "relocate-then-grow"
	// AdviceBlocked: the datadisk is full but neither the host volume nor a
	// configured offload destination can absorb the growth. Honest dead end:
	// free space or configure offload.
	AdviceBlocked AdviceLevel = "blocked"
)

// Advice is the remediation for one at-risk VM. It is advisory only: Risk is
// always high and Apply is never wired — the user runs the command by hand.
type Advice struct {
	VM    VM
	Level AdviceLevel
	// TargetGiB is the recommended new datadisk size, sized to demand.
	TargetGiB int
	// Command is the exact shell to run (empty for AdviceBlocked).
	Command string
	// Headline is a one-line human summary for the status surface.
	Headline string
	// Detail explains the host-volume reasoning (which volume, free vs needed).
	Detail string
	// HostVolume is the resolved mount point COLIMA_HOME physically lives on.
	HostVolume string
	// HostFreeBytes is that volume's free space; NeededBytes is (target-current).
	HostFreeBytes uint64
	NeededBytes   uint64
}

// Report bundles what Detect found for the status surface.
type Report struct {
	VMs     []VM
	Advices []Advice
}

// Detector enumerates VM managers and reads inner datadisk usage. All I/O is
// injectable so tests drive canned CLI output and never touch a real VM.
type Detector struct {
	// Runner runs colima/limactl/df; nil → the production exec runner.
	Runner Runner
	// ColimaHome overrides $COLIMA_HOME resolution; "" → $COLIMA_HOME or
	// $HOME/.colima.
	ColimaHome string

	// The seams below default to the real OS in production and are replaced
	// wholesale in tests.

	// lookPath reports whether a manager binary exists; a missing binary means
	// "that manager is not installed" → silently no VMs, never an error.
	lookPath func(string) (string, error)
	// statfsFree returns the free bytes of the volume backing a host path.
	statfsFree func(path string) (uint64, error)
	// evalSymlinks resolves a host path through symlinks before statfs.
	evalSymlinks func(string) (string, error)
}

// Detect enumerates colima profiles and lima instances (under BOTH the
// default LIMA_HOME and colima's private $COLIMA_HOME/_lima tree — bare
// `limactl list` is blind to colima's VMs) and reads each running guest's
// datadisk usage. A missing manager binary yields no VMs, never an error.
func (d *Detector) Detect(ctx context.Context) ([]VM, error) {
	lookPath := d.lookPath
	if lookPath == nil {
		lookPath = defaultLookPath
	}

	var vms []VM
	// Dedup: colima's guests re-appear in its private _lima tree under LIMA
	// instance names ("colima" for the default profile, "colima-<profile>"
	// otherwise) — record THOSE names, or every colima VM lists twice when
	// both binaries are installed.
	seen := map[string]bool{}

	if _, err := lookPath("colima"); err == nil {
		cvms, err := d.detectColima(ctx)
		if err != nil {
			return nil, err
		}
		for _, v := range cvms {
			vms = append(vms, v)
			seen[limaInstanceNameForColimaProfile(v.Name)] = true
		}
	}

	if _, err := lookPath("limactl"); err == nil {
		// Default LIMA_HOME first (standalone lima), then colima's private
		// tree. The second probe is the whole point: colima keeps its guests
		// in $COLIMA_HOME/_lima, invisible to a bare `limactl list`.
		homes := []string{"", filepath.Join(d.colimaHome(), "_lima")}
		for _, home := range homes {
			lvms, err := d.detectLima(ctx, home)
			if err != nil {
				return nil, err
			}
			for _, v := range lvms {
				if seen[v.Name] {
					continue // colima already accounts for this guest
				}
				vms = append(vms, v)
				seen[v.Name] = true
			}
		}
	}

	return vms, nil
}

// Report runs Detect and computes advice for every VM at or above the warn
// threshold. destRoot is the configured [offload] dest_root ("" = offload
// disabled), consulted only for the relocate-then-grow branch.
func (d *Detector) Report(ctx context.Context, destRoot string) (Report, error) {
	vms, err := d.Detect(ctx)
	if err != nil {
		return Report{}, err
	}
	r := Report{VMs: vms}
	for _, vm := range vms {
		if adv, ok := d.Advise(vm, destRoot); ok {
			r.Advices = append(r.Advices, adv)
		}
	}
	return r, nil
}

// Advise returns the remediation for a VM whose datadisk is at or above the
// warn threshold. The bool is false (and Advice zero) when there is nothing
// to advise — datadisk unknown, below threshold, or the VM is not running.
//
// The suggestion is only ever grow-in-place once the HOST volume that backs
// COLIMA_HOME is proven to have >= (target-current) GiB free. Without that
// headroom it degrades to relocate-then-grow when an offload destination is
// configured with room, else to an honest AdviceBlocked. It is never
// auto-applied: stopping a running VM is the user's decision.
func (d *Detector) Advise(vm VM, destRoot string) (Advice, bool) {
	if !vm.Running || !vm.Datadisk.Present {
		return Advice{}, false
	}
	if vm.Datadisk.Pct() < DatadiskWarnPct {
		return Advice{}, false
	}

	target := d.targetGiB(vm)
	currentGiB := vm.currentDiskGiB()
	needed := uint64(0)
	if target > currentGiB {
		needed = uint64(target-currentGiB) * gib
	}

	adv := Advice{
		VM:          vm,
		TargetGiB:   target,
		NeededBytes: needed,
	}

	// Resolve where COLIMA_HOME physically lives and how much room it has.
	// Only colima's datadisk lives under COLIMA_HOME; a standalone lima disk
	// lives under LIMA_HOME, but the same host-headroom reasoning applies to
	// the lima data dir, so we resolve the manager's home either way.
	hostPath := d.hostHome(vm)
	hostVol, free, resolved := d.resolveHostVolume(hostPath)
	adv.HostVolume = hostVol
	adv.HostFreeBytes = free

	pct := vm.Datadisk.Pct()
	switch {
	case resolved && free >= needed:
		adv.Level = AdviceGrow
		adv.Command = growCommand(vm, target)
		adv.Headline = fmt.Sprintf("%s %s: datadisk %s, %d%% full — dockerd at risk; grow to %dG",
			vm.Kind, vm.Name, humanBytes(vm.Datadisk.TotalBytes), pct, target)
		adv.Detail = fmt.Sprintf("host volume %s has %s free (need %s to grow) — safe to grow in place",
			hostVol, humanBytes(free), humanBytes(needed))
	case destRoot != "" && d.destUsable(destRoot, needed):
		adv.Level = AdviceRelocateThenGrow
		adv.Command = relocateThenGrowCommand(vm, destRoot, target)
		adv.Headline = fmt.Sprintf("%s %s: datadisk %s, %d%% full — dockerd at risk; host disk is full, relocate then grow",
			vm.Kind, vm.Name, humanBytes(vm.Datadisk.TotalBytes), pct)
		adv.Detail = fmt.Sprintf("host volume %s has only %s free (need %s); move %s to %s first, then grow to %dG",
			hostVolOrUnknown(hostVol, resolved), humanBytes(free), humanBytes(needed), hostPath, destRoot, target)
	default:
		adv.Level = AdviceBlocked
		adv.Headline = fmt.Sprintf("%s %s: datadisk %s, %d%% full — dockerd at risk; nowhere to grow",
			vm.Kind, vm.Name, humanBytes(vm.Datadisk.TotalBytes), pct)
		adv.Detail = fmt.Sprintf("host volume %s has only %s free (need %s) and no offload destination is configured — free space or set [offload] dest_root",
			hostVolOrUnknown(hostVol, resolved), humanBytes(free), humanBytes(needed))
	}
	return adv, true
}

// targetGiB sizes the recommended datadisk to demand: enough to bring usage
// to ~50% (double the used bytes), never smaller than one GiB above the
// current allocation so a grow is always a real grow.
func (d *Detector) targetGiB(vm VM) int {
	current := vm.currentDiskGiB()
	// Double the used space → target ~50% full after growth.
	wantBytes := vm.Datadisk.UsedBytes * 2
	want := int((wantBytes + gib - 1) / gib) // ceil to GiB
	if want <= current {
		want = current + 1
	}
	return want
}

// resolveHostVolume resolves a host path through symlinks and returns its
// backing volume mount point and free bytes. resolved is false when the path
// cannot be resolved or statfs fails (e.g. COLIMA_HOME does not exist yet) —
// callers must NOT suggest an in-place grow without a resolved volume.
func (d *Detector) resolveHostVolume(path string) (mount string, freeBytes uint64, resolved bool) {
	eval := d.evalSymlinks
	if eval == nil {
		eval = filepath.EvalSymlinks
	}
	statfs := d.statfsFree
	if statfs == nil {
		statfs = statfsFreeBytes
	}
	real, err := eval(path)
	if err != nil {
		real = path // fall back to the raw path; statfs may still answer
	}
	free, err := statfs(real)
	if err != nil {
		return "", 0, false
	}
	mp, err := mountPointOf(real)
	if err != nil {
		mp = real
	}
	return mp, free, true
}

// destUsable reports whether an offload destination is configured and its
// backing volume has room for the growth. This is the advisory gate — the
// full UUID + write-probe (core.VolGuard) runs only at apply time, and this
// suggestion is never auto-applied.
func (d *Detector) destUsable(destRoot string, needed uint64) bool {
	_, free, resolved := d.resolveHostVolume(destRoot)
	return resolved && free >= needed
}

// colimaHome returns the effective COLIMA_HOME: the override, else
// $COLIMA_HOME, else $HOME/.colima.
func (d *Detector) colimaHome() string {
	if d.ColimaHome != "" {
		return d.ColimaHome
	}
	return defaultColimaHome()
}

// hostHome returns the host directory whose volume must have headroom for a
// grow. Detection records it on the VM (the exact manager home the guest was
// found under); only when that is missing do we fall back to the manager
// default — COLIMA_HOME for colima, the default LIMA_HOME for lima.
func (d *Detector) hostHome(vm VM) string {
	if vm.Home != "" {
		return vm.Home
	}
	if vm.Kind == "colima" {
		return d.colimaHome()
	}
	return defaultLimaHome()
}

// currentDiskGiB is the VM's configured datadisk size, falling back to the
// observed total when the manager did not report a configured size. The
// fallback ceils: a df total is always a little under the allocated image
// (filesystem overhead), so ceil lands nearer the real `--disk N`.
func (v VM) currentDiskGiB() int {
	if v.ConfiguredDiskGiB > 0 {
		return v.ConfiguredDiskGiB
	}
	if v.Datadisk.TotalBytes > 0 {
		return int((v.Datadisk.TotalBytes + gib - 1) / gib)
	}
	return 0
}

// growCommand renders the in-place grow: stop first (colima refuses to resize
// a running VM), then start at the new size. The profile flag is omitted for
// the default profile to match colima's own ergonomics.
func growCommand(vm VM, targetGiB int) string {
	if vm.Kind == "colima" {
		if vm.Name == "" || vm.Name == "default" {
			return fmt.Sprintf("colima stop && colima start --disk %d", targetGiB)
		}
		return fmt.Sprintf("colima stop -p %s && colima start -p %s --disk %d", vm.Name, vm.Name, targetGiB)
	}
	// lima: disk is resized via limactl on a stopped instance.
	return fmt.Sprintf("limactl stop %s && limactl disk resize --size %dGiB %s && limactl start %s",
		vm.Name, targetGiB, vm.Name, vm.Name)
}

// relocateThenGrowCommand frames the proactive-after-offload path: move the
// manager home to the roomy external volume, then grow.
func relocateThenGrowCommand(vm VM, destRoot string, targetGiB int) string {
	if vm.Kind == "colima" {
		newHome := filepath.Join(destRoot, ".colima")
		return fmt.Sprintf("colima stop && mv ~/.colima %s && export COLIMA_HOME=%s && colima start --disk %d",
			newHome, newHome, targetGiB)
	}
	return fmt.Sprintf("# relocate lima home under %s, then: limactl disk resize --size %dGiB %s", destRoot, targetGiB, vm.Name)
}

func hostVolOrUnknown(mount string, resolved bool) string {
	if !resolved || mount == "" {
		return "(host volume unresolved)"
	}
	return mount
}

// humanBytes renders a byte count as a short human string (GiB/MiB) for the
// status surface.
func humanBytes(b uint64) string {
	switch {
	case b >= gib:
		return fmt.Sprintf("%.0fG", float64(b)/float64(gib))
	case b >= 1<<20:
		return fmt.Sprintf("%.0fM", float64(b)/float64(1<<20))
	case b == 0:
		return "0"
	default:
		return fmt.Sprintf("%dB", b)
	}
}

// RenderSection writes the VM datadisk section of `noo-noo status`. It writes
// NOTHING when there are no VMs — the honest empty state is silence, not a
// "no VMs found" banner cluttering a machine that never runs one. Running
// VMs whose datadisk read failed are shown as "usage unknown".
func RenderSection(w io.Writer, r Report) {
	if len(r.VMs) == 0 {
		return
	}
	var b strings.Builder
	b.WriteString("\nVM datadisks\n")
	adviceByKey := map[string]Advice{}
	for _, a := range r.Advices {
		adviceByKey[a.VM.Kind+"/"+a.VM.Name] = a
	}
	for _, vm := range r.VMs {
		switch {
		case !vm.Running:
			fmt.Fprintf(&b, "  %s %s: stopped\n", vm.Kind, vm.Name)
		case !vm.Datadisk.Present:
			fmt.Fprintf(&b, "  %s %s: running, datadisk usage unknown\n", vm.Kind, vm.Name)
		default:
			fmt.Fprintf(&b, "  %s %s: datadisk %s used of %s (%d%%)\n",
				vm.Kind, vm.Name, humanBytes(vm.Datadisk.UsedBytes),
				humanBytes(vm.Datadisk.TotalBytes), vm.Datadisk.Pct())
		}
		if a, ok := adviceByKey[vm.Kind+"/"+vm.Name]; ok {
			fmt.Fprintf(&b, "    ! %s\n", a.Headline)
			fmt.Fprintf(&b, "      %s\n", a.Detail)
			if a.Command != "" {
				fmt.Fprintf(&b, "      fix: %s\n", a.Command)
			}
		}
	}
	_, _ = w.Write([]byte(b.String()))
}
