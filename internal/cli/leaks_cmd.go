package cli

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/FRIKKern/noo-noo/internal/audit"
	"github.com/FRIKKern/noo-noo/internal/core"
	"github.com/FRIKKern/noo-noo/internal/leaks"
	"github.com/FRIKKern/noo-noo/internal/modules"
)

func init() { Register("leaks", leaksCmd) }

// newLeaksModule wires the shipped signature registry. Leak deletes go
// through the signature-scoped CanDeleteLeakTarget predicate, so the Safety
// needs no allowlist roots — and gets none, keeping the generic wall inert
// here by construction.
func newLeaksModule() *leaks.Module {
	return leaks.New(leaks.DefaultSignatures(), core.NewSafety(nil, nil))
}

func leaksCmd(ctx context.Context, app *App, args []string) int {
	fs := flag.NewFlagSet("leaks", flag.ContinueOnError)
	fs.SetOutput(app.Err)
	asJSON := fs.Bool("json", false, "output NDJSON")
	yes := fs.Bool("y", false, "skip confirmation")
	dryRun := fs.Bool("dry-run", false, "show what would happen")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	rest := fs.Args()
	if len(rest) == 0 {
		_, _ = fmt.Fprintln(app.Err, "Usage: noo-noo leaks [list|scan|clean]")
		return 2
	}
	switch rest[0] {
	case "list", "scan", "clean":
	default:
		_, _ = fmt.Fprintf(app.Err, "unknown leaks subcommand %q\n", rest[0])
		return 2
	}
	m := newLeaksModule()

	rep, err := m.Scan(ctx)
	if err != nil {
		_, _ = fmt.Fprintln(app.Err, "scan:", err)
		return 1
	}
	switch rest[0] {
	case "list", "scan":
		// Diagnose-only: report every hit with its staleness verdict and
		// truth-sized evidence. Nothing is deleted here.
		_ = PrintReport(app.Out, rep, *asJSON)
		if !*asJSON {
			printLeakDetails(app, rep)
		}
		return 0
	case "clean":
		actions := m.Plan(rep)
		if len(actions) == 0 {
			_, _ = fmt.Fprintln(app.Out, "Nothing provably stale to clean. (LIVE hits are never deletable.)")
			return 0
		}
		_ = PrintReport(app.Out, rep, *asJSON)
		if !*asJSON {
			printLeakDetails(app, rep)
		}
		var planned core.Bytes
		for _, a := range actions {
			planned += a.Size
		}
		if !Confirm(os.Stdin, app.Out,
			fmt.Sprintf("Delete %d stale leak hit(s), ~%s real reclaimable?", len(actions), planned),
			*yes) {
			_, _ = fmt.Fprintln(app.Out, "Aborted.")
			return 0
		}
		log, _ := audit.New(auditDir())
		defer func() { _ = log.Close() }()
		var freed core.Bytes
		for _, a := range actions {
			if *dryRun {
				_, _ = fmt.Fprintf(app.Out, "would delete: %s (%s)\n", a.Target, a.Size)
				continue
			}
			res, err := m.Apply(ctx, a)
			outcome, errStr := outcomeOf(err)
			_ = log.Write(audit.Record{
				Module: "leaks", Op: a.Op, Target: a.Target,
				Size: int64(res.BytesFreed), Outcome: outcome, Error: errStr,
			})
			if err == nil {
				freed += res.BytesFreed
				_, _ = fmt.Fprintf(app.Out, "deleted: %s (freed %s, statfs-measured)\n", a.Target, res.BytesFreed)
			} else {
				_, _ = fmt.Fprintf(app.Err, "refused/failed: %s — %v\n", a.Target, err)
			}
		}
		if !*dryRun {
			_, _ = fmt.Fprintf(app.Out, "Freed %s (real, statfs-measured).\n", freed)
		}
		return 0
	default: // unreachable: subcommand validated above
		return 2
	}
}

// printLeakDetails renders the per-hit evidence trail in human mode: the
// stale/live verdict, the du-vs-real sizing delta, the lsof proof, and the
// signature's workaround.
func printLeakDetails(app *App, rep modules.Report) {
	for _, it := range rep.Items {
		ev := it.Evidence
		_, _ = fmt.Fprintf(app.Out, "\n%s [%s] %s\n", strupper(ev["staleness"]), ev["signature"], it.Path)
		if ev["blocks_bytes"] != "" && ev["du_fiction_bytes"] != "" {
			_, _ = fmt.Fprintf(app.Out, "  du would say %s bytes; really reclaimable %s bytes (APFS-clone fiction: %s bytes)\n",
				ev["blocks_bytes"], ev["unique_allocated_bytes"], ev["du_fiction_bytes"])
		} else if ev["unique_allocated_bytes"] != "" {
			_, _ = fmt.Fprintf(app.Out, "  really reclaimable: %s bytes\n", ev["unique_allocated_bytes"])
		}
		if ev["lsof"] != "" {
			_, _ = fmt.Fprintf(app.Out, "  liveness: %s\n", ev["lsof"])
		}
		if ev["age"] != "" {
			_, _ = fmt.Fprintf(app.Out, "  age: %s\n", ev["age"])
		}
		if ev["workaround"] != "" {
			_, _ = fmt.Fprintf(app.Out, "  prevent: %s\n", ev["workaround"])
		}
	}
}

func strupper(s string) string {
	switch s {
	case "stale":
		return "STALE"
	case "live":
		return "LIVE"
	default:
		return s
	}
}
