// orphans CLI subcommand: list | scan | kill.
//
// Finds orphaned automation browsers (headless, scratch-dir profile, parent
// job gone) via the internal/procsig registry and, on `kill`, terminates
// them behind confirmation. Killing an orphan releases its open-file pins,
// which is exactly what lets the EXISTING leaks signatures sweep the Chrome
// code_sign_clone dirs it was holding live — so kill output always points
// at `noo-noo leaks clean` (composition by sequencing, charter D20).
//
// Verb-first flag law (charter D22): the verb is stripped BEFORE
// flag.Parse, so `noo-noo orphans kill -y` parses -y instead of silently
// dropping it into positionals and re-prompting.
package cli

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/FRIKKern/noo-noo/internal/audit"
	"github.com/FRIKKern/noo-noo/internal/modules"
	"github.com/FRIKKern/noo-noo/internal/procsig"
)

func init() { Register("orphans", orphansCmd) }

// newOrphansModule is the module hook; tests inject a fake so no CLI test
// ever scans the live process table or signals a real process.
var newOrphansModule = func() modules.Module {
	return procsig.New(procsig.DefaultSignatures())
}

func orphansCmd(ctx context.Context, app *App, args []string) int {
	if len(args) == 0 {
		_, _ = fmt.Fprintln(app.Err, "Usage: noo-noo orphans [list|scan|kill]")
		return 2
	}
	verb, rest := args[0], args[1:]
	switch verb {
	case "list", "scan", "kill":
	default:
		_, _ = fmt.Fprintf(app.Err, "unknown orphans subcommand %q (try list|scan|kill)\n", verb)
		return 2
	}
	fs := flag.NewFlagSet("orphans "+verb, flag.ContinueOnError)
	fs.SetOutput(app.Err)
	asJSON := fs.Bool("json", false, "output NDJSON")
	yes := fs.Bool("y", false, "skip confirmation")
	dryRun := fs.Bool("dry-run", false, "show what would happen")
	if err := fs.Parse(rest); err != nil {
		return 2
	}
	// Trust guard: anything flag.Parse left over is a mistake the user
	// needs to hear about, never a silent no-op.
	if fs.NArg() > 0 {
		_, _ = fmt.Fprintf(app.Err, "unexpected argument(s) %v after 'orphans %s'\n", fs.Args(), verb)
		return 2
	}

	m := newOrphansModule()
	rep, err := m.Scan(ctx)
	if err != nil {
		_, _ = fmt.Fprintln(app.Err, "scan:", err)
		return 1
	}
	if len(rep.Items) == 0 {
		_, _ = fmt.Fprintln(app.Out, "No orphaned automation browsers found. (Headless browsers with a live parent job are never touched.)")
		return 0
	}

	switch verb {
	case "list", "scan":
		_ = PrintReport(app.Out, rep, *asJSON)
		if !*asJSON {
			printOrphanDetails(app, rep)
			_, _ = fmt.Fprintln(app.Out, "\nTerminate with 'noo-noo orphans kill', then run 'noo-noo leaks clean' to sweep the clone dirs they were pinning.")
		}
		return 0
	case "kill":
		return orphansKill(ctx, app, m, rep, *asJSON, *yes, *dryRun)
	default: // unreachable: verb validated above
		return 2
	}
}

// orphansKill plans, confirms, and terminates the orphaned browsers in rep,
// logging each outcome to the audit trail. Split out of orphansCmd so the
// dispatcher stays flat.
func orphansKill(ctx context.Context, app *App, m modules.Module, rep modules.Report, asJSON, yes, dryRun bool) int {
	actions := m.Plan(rep)
	if len(actions) == 0 {
		_, _ = fmt.Fprintln(app.Out, "Nothing to terminate.")
		return 0
	}
	_ = PrintReport(app.Out, rep, asJSON)
	if !asJSON {
		printOrphanDetails(app, rep)
	}
	if !Confirm(os.Stdin, app.Out,
		fmt.Sprintf("Terminate %d orphaned automation browser process(es)? (SIGTERM, SIGKILL after a grace period)", len(actions)),
		yes) {
		_, _ = fmt.Fprintln(app.Out, "Aborted.")
		return 0
	}
	log, _ := audit.New(auditDir())
	defer func() { _ = log.Close() }()
	killed := 0
	for _, a := range actions {
		dir := userDataDirFor(rep, a.Target)
		if dryRun {
			_, _ = fmt.Fprintf(app.Out, "would terminate: pid %s (%s)\n", a.Target, dir)
			continue
		}
		_, err := m.Apply(ctx, a)
		outcome, errStr := outcomeOf(err)
		_ = log.Write(audit.Record{
			Module: "orphans", Op: a.Op, Target: a.Target,
			Evidence: map[string]string{"user_data_dir": dir},
			Outcome:  outcome, Error: errStr,
		})
		if err == nil {
			killed++
			_, _ = fmt.Fprintf(app.Out, "terminated: pid %s (%s)\n", a.Target, dir)
		} else {
			_, _ = fmt.Fprintf(app.Err, "refused/failed: pid %s — %v\n", a.Target, err)
		}
	}
	if !dryRun && killed > 0 {
		_, _ = fmt.Fprintf(app.Out, "Terminated %d orphaned automation browser(s).\n", killed)
		_, _ = fmt.Fprintln(app.Out, "Their leaked Chrome code-sign clones are now unpinned — run 'noo-noo leaks clean' to sweep them.")
	}
	return 0
}

// printOrphanDetails renders the per-hit evidence trail in human mode: pid,
// how long the orphan has been running, its command line, and the
// prevention advice.
func printOrphanDetails(app *App, rep modules.Report) {
	for _, it := range rep.Items {
		ev := it.Evidence
		_, _ = fmt.Fprintf(app.Out, "\nORPHAN [%s] pid %s — profile %s\n", ev["signature"], ev["pid"], it.Path)
		if ev["etime"] != "" {
			_, _ = fmt.Fprintf(app.Out, "  running for: %s (parent job is gone — reparented to launchd)\n", ev["etime"])
		}
		if ev["cmdline"] != "" {
			_, _ = fmt.Fprintf(app.Out, "  cmdline: %s\n", ev["cmdline"])
		}
		if ev["workaround"] != "" {
			_, _ = fmt.Fprintf(app.Out, "  prevent: %s\n", ev["workaround"])
		}
	}
}

// userDataDirFor finds the scanned profile dir for a planned pid, for
// audit evidence and human output.
func userDataDirFor(rep modules.Report, pid string) string {
	for _, it := range rep.Items {
		if it.Evidence["pid"] == pid {
			return it.Path
		}
	}
	return ""
}
