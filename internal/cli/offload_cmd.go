package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/FRIKKern/noo-noo/internal/audit"
	"github.com/FRIKKern/noo-noo/internal/config"
	"github.com/FRIKKern/noo-noo/internal/modules"
	"github.com/FRIKKern/noo-noo/internal/modules/offload"
	"github.com/FRIKKern/noo-noo/internal/store"
)

func init() { Register("offload", offloadCmd) }

// defaultConfigPath is the same file the daemon and the menubar app read.
func defaultConfigPath() string {
	return filepath.Join(homeDir(), ".config", "noo-noo", "config.toml")
}

// offloadSetup builds the offload module from the user config and returns
// both (the queue verbs also need cfg.Daemon.StorePath). Package-level so
// tests can inject a module with fake deps. Diagnose-by-default: an
// unconfigured [offload] section still scans and reports — it just cannot
// plan, apply, or queue anything.
var offloadSetup = func() (config.Config, *offload.Module) {
	cfg, err := config.Load(defaultConfigPath())
	if err != nil {
		cfg = config.Defaults()
	}
	return cfg, offload.New(offload.Config{
		DestRoot:       cfg.Offload.DestRoot,
		DestVolumeUUID: cfg.Offload.DestVolumeUUID,
	}, nil, offload.Deps{})
}

// offloadQueueStore opens the relocation queue (the daemon's SQLite store;
// WAL mode makes the cross-process access safe). Injectable for tests.
var offloadQueueStore = func(cfg config.Config) (*store.Store, error) {
	return store.Open(cfg.Daemon.StorePath)
}

// offloadStdin feeds the queue-consent prompt; tests inject answers.
var offloadStdin io.Reader = os.Stdin

const offloadUsage = "Usage: noo-noo offload <scan|plan|apply|pending|run-pending|cancel> [flags]"

func offloadCmd(ctx context.Context, app *App, args []string) int {
	// D22: parseVerb strips the verb before flag.Parse — flags come after the
	// verb (`offload apply --defer -y`) or before it (legacy), never silently
	// into fs.Args(); a leftover flag-like token hard-errors.
	fs := flag.NewFlagSet("offload", flag.ContinueOnError)
	fs.SetOutput(app.Err)
	asJSON := fs.Bool("json", false, "output NDJSON")
	yes := fs.Bool("y", false, "skip confirmation")
	dryRun := fs.Bool("dry-run", false, "show what would happen")
	deferQ := fs.Bool("defer", false,
		"consent to queue a stop-gated relocation for a later safe moment")
	verb, code, ok := parseVerb(app, fs, args, offloadUsage)
	if !ok {
		return code
	}

	cfg, m := offloadSetup()
	switch verb {
	case "scan", "plan", "apply":
		return offloadScanPlanApply(ctx, app, cfg, m, verb, offloadFlags{
			asJSON: *asJSON, yes: *yes, dryRun: *dryRun, deferQ: *deferQ,
		})
	case "pending":
		return offloadPending(app, cfg, *asJSON)
	case "run-pending":
		return offloadRunPending(ctx, app, cfg, m, *dryRun)
	case "cancel":
		return offloadCancel(app, cfg, fs.Args())
	default:
		_, _ = fmt.Fprintf(app.Err, "unknown offload subcommand %q\n%s\n", verb, offloadUsage)
		return 2
	}
}

type offloadFlags struct {
	asJSON, yes, dryRun, deferQ bool
}

func offloadScanPlanApply(ctx context.Context, app *App, cfg config.Config, m *offload.Module, verb string, f offloadFlags) int {
	rep, err := m.Scan(ctx)
	if err != nil {
		_, _ = fmt.Fprintln(app.Err, "scan:", err)
		return 1
	}
	switch verb {
	case "scan":
		_ = PrintReport(app.Out, rep, f.asJSON)
		if !f.asJSON {
			printOffloadEvidence(app, rep.Items)
		}
		return 0
	case "plan":
		actions := m.Plan(rep)
		if len(actions) == 0 {
			_, _ = fmt.Fprintln(app.Out, "Nothing to relocate. Native-config assets move via their own commands — run `noo-noo offload scan` for the exact commands.")
			return 0
		}
		for _, a := range actions {
			_, _ = fmt.Fprintf(app.Out, "would relocate: %s → %s (frees %s locally)\n", a.Target, a.Destination, a.Size)
		}
		return 0
	default: // apply
		actions := m.Plan(rep)
		if len(actions) == 0 {
			_, _ = fmt.Fprintln(app.Out, "Nothing to relocate. Configure [offload] dest_root + dest_volume_uuid, or use the native commands from `noo-noo offload scan`.")
			return 0
		}
		for _, a := range actions {
			_, _ = fmt.Fprintf(app.Out, "relocate: %s → %s (frees %s locally)\n", a.Target, a.Destination, a.Size)
		}
		if !Confirm(offloadStdin, app.Out,
			fmt.Sprintf("Relocate %d asset(s)? Data is copied, verified, then symlinked — nothing is deleted until the copy is proven.", len(actions)),
			f.yes) {
			_, _ = fmt.Fprintln(app.Out, "Aborted.")
			return 0
		}
		log, _ := audit.New(auditDir())
		defer func() { _ = log.Close() }()
		exit := 0
		for _, a := range actions {
			if f.dryRun {
				_, _ = fmt.Fprintf(app.Out, "would relocate: %s → %s\n", a.Target, a.Destination)
				continue
			}
			res, err := m.Apply(ctx, a)
			outcome, errStr := outcomeOf(err)
			_ = log.Write(audit.Record{
				Module: "offload", Op: a.Op, Target: a.Target,
				Size: int64(res.BytesFreed), Outcome: outcome, Error: errStr,
				Evidence: map[string]string{"destination": a.Destination},
			})
			switch {
			case err == nil:
				_, _ = fmt.Fprintf(app.Out, "relocated: %s → %s (freed %s locally)\n", a.Target, a.Destination, res.BytesFreed)
			case errors.Is(err, offload.ErrStopGate):
				_, _ = fmt.Fprintf(app.Err, "deferred-able: %s — %v\n", a.Target, err)
				if !offloadOfferQueue(app, cfg, m, a, err.Error(), f.deferQ) {
					exit = 1
				}
			default:
				_, _ = fmt.Fprintf(app.Err, "failed: %s — %v\n", a.Target, err)
				exit = 1
			}
		}
		return exit
	}
}

// offloadOfferQueue asks for (or, with --defer, already has) EXPLICIT consent
// to queue a stop-gated relocation, then enqueues it. Queueing is consent to
// the SAME action later — it never weakens a gate; every re-check runs the
// full fresh gate set. Returns true when the action ended in a queued row.
func offloadOfferQueue(app *App, cfg config.Config, m *offload.Module, a modules.Action, gateReason string, consented bool) bool {
	gateName := "the blocking app"
	if pb, ok := m.PlaybookFor(a.Target); ok && pb.StopGate != "" {
		gateName = fmt.Sprintf("%q", pb.StopGate)
	}
	if consented {
		_, _ = fmt.Fprintln(app.Out, "(--defer specified: queueing for a later safe moment)")
	} else if !Confirm(offloadStdin, app.Out,
		fmt.Sprintf("Queue %s for automatic retry once %s is no longer running?", a.Target, gateName), false) {
		_, _ = fmt.Fprintf(app.Out, "Not queued. Re-run `noo-noo offload apply` after quitting %s, or pass --defer to queue.\n", gateName)
		return false
	}
	qs, err := offloadQueueStore(cfg)
	if err != nil {
		_, _ = fmt.Fprintln(app.Err, "open queue store:", err)
		return false
	}
	defer func() { _ = qs.Close() }()
	id, err := m.EnqueueDeferred(qs, a, gateReason, time.Now())
	if err != nil {
		_, _ = fmt.Fprintln(app.Err, "queue:", err)
		return false
	}
	_, _ = fmt.Fprintf(app.Out, "queued (id %d): %s → %s\n", id, a.Target, a.Destination)
	_, _ = fmt.Fprintf(app.Out, "  run `noo-noo offload run-pending` after quitting %s,\n", gateName)
	_, _ = fmt.Fprintln(app.Out, "  or let the daemon retry on its daily tick by setting BOTH in config.toml:")
	_, _ = fmt.Fprintln(app.Out, "    [offload]")
	_, _ = fmt.Fprintln(app.Out, "    auto_apply_pending = true")
	_, _ = fmt.Fprintf(app.Out, "    risk_acknowledged_at = %q\n", time.Now().UTC().Format(time.RFC3339))
	return true
}

// offloadPending lists the queue: every not-yet-terminal entry with its age,
// the gate that deferred it, and the latest re-check verdict.
func offloadPending(app *App, cfg config.Config, asJSON bool) int {
	qs, err := offloadQueueStore(cfg)
	if err != nil {
		_, _ = fmt.Fprintln(app.Err, "open queue store:", err)
		return 1
	}
	defer func() { _ = qs.Close() }()
	entries, err := qs.ListPendingRelocations()
	if err != nil {
		_, _ = fmt.Fprintln(app.Err, "list pending:", err)
		return 1
	}
	if asJSON {
		enc := json.NewEncoder(app.Out)
		for _, e := range entries {
			_ = enc.Encode(e)
		}
		return 0
	}
	if len(entries) == 0 {
		_, _ = fmt.Fprintln(app.Out, "No pending relocations. A stop-gated `offload apply` offers queueing (or pass --defer).")
		return 0
	}
	now := time.Now()
	for _, e := range entries {
		age := humanAge(now.Sub(time.Unix(e.QueuedAtUnix, 0)))
		_, _ = fmt.Fprintf(app.Out, "#%d  %s  [%s, queued %s ago]\n", e.ID, e.TargetPath, e.Status, age)
		_, _ = fmt.Fprintf(app.Out, "    deferred because: %s\n", e.GateReason)
		if e.Status == store.RelocBlocked && e.BlockedReason != "" {
			_, _ = fmt.Fprintf(app.Out, "    last re-check: %s (attempts: %d)\n", e.BlockedReason, e.Attempts)
		}
		_, _ = fmt.Fprintf(app.Out, "    destination: %s\n", filepath.Join(e.DestRoot, e.PlaybookAssetID))
	}
	_, _ = fmt.Fprintln(app.Out, "\nRe-check now: `noo-noo offload run-pending` · withdraw: `noo-noo offload cancel <id>`")
	return 0
}

// offloadRunPending re-checks EVERY pending entry fresh (stop gate, volume
// guard, destination collision) and applies the ones whose gates now pass.
// Still-blocked entries are marked with the exact reason; sources stay
// untouched. Unlimited budget: the operator asked for this run.
func offloadRunPending(ctx context.Context, app *App, cfg config.Config, m *offload.Module, dryRun bool) int {
	qs, err := offloadQueueStore(cfg)
	if err != nil {
		_, _ = fmt.Fprintln(app.Err, "open queue store:", err)
		return 1
	}
	defer func() { _ = qs.Close() }()
	if dryRun {
		// The honest dry view of a re-check is the pending list itself.
		_, _ = fmt.Fprintln(app.Out, "(dry-run: showing what would be re-checked)")
		return offloadPending(app, cfg, false)
	}
	results, err := m.RunPending(ctx, qs, offload.TriggerPendingManual, -1, nil)
	if err != nil {
		_, _ = fmt.Fprintln(app.Err, "run-pending:", err)
		return 1
	}
	if len(results) == 0 {
		_, _ = fmt.Fprintln(app.Out, "No pending relocations.")
		return 0
	}
	log, _ := audit.New(auditDir())
	defer func() { _ = log.Close() }()
	exit := 0
	for _, r := range results {
		if r.Attempted {
			outcome, errStr := outcomeOf(r.Err)
			_ = log.Write(audit.Record{
				Module: "offload", Op: "relocate", Target: r.Entry.TargetPath,
				Size: int64(r.Freed), Outcome: outcome, Error: errStr,
				Evidence: map[string]string{
					"destination": filepath.Join(r.Entry.DestRoot, r.Entry.PlaybookAssetID),
					"queue_id":    strconv.FormatInt(r.Entry.ID, 10),
					"trigger":     offload.TriggerPendingManual,
				},
			})
		}
		switch r.Outcome {
		case offload.PendingApplied:
			_, _ = fmt.Fprintf(app.Out, "relocated: %s (freed %s locally)\n", r.Entry.TargetPath, r.Freed)
			if r.Err != nil {
				_, _ = fmt.Fprintf(app.Err, "  note: %v\n", r.Err)
			}
		case offload.PendingBlocked:
			_, _ = fmt.Fprintf(app.Out, "still blocked: #%d %s — %s\n", r.Entry.ID, r.Entry.TargetPath, r.Reason)
			exit = 1
		default:
			_, _ = fmt.Fprintf(app.Out, "skipped: #%d %s — %s\n", r.Entry.ID, r.Entry.TargetPath, r.Reason)
		}
	}
	return exit
}

// offloadCancel withdraws consent for a queued relocation (terminal).
func offloadCancel(app *App, cfg config.Config, args []string) int {
	if len(args) != 1 {
		_, _ = fmt.Fprintln(app.Err, "Usage: noo-noo offload cancel <id>   (ids from `noo-noo offload pending`)")
		return 2
	}
	id, err := strconv.ParseInt(args[0], 10, 64)
	if err != nil {
		_, _ = fmt.Fprintf(app.Err, "invalid id %q\n", args[0])
		return 2
	}
	qs, err := offloadQueueStore(cfg)
	if err != nil {
		_, _ = fmt.Fprintln(app.Err, "open queue store:", err)
		return 1
	}
	defer func() { _ = qs.Close() }()
	if err := qs.ResolveRelocation(id, store.RelocationResolution{
		Status:         store.RelocCancelled,
		ResolvedAtUnix: time.Now().Unix(),
	}); err != nil {
		_, _ = fmt.Fprintf(app.Err, "cancel #%d: %v\n", id, err)
		return 1
	}
	_, _ = fmt.Fprintf(app.Out, "cancelled #%d — it will not be retried.\n", id)
	return 0
}

// humanAge renders a duration as the largest sensible unit for queue ages.
func humanAge(d time.Duration) string {
	switch {
	case d >= 48*time.Hour:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	case d >= time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	case d >= time.Minute:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	default:
		return "just now"
	}
}

// printOffloadEvidence renders the per-asset knowledge the table view drops:
// the class, the exact native command where one exists, and risk notes.
func printOffloadEvidence(app *App, items []modules.Item) {
	for _, it := range items {
		ev := it.Evidence
		if ev == nil {
			continue
		}
		switch ev["class"] {
		case "native-config":
			_, _ = fmt.Fprintf(app.Out, "\n%s [native-config]\n  run: %s\n", it.Path, ev["native_command"])
		case "relocate":
			extra := ""
			if ev["never_delete"] == "true" {
				extra = " (never-delete: irreplaceable data, relocate only)"
			}
			if ev["stop_gate"] != "" {
				extra += fmt.Sprintf(" (stop-gate: %s must not be running)", ev["stop_gate"])
			}
			_, _ = fmt.Fprintf(app.Out, "\n%s [relocate]%s\n  guard: %s\n", it.Path, extra, ev["guard_verdict"])
		case "manual":
			_, _ = fmt.Fprintf(app.Out, "\n%s [manual]\n  %s\n", it.Path, ev["note"])
		case "unguarded-external-symlink":
			_, _ = fmt.Fprintf(app.Out, "\n%s [risk: unguarded external symlink → %s]\n  %s\n", it.Path, ev["target"], ev["risk"])
		}
	}
}
