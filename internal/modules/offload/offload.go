package offload

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/FRIKKern/noo-noo/internal/core"
	"github.com/FRIKKern/noo-noo/internal/leaks"
	"github.com/FRIKKern/noo-noo/internal/modules"
)

// ErrDeleteForbidden is returned for any attempt to route a delete through
// this module. Offload NEVER deletes user data — the whole point is that the
// data lives on, elsewhere.
var ErrDeleteForbidden = errors.New("offload: delete is forbidden — this module only relocates")

// ErrStopGate wraps a stop-gate refusal so callers can distinguish "the
// owning app is running" (a DEFERRABLE condition — the relocation queue may
// retry at a safe moment, with consent) from every other Apply failure.
// Match with errors.Is.
var ErrStopGate = errors.New("stop-gate refused")

// Config pins the offload destination. Both fields empty (the default)
// means offload is disabled: scans still report, Plan emits nothing.
type Config struct {
	// DestRoot is the directory on the external volume that receives
	// relocated assets (one subdirectory per AssetID).
	DestRoot string
	// DestVolumeUUID is the pinned VolumeUUID of the external volume.
	// Names are not identity; the guard verifies this UUID at apply time.
	DestVolumeUUID string
}

func (c Config) configured() bool { return c.DestRoot != "" && c.DestVolumeUUID != "" }

// DestGuard verifies the offload destination (see core.VolGuard).
type DestGuard interface {
	CheckDest(ctx context.Context, destDir, pinnedUUID string) error
}

// ProcessChecker reports whether a process matching pattern is running.
// Injectable so stop-gate tests need no live processes.
type ProcessChecker interface {
	Running(ctx context.Context, pattern string) (bool, error)
}

// PgrepChecker is the production ProcessChecker backed by pgrep.
type PgrepChecker struct{}

func (PgrepChecker) Running(ctx context.Context, pattern string) (bool, error) {
	err := exec.CommandContext(ctx, "/usr/bin/pgrep", "-f", pattern).Run()
	if err == nil {
		return true, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
		return false, nil // pgrep exit 1 = no match
	}
	return false, fmt.Errorf("pgrep -f %q: %w", pattern, err)
}

// Copier copies the tree at src to dst. Injectable for failure-path tests.
type Copier func(ctx context.Context, src, dst string) error

// DittoCopy is the production Copier: /usr/bin/ditto preserves resource
// forks, xattrs, ACLs and permissions — the safe way to move app data.
func DittoCopy(ctx context.Context, src, dst string) error {
	out, err := exec.CommandContext(ctx, "/usr/bin/ditto", src, dst).CombinedOutput()
	if err != nil {
		return fmt.Errorf("ditto %s → %s: %w (output: %s)", src, dst, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// Deps are the injectable collaborators. Zero-value fields get production
// defaults in New.
type Deps struct {
	Guard DestGuard
	Procs ProcessChecker
	Copy  Copier
	Sizes SizeFns
	Home  string // user home for ~-expansion; "" → os.UserHomeDir()
	// PathProbe gates GatePath assets on open files under the target path.
	// nil → leaks.LsofProbe (fail-safe: doubt/timeout = live = blocked).
	PathProbe leaks.Prober
}

// Module implements modules.Module for playbook-driven offload.
type Module struct {
	playbooks []Playbook
	cfg       Config
	guard     DestGuard
	procs     ProcessChecker
	copy      Copier
	sizes     SizeFns
	home      string
	pathProbe leaks.Prober
}

// New constructs the offload module. playbooks nil → DefaultPlaybooks().
func New(cfg Config, playbooks []Playbook, d Deps) *Module {
	if playbooks == nil {
		playbooks = DefaultPlaybooks()
	}
	if d.Guard == nil {
		d.Guard = core.VolGuard{}
	}
	if d.Procs == nil {
		d.Procs = PgrepChecker{}
	}
	if d.Copy == nil {
		d.Copy = DittoCopy
	}
	if d.PathProbe == nil {
		d.PathProbe = leaks.LsofProbe
	}
	if d.Home == "" {
		if h, err := os.UserHomeDir(); err == nil {
			d.Home = h
		} else {
			d.Home = "."
		}
	}
	return &Module{
		playbooks: playbooks,
		cfg:       cfg,
		guard:     d.Guard,
		procs:     d.Procs,
		copy:      d.Copy,
		sizes:     d.Sizes.fill(),
		home:      d.Home,
		pathProbe: d.PathProbe,
	}
}

func (*Module) Name() string { return "offload" }

// Scan reports every playbook asset present on disk with its class, sizes,
// the exact native command where one exists, and the volume-guard verdict
// for the configured destination. It also detects the standing risk of
// unguarded external symlinks at the top of $HOME (e.g. a hand-rolled
// Desktop → /Volumes/... link with no volume-present guard) — report-only.
//
// Report.Total counts only relocate-class bytes: that is what THIS module
// can reclaim locally. Native-config assets reclaim via their app's own
// command and are surfaced as evidence, not totals.
func (m *Module) Scan(ctx context.Context) (modules.Report, error) {
	rep := modules.Report{Module: "offload"}

	verdict := "offload disabled: set [offload] dest_root and dest_volume_uuid"
	if m.cfg.configured() {
		if err := m.guard.CheckDest(ctx, m.cfg.DestRoot, m.cfg.DestVolumeUUID); err != nil {
			verdict = "guard REFUSED: " + err.Error()
		} else {
			verdict = fmt.Sprintf("guard ok: %s is volume %s and live-writable", m.cfg.DestRoot, m.cfg.DestVolumeUUID)
		}
	}

	for _, pb := range m.playbooks {
		if err := ctx.Err(); err != nil {
			return rep, err
		}
		ev := map[string]string{
			"class":         string(pb.Class),
			"asset_id":      pb.AssetID,
			"guard_verdict": verdict,
		}
		if pb.Note != "" {
			ev["note"] = pb.Note
		}

		if pb.Path == "" { // generic manual entry: informational only
			rep.Items = append(rep.Items, modules.Item{Path: "(" + pb.AssetID + ")", Evidence: ev})
			continue
		}
		p := expandPath(m.home, pb.Path)
		info, err := os.Stat(p)
		if err != nil || !info.IsDir() {
			continue
		}
		size, err := m.sizes.Blocks(p)
		if err != nil {
			ev["size_error"] = err.Error()
		}
		ev["allocated_bytes"] = strconv.FormatInt(int64(size), 10)

		switch pb.Class {
		case ClassNativeConfig:
			// Render the machine-agnostic {dest_root} template against the
			// configured destination at READ time — an unconfigured dest_root
			// yields the placeholder, and the verdict above carries the
			// "configure [offload] dest_root" guidance.
			ev["native_command"] = renderNativeCommand(pb.NativeCommand, m.cfg.DestRoot)
			ev["suggestion"] = "use the app's own relocation — noo-noo will not move this (the app would regrow the store at the old path)"
		case ClassRelocate:
			if uniq, err := m.sizes.UniqueAllocated(p); err == nil {
				ev["unique_allocated_bytes"] = strconv.FormatInt(int64(uniq), 10)
			}
			if pb.NeverDelete {
				ev["never_delete"] = "true"
			}
			rep.Total += size
		}
		if pb.pathGated() {
			ev["gate"] = "path"
		} else if pb.StopGate != "" {
			ev["gate"] = "process"
			ev["stop_gate"] = pb.StopGate
		}
		if pb.ParentAssetID != "" {
			ev["parent_asset"] = pb.ParentAssetID
		}
		rep.Items = append(rep.Items, modules.Item{Path: p, Size: size, Evidence: ev})
	}

	m.scanExternalSymlinkRisks(&rep)
	return rep, nil
}

// scanExternalSymlinkRisks flags top-level $HOME entries that are symlinks
// resolving under /Volumes/ — data silently living on an external drive with
// no volume-present guard (an app writing while the drive is absent lands in
// a phantom directory on the internal disk). Report-only.
func (m *Module) scanExternalSymlinkRisks(rep *modules.Report) {
	entries, err := os.ReadDir(m.home)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.Type()&os.ModeSymlink == 0 {
			continue
		}
		p := filepath.Join(m.home, e.Name())
		target, err := os.Readlink(p)
		if err != nil {
			continue
		}
		if !filepath.IsAbs(target) {
			target = filepath.Join(m.home, target)
		}
		target = filepath.Clean(target)
		if !strings.HasPrefix(target, "/Volumes/") {
			continue
		}
		rep.Items = append(rep.Items, modules.Item{
			Path: p,
			Evidence: map[string]string{
				"class":  "unguarded-external-symlink",
				"target": target,
				"risk":   "no volume-present guard: writes while the volume is unmounted land on the internal disk as a phantom directory (report-only)",
			},
		})
	}
}

// Plan proposes relocate actions for relocate-class assets. Native-config
// and manual assets never yield actions (their evidence IS the suggestion),
// and no input can make Plan emit a delete — the only op this module knows
// is "relocate".
func (m *Module) Plan(r modules.Report) []modules.Action {
	if !m.cfg.configured() {
		return nil
	}
	byPath := make(map[string]Playbook, len(m.playbooks))
	for _, pb := range m.playbooks {
		if pb.Path != "" {
			byPath[expandPath(m.home, pb.Path)] = pb
		}
	}
	var out []modules.Action
	for _, it := range r.Items {
		pb, ok := byPath[it.Path]
		if !ok || pb.Class != ClassRelocate {
			continue
		}
		out = append(out, modules.Action{
			Module:      "offload",
			Op:          "relocate",
			Target:      it.Path,
			Destination: filepath.Join(m.cfg.DestRoot, pb.AssetID),
			Size:        it.Size,
			Risk:        modules.RiskMedium,
		})
	}
	return out
}

// Apply executes one relocate: guard → stop-gate → copy → verify → swap to a
// .noo-noo-bak → symlink → verify the symlink resolves → only then drop the
// bak. Any failure before the symlink is verified restores the original
// path intact.
func (m *Module) Apply(ctx context.Context, a modules.Action) (modules.Result, error) {
	res := modules.Result{Action: a}
	fail := func(err error) (modules.Result, error) {
		res.Err = err
		return res, err
	}

	switch a.Op {
	case "relocate":
		// proceed
	case "delete":
		return fail(fmt.Errorf("%w (target %q)", ErrDeleteForbidden, a.Target))
	default:
		return fail(fmt.Errorf("offload: unsupported op %q", a.Op))
	}

	pb, ok := m.playbookForTarget(a.Target)
	if !ok {
		return fail(fmt.Errorf("offload: %q is not a known playbook asset", a.Target))
	}
	if pb.Class != ClassRelocate {
		return fail(fmt.Errorf("offload: asset %q is class %q — use its native mechanism, not a file move", pb.AssetID, pb.Class))
	}

	// Volume guard AT APPLY: config may have been written days ago; the
	// drive present now must be the pinned one and live-writable.
	if err := m.guard.CheckDest(ctx, m.cfg.DestRoot, m.cfg.DestVolumeUUID); err != nil {
		return fail(fmt.Errorf("offload: destination refused: %w", err))
	}

	if pb.pathGated() {
		// Path-gate: probe the target subtree for open files instead of a
		// named process. Fail-safe — the prober reports live=true whenever
		// emptiness cannot be proven (missing lsof, timeout, error). Wrapped
		// in ErrStopGate: "something still holds it open" is exactly as
		// deferrable as a running stop-gate process — the queue re-checks
		// the same gate fresh at the next safe moment.
		if live, proof := m.pathProbe(ctx, a.Target); live {
			return fail(fmt.Errorf("offload: %w: %q is in use — %s (close what holds it open and retry, or queue with --defer)", ErrStopGate, a.Target, proof))
		}
	} else if pb.StopGate != "" {
		running, err := m.procs.Running(ctx, pb.StopGate)
		if err != nil {
			return fail(fmt.Errorf("offload: stop-gate check for %q failed: %w", pb.StopGate, err))
		}
		if running {
			return fail(fmt.Errorf("offload: %w: %q is running — stop it and retry (or queue with --defer)", ErrStopGate, pb.StopGate))
		}
	}

	dest := a.Destination
	if rel, err := filepath.Rel(m.cfg.DestRoot, dest); err != nil || strings.HasPrefix(rel, "..") {
		return fail(fmt.Errorf("offload: destination %q escapes dest_root %q", dest, m.cfg.DestRoot))
	}
	if _, err := os.Lstat(dest); err == nil {
		return fail(fmt.Errorf("offload: destination %q already exists — refusing to overwrite", dest))
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return fail(fmt.Errorf("offload: mkdir %q: %w", filepath.Dir(dest), err))
	}

	// Copy. Source is untouched until the copy is verified.
	if err := m.copy(ctx, a.Target, dest); err != nil {
		_ = os.RemoveAll(dest) // drop the partial copy
		return fail(fmt.Errorf("offload: copy failed, source untouched: %w", err))
	}

	srcN, srcB, err := countAndBytes(a.Target)
	if err != nil {
		_ = os.RemoveAll(dest)
		return fail(fmt.Errorf("offload: verify walk of source failed: %w", err))
	}
	dstN, dstB, err := countAndBytes(dest)
	if err != nil {
		_ = os.RemoveAll(dest)
		return fail(fmt.Errorf("offload: verify walk of copy failed: %w", err))
	}
	if srcN != dstN || srcB != dstB {
		_ = os.RemoveAll(dest)
		return fail(fmt.Errorf("offload: copy verify FAILED (source %d files/%d bytes, copy %d files/%d bytes) — source untouched",
			srcN, int64(srcB), dstN, int64(dstB)))
	}

	// Estimate local reclaim before the tree moves aside: the clone-aware
	// unique-allocated bytes of the source (what dropping the local copy
	// can at most free — extents referenced from outside it stay allocated).
	freed, _ := m.sizes.FreedByDelete(a.Target)

	bak := a.Target + ".noo-noo-bak"
	if _, err := os.Lstat(bak); err == nil {
		_ = os.RemoveAll(dest)
		return fail(fmt.Errorf("offload: leftover backup %q exists — resolve it first", bak))
	}
	if err := os.Rename(a.Target, bak); err != nil {
		_ = os.RemoveAll(dest)
		return fail(fmt.Errorf("offload: swap rename failed, source untouched: %w", err))
	}

	restore := func(cause error) (modules.Result, error) {
		_ = os.Remove(a.Target) // drop a half-made symlink if any
		if rerr := os.Rename(bak, a.Target); rerr != nil {
			return fail(fmt.Errorf("offload: %v; RESTORE ALSO FAILED (%v) — original data is at %q", cause, rerr, bak))
		}
		_ = os.RemoveAll(dest)
		return fail(fmt.Errorf("offload: %w — original restored", cause))
	}

	if err := os.Symlink(dest, a.Target); err != nil {
		return restore(fmt.Errorf("symlink %q → %q failed: %w", a.Target, dest, err))
	}
	resolvedLink, lerr := filepath.EvalSymlinks(a.Target)
	resolvedDest, derr := filepath.EvalSymlinks(dest)
	if lerr != nil || derr != nil || resolvedLink != resolvedDest {
		return restore(fmt.Errorf("symlink verify failed (link → %q err %v, dest %q err %v)", resolvedLink, lerr, resolvedDest, derr))
	}

	// Only now is the bak safe to drop.
	if err := os.RemoveAll(bak); err != nil {
		// The relocation itself succeeded; report the stale bak honestly.
		res.Err = fmt.Errorf("offload: relocated, but removing backup %q failed: %w — remove it manually", bak, err)
		return res, res.Err
	}
	res.BytesFreed = freed
	return res, nil
}

func (m *Module) playbookForTarget(target string) (Playbook, bool) {
	clean := filepath.Clean(target)
	for _, pb := range m.playbooks {
		if pb.Path != "" && filepath.Clean(expandPath(m.home, pb.Path)) == clean {
			return pb, true
		}
	}
	return Playbook{}, false
}
