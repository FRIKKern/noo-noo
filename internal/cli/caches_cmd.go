package cli

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/FRIKKern/noo-noo/internal/audit"
	"github.com/FRIKKern/noo-noo/internal/core"
	"github.com/FRIKKern/noo-noo/internal/modules"
	"github.com/FRIKKern/noo-noo/internal/modules/caches"
)

func init() { Register("caches", cachesCmd) }

// defaultCacheTargets mirrors the bash cache-cleanup.sh TARGETS list.
// Paths are home-relative.
func defaultCacheTargets() []string {
	home := homeDir()
	rels := []string{
		"Library/Caches/Yarn",
		"Library/Caches/pnpm",
		"Library/Caches/Google",
		"Library/Caches/com.spotify.client",
		"Library/Caches/Adobe",
		"Library/Caches/Arc",
		"Library/Caches/curseforge-updater",
		"Library/Caches/go-build",
		"Library/Caches/Cursor",
		"Library/Caches/typescript",
		"Library/Caches/composer",
		"Library/Caches/node-gyp",
		"Library/Caches/electron",
		// iOS Simulator dyld shared-cache: 5.7 GB on the reference machine,
		// rebuilt on the next simulator boot. Lives outside ~/Library/Caches,
		// so defaultCacheSafety roots it explicitly.
		"Library/Developer/CoreSimulator/Caches/dyld",
	}
	out := make([]string, 0, len(rels)+2)
	for _, r := range rels {
		out = append(out, filepath.Join(home, r))
	}
	// Electron ShipIt updater caches (~/Library/Caches/<bundle-id>.ShipIt)
	// hold stale downloaded app updates — ~3G across three apps on the
	// reference machine — regenerated on the next update check, so a safe
	// delete class. Bundle IDs are build-generated, so match them by glob
	// rather than literals. filepath.Glob returns only existing paths (and no
	// error for "no match"), so a machine without any ShipIt dir gains nothing
	// and the safety root (~/Library/Caches) is unchanged.
	if matches, err := filepath.Glob(filepath.Join(home, "Library", "Caches", "*.ShipIt")); err == nil {
		out = append(out, matches...)
	}
	return out
}

// defaultCacheSafety roots cache clears at ~/Library/Caches plus the
// CoreSimulator cache root — nothing wider.
func defaultCacheSafety() *core.Safety {
	home := homeDir()
	return core.NewSafety([]string{
		filepath.Join(home, "Library", "Caches"),
		filepath.Join(home, "Library", "Developer", "CoreSimulator", "Caches"),
	}, nil)
}

// newCachesModule wires the shipped target list plus the report-only
// ~/.Trash item (sized, never cleaned: the user empties the Trash).
func newCachesModule() *caches.Module {
	return caches.New(defaultCacheTargets(), defaultCacheSafety()).
		WithReportOnly(caches.ReportOnly{Path: filepath.Join(homeDir(), ".Trash"), Suggestion: "Empty Trash"})
}

func cachesCmd(ctx context.Context, app *App, args []string) int {
	fs := flag.NewFlagSet("caches", flag.ContinueOnError)
	fs.SetOutput(app.Err)
	asJSON := fs.Bool("json", false, "output NDJSON")
	yes := fs.Bool("y", false, "skip confirmation")
	dryRun := fs.Bool("dry-run", false, "show what would happen")
	verb, code, ok := parseVerb(app, fs, args, "Usage: noo-noo caches [list|clean]")
	if !ok {
		return code
	}
	m := newCachesModule()

	rep, err := m.Scan(ctx)
	if err != nil {
		_, _ = fmt.Fprintln(app.Err, "scan:", err)
		return 1
	}
	switch verb {
	case "list":
		_ = PrintReport(app.Out, rep, *asJSON)
		printReportOnlyNotes(app, rep, *asJSON)
		return 0
	case "clean":
		actions := m.Plan(rep)
		if len(actions) == 0 {
			_, _ = fmt.Fprintln(app.Out, "Nothing to clean.")
			printReportOnlyNotes(app, rep, *asJSON)
			return 0
		}
		_ = PrintReport(app.Out, rep, *asJSON)
		printReportOnlyNotes(app, rep, *asJSON)
		var planned core.Bytes
		for _, a := range actions {
			planned += a.Size
		}
		if !Confirm(os.Stdin, app.Out,
			fmt.Sprintf("Clear %d cache target(s) totalling %s?", len(actions), planned),
			*yes) {
			_, _ = fmt.Fprintln(app.Out, "Aborted.")
			return 0
		}
		log, _ := audit.New(auditDir())
		defer func() { _ = log.Close() }()
		var freed core.Bytes
		for _, a := range actions {
			if *dryRun {
				_, _ = fmt.Fprintf(app.Out, "would clear: %s (%s)\n", a.Target, a.Size)
				continue
			}
			res, err := m.Apply(ctx, a)
			outcome, errStr := outcomeOf(err)
			_ = log.Write(audit.Record{
				Module: "caches", Op: a.Op, Target: a.Target,
				Size: int64(res.BytesFreed), Outcome: outcome, Error: errStr,
			})
			if err == nil {
				freed += res.BytesFreed
				_, _ = fmt.Fprintf(app.Out, "cleared: %s (%s)\n", a.Target, res.BytesFreed)
			} else {
				_, _ = fmt.Fprintf(app.Err, "failed: %s — %v\n", a.Target, err)
			}
		}
		if !*dryRun {
			_, _ = fmt.Fprintf(app.Out, "Freed %s.\n", freed)
		}
		return 0
	default:
		_, _ = fmt.Fprintf(app.Err, "unknown caches subcommand %q\n", verb)
		return 2
	}
}

// printReportOnlyNotes names the sized-but-never-cleaned items (human mode
// only; JSON rows already carry report_only/suggestion evidence).
func printReportOnlyNotes(app *App, rep modules.Report, asJSON bool) {
	if asJSON {
		return
	}
	for _, it := range rep.Items {
		if it.Evidence["report_only"] == "true" {
			_, _ = fmt.Fprintf(app.Out, "note: %s (%s) is reported only — %s\n", it.Path, it.Size, it.Evidence["suggestion"])
		}
	}
}

func outcomeOf(err error) (outcome, errStr string) {
	if err != nil {
		return "error", err.Error()
	}
	return "ok", ""
}
