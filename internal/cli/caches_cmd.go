package cli

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/FRIKKern/noo-noo/internal/audit"
	"github.com/FRIKKern/noo-noo/internal/core"
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
	}
	out := make([]string, len(rels))
	for i, r := range rels {
		out[i] = filepath.Join(home, r)
	}
	return out
}

func cachesCmd(ctx context.Context, app *App, args []string) int {
	fs := flag.NewFlagSet("caches", flag.ContinueOnError)
	fs.SetOutput(app.Err)
	asJSON := fs.Bool("json", false, "output NDJSON")
	yes := fs.Bool("y", false, "skip confirmation")
	dryRun := fs.Bool("dry-run", false, "show what would happen")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	rest := fs.Args()
	if len(rest) == 0 {
		_, _ = fmt.Fprintln(app.Err, "Usage: noo-noo caches [list|clean]")
		return 2
	}
	targets := defaultCacheTargets()
	safety := core.NewSafety([]string{filepath.Join(homeDir(), "Library", "Caches")}, nil)
	m := caches.New(targets, safety)

	rep, err := m.Scan(ctx)
	if err != nil {
		_, _ = fmt.Fprintln(app.Err, "scan:", err)
		return 1
	}
	switch rest[0] {
	case "list":
		_ = PrintReport(app.Out, rep, *asJSON)
		return 0
	case "clean":
		actions := m.Plan(rep)
		if len(actions) == 0 {
			_, _ = fmt.Fprintln(app.Out, "Nothing to clean.")
			return 0
		}
		_ = PrintReport(app.Out, rep, *asJSON)
		if !Confirm(os.Stdin, app.Out,
			fmt.Sprintf("Clear %d cache target(s) totalling %s?", len(actions), rep.Total),
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
		_, _ = fmt.Fprintf(app.Err, "unknown caches subcommand %q\n", rest[0])
		return 2
	}
}

func outcomeOf(err error) (outcome, errStr string) {
	if err != nil {
		return "error", err.Error()
	}
	return "ok", ""
}
