package cli

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/FRIKKern/noo-noo/internal/audit"
	"github.com/FRIKKern/noo-noo/internal/core"
	"github.com/FRIKKern/noo-noo/internal/modules"
	"github.com/FRIKKern/noo-noo/internal/modules/orphanapps"
)

func init() { Register("apps", appsCmd) }

// newAppsModule wires the orphaned-app-data module over the shipped
// library roots and curated home folders, resolving apps through one
// batched Spotlight query per run.
func newAppsModule() *orphanapps.Module {
	home := homeDir()
	return orphanapps.New(orphanapps.DefaultLibraryRoots(home), orphanapps.DefaultCurated(home), orphanapps.BuildIndex)
}

func appsCmd(ctx context.Context, app *App, args []string) int {
	fs := flag.NewFlagSet("apps", flag.ContinueOnError)
	fs.SetOutput(app.Err)
	asJSON := fs.Bool("json", false, "output NDJSON")
	yes := fs.Bool("y", false, "skip confirmation")
	dryRun := fs.Bool("dry-run", false, "show what would happen")
	verb, code, ok := parseVerb(app, fs, args, "Usage: noo-noo apps [list|clean]")
	if !ok {
		return code
	}
	switch verb {
	case "list", "clean":
	default:
		_, _ = fmt.Fprintf(app.Err, "unknown apps subcommand %q\n", verb)
		return 2
	}
	m := newAppsModule()

	rep, err := m.Scan(ctx)
	if err != nil {
		_, _ = fmt.Fprintln(app.Err, "scan:", err)
		return 1
	}
	_ = PrintReport(app.Out, rep, *asJSON)
	if !*asJSON {
		printAppDetails(app, rep)
	}
	if verb == "list" {
		return 0
	}
	actions := m.Plan(rep)
	if len(actions) == 0 {
		_, _ = fmt.Fprintln(app.Out, "No orphaned app data to clean.")
		return 0
	}
	var planned core.Bytes
	for _, a := range actions {
		planned += a.Size
	}
	if !Confirm(os.Stdin, app.Out,
		fmt.Sprintf("Delete %d orphaned app-data dir(s), ~%s real? (data of uninstalled apps — not regenerable)", len(actions), planned),
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
			Module: "apps", Op: a.Op, Target: a.Target,
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
}

// printAppDetails renders the per-dir verdict: why it is orphaned, how idle,
// and the truth-vs-fiction sizing (sparse Docker.raw, APFS clones).
func printAppDetails(app *App, rep modules.Report) {
	for _, it := range rep.Items {
		ev := it.Evidence
		_, _ = fmt.Fprintf(app.Out, "\n%s\n  %s\n", it.Path, ev["reason"])
		if ev["sparse_fiction_bytes"] != "" {
			_, _ = fmt.Fprintf(app.Out, "  apparent %s bytes, really allocated %s bytes (sparse: %s bytes never written)\n",
				ev["logical_bytes"], ev["unique_allocated_bytes"], ev["sparse_fiction_bytes"])
		} else if ev["du_fiction_bytes"] != "" {
			_, _ = fmt.Fprintf(app.Out, "  du would say %s bytes; really reclaimable %s bytes (APFS-clone fiction: %s bytes)\n",
				ev["blocks_bytes"], ev["unique_allocated_bytes"], ev["du_fiction_bytes"])
		}
		if ev["resolver"] != "" {
			_, _ = fmt.Fprintf(app.Out, "  resolver: %s\n", ev["resolver"])
		}
		if ev["suggestion"] != "" {
			_, _ = fmt.Fprintf(app.Out, "  suggest: %s\n", ev["suggestion"])
		}
	}
}
