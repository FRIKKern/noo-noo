package procsig

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"syscall"
	"time"

	"github.com/FRIKKern/noo-noo/internal/core"
	"github.com/FRIKKern/noo-noo/internal/modules"
)

// psArgs is the single process-table read this module ever performs, both
// at Scan and at Apply-time re-verification.
var psArgs = []string{"-axo", "pid,ppid,uid,etime,command"}

// Module scans the process table for registry matches that are ORPHANS
// (reparented to launchd, ppid 1), plans terminate actions for them, and
// applies SIGTERM→SIGKILL behind its own safety predicate.
//
// The safety predicate is procsig's OWN (charter D20) — core.Safety and
// CanDeleteLeakTarget are path predicates and are deliberately not involved:
//   - only processes owned by the scanning uid are ever considered;
//   - pid must be > 1 and never this process itself;
//   - identity is RE-VERIFIED at apply time (fresh ps read: still running,
//     still ours, still ppid 1, cmdline still matches a registered
//     signature) so a recycled pid is refused, never signaled (TOCTOU guard).
type Module struct {
	sigs   []ProcessSignature
	runner core.OutputRunner // injectable ps; nil never happens via New
	uid    int
	self   int // our own pid — never a signal target
	// kill is the signal seam: syscall.Kill in production, injectable so
	// refusal tests can prove no signal was ever sent.
	kill func(pid int, sig syscall.Signal) error
	// termWait bounds the post-SIGTERM grace before escalating to SIGKILL;
	// killWait bounds the post-SIGKILL wait before giving up.
	termWait  time.Duration
	killWait  time.Duration
	pollEvery time.Duration
}

// New constructs a Module over a signature registry with production
// defaults: real ps, real signals, the current uid.
func New(sigs []ProcessSignature) *Module {
	return &Module{
		sigs:      sigs,
		runner:    core.ExecOutputRunner{},
		uid:       os.Getuid(),
		self:      os.Getpid(),
		kill:      syscall.Kill,
		termWait:  5 * time.Second,
		killWait:  2 * time.Second,
		pollEvery: 100 * time.Millisecond,
	}
}

func (*Module) Name() string { return "orphans" }

// Scan reads the process table once and reports every registry match that
// is a true orphan: owned by our uid, ppid 1 (parent job gone, reparented
// to launchd), cmdline matching a signature with a scratch-dir profile.
// A headless browser with a LIVE parent is a running job, not an orphan,
// and is never reported. Size is always 0: terminating frees pins and RAM,
// not bytes — the bytes come from the leaks sweep that follows.
func (m *Module) Scan(ctx context.Context) (modules.Report, error) {
	rep := modules.Report{Module: "orphans"}
	procs, err := m.listProcesses(ctx)
	if err != nil {
		return rep, err
	}
	for _, p := range procs {
		if err := ctx.Err(); err != nil {
			return rep, err
		}
		if p.PID <= 1 || p.PID == m.self || p.UID != m.uid || p.PPID != 1 {
			continue
		}
		sig, dir, ok := m.matchAny(p)
		if !ok {
			continue
		}
		rep.Items = append(rep.Items, modules.Item{
			Path: dir, // the profile dir is the path-shaped face of the hit
			Size: 0,
			Evidence: map[string]string{
				"pid":        strconv.Itoa(p.PID),
				"ppid":       strconv.Itoa(p.PPID),
				"etime":      p.Etime,
				"signature":  sig.ID,
				"title":      sig.Title,
				"cmdline":    p.Command,
				"workaround": sig.Workaround,
			},
		})
	}
	return rep, nil
}

// Plan emits one terminate action per orphan. Target carries the decimal
// PID — Apply re-establishes everything else from a fresh process-table
// read, so nothing else from Scan is trusted.
func (m *Module) Plan(r modules.Report) []modules.Action {
	var out []modules.Action
	for _, it := range r.Items {
		pid := it.Evidence["pid"]
		if pid == "" {
			continue
		}
		risk := modules.RiskLow
		if sig, ok := m.signatureByID(it.Evidence["signature"]); ok {
			risk = sig.Risk
		}
		out = append(out, modules.Action{
			Module: "orphans",
			Op:     "terminate",
			Target: pid,
			Size:   0,
			Risk:   risk,
		})
	}
	return out
}

// Apply terminates one orphan: SIGTERM, a bounded grace, then SIGKILL.
// Identity is RE-VERIFIED here against a fresh ps read — a pid that
// exited, was recycled, regained a parent, changed cmdline, or belongs to
// another user is refused, never signaled.
func (m *Module) Apply(ctx context.Context, a modules.Action) (modules.Result, error) {
	res := modules.Result{Action: a}
	fail := func(err error) (modules.Result, error) {
		res.Err = err
		return res, err
	}
	if a.Op != "terminate" {
		return fail(fmt.Errorf("orphans: unsupported op %q", a.Op))
	}
	pid, err := strconv.Atoi(a.Target)
	if err != nil {
		return fail(fmt.Errorf("orphans: target %q is not a pid", a.Target))
	}
	if pid <= 1 {
		return fail(fmt.Errorf("orphans: never signal pid %d", pid))
	}
	if pid == m.self {
		return fail(errors.New("orphans: refusing to signal our own process"))
	}

	// TOCTOU guard: never trust identity carried over from Scan/Plan.
	procs, err := m.listProcesses(ctx)
	if err != nil {
		return fail(err)
	}
	p, found := findPID(procs, pid)
	if !found {
		return fail(fmt.Errorf("orphans: pid %d is not running (already exited?) — nothing to signal", pid))
	}
	if p.UID != m.uid {
		return fail(fmt.Errorf("orphans: pid %d is owned by uid %d, not %d — refusing", pid, p.UID, m.uid))
	}
	if p.PPID != 1 {
		return fail(fmt.Errorf("orphans: pid %d has a live parent (ppid %d) — no longer an orphan, refusing", pid, p.PPID))
	}
	if _, _, ok := m.matchAny(p); !ok {
		return fail(fmt.Errorf("orphans: pid %d cmdline no longer matches any registered signature (pid recycled?) — refusing: %q", pid, p.Command))
	}

	if err := m.kill(pid, syscall.SIGTERM); err != nil {
		if errors.Is(err, syscall.ESRCH) {
			return res, nil // exited between verify and signal — goal achieved
		}
		return fail(fmt.Errorf("orphans: SIGTERM pid %d: %w", pid, err))
	}
	if m.waitGone(ctx, pid, m.termWait) {
		return res, nil
	}
	if err := m.kill(pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
		return fail(fmt.Errorf("orphans: SIGKILL pid %d: %w", pid, err))
	}
	if m.waitGone(ctx, pid, m.killWait) {
		return res, nil
	}
	return fail(fmt.Errorf("orphans: pid %d survived SIGTERM and SIGKILL", pid))
}

// listProcesses reads the process table through the injectable runner.
func (m *Module) listProcesses(ctx context.Context) ([]Process, error) {
	out, err := m.runner.Output(ctx, nil, "ps", psArgs...)
	if err != nil {
		return nil, fmt.Errorf("orphans: ps: %w", err)
	}
	return ParsePS(string(out)), nil
}

// matchAny returns the first registry signature matching proc.
func (m *Module) matchAny(proc Process) (ProcessSignature, string, bool) {
	for _, sig := range m.sigs {
		if dir, ok := sig.Match(proc); ok {
			return sig, dir, true
		}
	}
	return ProcessSignature{}, "", false
}

func (m *Module) signatureByID(id string) (ProcessSignature, bool) {
	for _, sig := range m.sigs {
		if sig.ID == id {
			return sig, true
		}
	}
	return ProcessSignature{}, false
}

// waitGone polls (signal 0) until the pid disappears or the budget runs
// out. Orphans are children of launchd, which reaps immediately, so ESRCH
// is a reliable "gone" signal here.
func (m *Module) waitGone(ctx context.Context, pid int, budget time.Duration) bool {
	deadline := time.Now().Add(budget)
	for {
		if err := m.kill(pid, 0); errors.Is(err, syscall.ESRCH) {
			return true
		}
		if ctx.Err() != nil || time.Now().After(deadline) {
			return false
		}
		time.Sleep(m.pollEvery)
	}
}

func findPID(procs []Process, pid int) (Process, bool) {
	for _, p := range procs {
		if p.PID == pid {
			return p, true
		}
	}
	return Process{}, false
}

// Compile-time check: *Module satisfies the modules contract.
var _ modules.Module = (*Module)(nil)
