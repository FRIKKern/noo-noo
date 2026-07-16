package offload

// Session-safe deferred relocation (wave 2, charter D24). An `offload apply`
// refused by a stop-gate — the ~/.claude case: you cannot relocate the config
// dir of the app you are running — is not a dead end: with EXPLICIT consent
// (prompt or --defer) the action is queued, and re-checked later at a safe
// moment by `offload run-pending` or the daemon's gated daily tick.
//
// Law: queueing never weakens a single gate. Every re-check runs the FULL
// fresh gate set — dest-pin match, playbook membership, stop-gate, volume
// guard, destination collision — and then the normal Apply, which re-checks
// everything again itself (TOCTOU defense in depth). A still-blocked entry is
// marked 'blocked' with the exact reason and its source is untouched (Apply's
// restore law). Only Apply invocations consume the caller's budget.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/FRIKKern/noo-noo/internal/core"
	"github.com/FRIKKern/noo-noo/internal/modules"
	"github.com/FRIKKern/noo-noo/internal/store"
)

// Pending-run triggers. Mirrors autoclean's errPressureTrigger law: pressure
// events correlate with active dev work — the moment a sustained-high
// trigger fires is exactly the moment we must NOT start moving directories
// out from under running tools.
const (
	// TriggerPendingDaily is the daemon's gated daily tick.
	TriggerPendingDaily = "daily"
	// TriggerPendingManual is the operator's `noo-noo offload run-pending`.
	TriggerPendingManual = "manual"
)

// ErrPendingTriggerForbidden is returned by RunPending for any trigger other
// than 'daily' or 'manual' — pressure-driven relocations are forbidden by
// design, before a single queue row is even read.
var ErrPendingTriggerForbidden = errors.New(
	"offload: pending relocations apply only on the 'daily' or 'manual' trigger (pressure-driven moves are forbidden)")

// The canonical row types live in internal/store (which owns the DDL); these
// aliases keep offload call sites readable while letting *store.Store satisfy
// QueueStore directly — the same pattern autoclean uses for its event rows.
type (
	RelocationQueueEntry = store.RelocationQueueEntry
	RelocationResolution = store.RelocationResolution
)

// QueueStore is the narrow relocation-queue surface this module needs.
// *store.Store satisfies it; tests may use a real temp-dir store.
type QueueStore interface {
	EnqueueRelocation(e RelocationQueueEntry) (int64, error)
	ListPendingRelocations() ([]RelocationQueueEntry, error)
	MarkRelocationAttempt(id int64, atUnix int64) error
	ResolveRelocation(id int64, r RelocationResolution) error
}

// PlaybookFor exposes the playbook backing a target path, so callers can
// name the stop-gate in consent prompts ("queue for when %q is not
// running?") and surface per-asset facts alongside queue rows.
func (m *Module) PlaybookFor(target string) (Playbook, bool) {
	return m.playbookForTarget(target)
}

// EnqueueDeferred queues a relocate action for a later safe moment. It
// refuses anything Apply itself would refuse structurally (unknown asset,
// non-relocate class, unconfigured offload): consent to defer is consent to
// the SAME action, so only actions that were legal to attempt are legal to
// queue. The destination pin is captured at queue time — what the user
// consented to is what a re-check will verify against.
func (m *Module) EnqueueDeferred(qs QueueStore, a modules.Action, gateReason string, now time.Time) (int64, error) {
	if !m.cfg.configured() {
		return 0, fmt.Errorf("offload: cannot queue %q — offload is not configured", a.Target)
	}
	pb, ok := m.playbookForTarget(a.Target)
	if !ok {
		return 0, fmt.Errorf("offload: cannot queue %q — not a known playbook asset", a.Target)
	}
	if pb.Class != ClassRelocate {
		return 0, fmt.Errorf("offload: cannot queue %q — asset %q is class %q, not relocate", a.Target, pb.AssetID, pb.Class)
	}
	return qs.EnqueueRelocation(RelocationQueueEntry{
		QueuedAtUnix:    now.Unix(),
		TargetPath:      filepath.Clean(a.Target),
		PlaybookAssetID: pb.AssetID,
		DestRoot:        m.cfg.DestRoot,
		DestVolumeUUID:  m.cfg.DestVolumeUUID,
		GateReason:      gateReason,
	})
}

// Pending re-check outcomes (PendingResult.Outcome).
const (
	// PendingApplied: every gate passed and Apply completed; row terminal.
	PendingApplied = store.RelocApplied
	// PendingBlocked: a fresh gate (or Apply itself) refused; row stays
	// pending with the reason recorded, source untouched.
	PendingBlocked = store.RelocBlocked
	// PendingSkippedBudget: gates were not even consulted — the caller's
	// apply budget was already spent. Row untouched.
	PendingSkippedBudget = "skipped_budget"
)

// PendingResult reports one entry's re-check outcome.
type PendingResult struct {
	Entry   RelocationQueueEntry
	Outcome string
	// Reason carries the blocked reason (or the budget note).
	Reason string
	// Freed is the local bytes reclaimed when Outcome == PendingApplied.
	Freed core.Bytes
	// Attempted is true when Apply was actually invoked (audit callers
	// append their JSONL entries only for real apply attempts).
	Attempted bool
	// Err is the underlying Apply error for an attempted-but-blocked entry.
	Err error
}

// RunPending re-checks every pending queue entry fresh and applies the ones
// whose gates now pass. trigger must be TriggerPendingDaily (the daemon) or
// TriggerPendingManual (the CLI) — anything else, notably a pressure tick,
// is refused outright with ErrPendingTriggerForbidden before any row is
// read. budget caps Apply INVOCATIONS (the daemon passes 1; budget < 0
// means unlimited — the CLI's interactive run). Entries beyond the budget
// are left exactly as they were. Never returns a partial error: per-entry
// failures land in the entry's own PendingResult.
func (m *Module) RunPending(ctx context.Context, qs QueueStore, trigger string, budget int, now func() time.Time) ([]PendingResult, error) {
	switch trigger {
	case TriggerPendingDaily, TriggerPendingManual:
	default:
		return nil, ErrPendingTriggerForbidden
	}
	if now == nil {
		now = time.Now
	}
	entries, err := qs.ListPendingRelocations()
	if err != nil {
		return nil, fmt.Errorf("offload: list pending relocations: %w", err)
	}
	var out []PendingResult
	attempted := 0
	for _, e := range entries {
		if err := ctx.Err(); err != nil {
			return out, err
		}
		res := m.recheckOne(ctx, qs, e, budget, &attempted, now)
		out = append(out, res)
	}
	return out, nil
}

// recheckOne runs the full fresh gate set for one entry and, if every gate
// passes and budget remains, the two-phase apply (attempt mark -> Apply ->
// outcome patch).
func (m *Module) recheckOne(ctx context.Context, qs QueueStore, e RelocationQueueEntry, budget int, attempted *int, now func() time.Time) PendingResult {
	block := func(reason string, err error) PendingResult {
		if rerr := qs.ResolveRelocation(e.ID, RelocationResolution{
			Status:        store.RelocBlocked,
			BlockedReason: reason,
		}); rerr != nil {
			// The row raced to terminal (e.g. cancelled mid-run); report the
			// original reason, but carry the resolve error too.
			err = fmt.Errorf("%s (resolve: %v)", reason, rerr)
		}
		return PendingResult{Entry: e, Outcome: PendingBlocked, Reason: reason, Err: err}
	}

	// Gate 0: the destination pin the user consented to must still be the
	// configured one. A changed [offload] needs fresh consent, never a
	// silent redirect of a queued move.
	if e.DestRoot != m.cfg.DestRoot || e.DestVolumeUUID != m.cfg.DestVolumeUUID {
		return block(fmt.Sprintf("offload destination changed since queued (queued for %s on %s) — cancel and re-queue under the new config", e.DestRoot, e.DestVolumeUUID), nil)
	}

	// Gate 1: still a known relocate-class playbook asset.
	pb, ok := m.playbookForTarget(e.TargetPath)
	if !ok {
		return block(fmt.Sprintf("%q is no longer a known playbook asset", e.TargetPath), nil)
	}
	if pb.Class != ClassRelocate {
		return block(fmt.Sprintf("asset %q is now class %q — not relocatable by file move", pb.AssetID, pb.Class), nil)
	}

	// Gate 2: the source must still be a real directory (not already a
	// symlink from a manual relocation, not deleted).
	if fi, err := os.Lstat(e.TargetPath); err != nil || !fi.IsDir() {
		return block(fmt.Sprintf("source %q is missing or no longer a real directory", e.TargetPath), err)
	}

	// Gate 3: fresh stop-gate — the reason the entry was queued.
	if pb.StopGate != "" {
		running, err := m.procs.Running(ctx, pb.StopGate)
		if err != nil {
			return block(fmt.Sprintf("stop-gate check for %q failed: %v", pb.StopGate, err), err)
		}
		if running {
			return block(fmt.Sprintf("stop-gate: %q still running", pb.StopGate), nil)
		}
	}

	// Gate 4: fresh volume guard against the QUEUED pin (== current config
	// per gate 0): UUID identity + live write-probe.
	if err := m.guard.CheckDest(ctx, e.DestRoot, e.DestVolumeUUID); err != nil {
		return block(fmt.Sprintf("destination refused: %v", err), err)
	}

	// Gate 5: destination collision.
	dest := filepath.Join(e.DestRoot, e.PlaybookAssetID)
	if _, err := os.Lstat(dest); err == nil {
		return block(fmt.Sprintf("destination %q already exists — refusing to overwrite", dest), nil)
	}

	// Budget: gates pass, but the caller's apply allowance is spent. The row
	// is left exactly as it was (still pending, no attempt burned).
	if budget >= 0 && *attempted >= budget {
		return PendingResult{Entry: e, Outcome: PendingSkippedBudget,
			Reason: "apply budget for this run is spent — left pending"}
	}

	// Two-phase apply. Phase 1: mark the attempt (crash pivot) BEFORE any
	// bytes move.
	*attempted++
	if err := qs.MarkRelocationAttempt(e.ID, now().Unix()); err != nil {
		return PendingResult{Entry: e, Outcome: PendingBlocked,
			Reason: "could not mark attempt (row raced to terminal?)", Err: err}
	}
	res, err := m.Apply(ctx, modules.Action{
		Module:      "offload",
		Op:          "relocate",
		Target:      e.TargetPath,
		Destination: dest,
		Risk:        modules.RiskMedium,
	})
	// Phase 2: patch the outcome.
	if err != nil {
		out := block(fmt.Sprintf("apply failed: %v", err), err)
		out.Attempted = true
		return out
	}
	if rerr := qs.ResolveRelocation(e.ID, RelocationResolution{
		Status:         store.RelocApplied,
		FreedBytes:     int64(res.BytesFreed),
		ResolvedAtUnix: now().Unix(),
	}); rerr != nil {
		// The relocation itself succeeded; report the bookkeeping failure
		// honestly rather than pretending the whole thing failed.
		return PendingResult{Entry: e, Outcome: PendingApplied, Freed: res.BytesFreed,
			Attempted: true, Err: fmt.Errorf("relocated, but updating the queue row failed: %w", rerr)}
	}
	return PendingResult{Entry: e, Outcome: PendingApplied, Freed: res.BytesFreed, Attempted: true}
}
