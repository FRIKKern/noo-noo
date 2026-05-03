package cli

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/frikkjarl/noo-noo/internal/audit"
	"github.com/frikkjarl/noo-noo/internal/core"
	"github.com/frikkjarl/noo-noo/internal/modules/dev"
)

func init() { Register("dev", devCmd) }

func devCmd(ctx context.Context, app *App, args []string) int {
	fs := flag.NewFlagSet("dev", flag.ContinueOnError)
	fs.SetOutput(app.Err)
	asJSON := fs.Bool("json", false, "output NDJSON")
	yes := fs.Bool("y", false, "skip confirmation")
	dryRun := fs.Bool("dry-run", false, "show what would happen")
	roots := fs.String("roots", filepath.Join(homeDir(), "Documents", "GitHub"),
		"comma-separated scan roots")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	rest := fs.Args()
	if len(rest) == 0 {
		_, _ = fmt.Fprintln(app.Err, "Usage: noo-noo dev [list|clean] [flags]")
		return 2
	}
	rootList := splitCSV(*roots)
	safety := core.NewSafety(rootList, []string{".git"})
	m := dev.New(rootList, safety)

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
			fmt.Sprintf("Delete %d folder(s) totalling %s?", len(actions), rep.Total),
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
			outcome := "ok"
			errStr := ""
			if err != nil {
				outcome = "error"
				errStr = err.Error()
			}
			_ = log.Write(audit.Record{
				Module: "dev", Op: a.Op, Target: a.Target,
				Size: int64(res.BytesFreed), Outcome: outcome, Error: errStr,
			})
			if err == nil {
				freed += res.BytesFreed
				_, _ = fmt.Fprintf(app.Out, "deleted: %s (%s)\n", a.Target, res.BytesFreed)
			} else {
				_, _ = fmt.Fprintf(app.Err, "failed: %s — %v\n", a.Target, err)
			}
		}
		if !*dryRun {
			_, _ = fmt.Fprintf(app.Out, "Freed %s.\n", freed)
		}
		return 0
	default:
		_, _ = fmt.Fprintf(app.Err, "unknown dev subcommand %q\n", rest[0])
		return 2
	}
}

func homeDir() string {
	if h, err := os.UserHomeDir(); err == nil {
		return h
	}
	return "."
}

func auditDir() string {
	return filepath.Join(homeDir(), "Library", "Logs", "noo-noo")
}

func splitCSV(s string) []string {
	if s == "" {
		return nil
	}
	var out []string
	cur := ""
	for _, r := range s {
		if r == ',' {
			out = append(out, cur)
			cur = ""
			continue
		}
		cur += string(r)
	}
	if cur != "" {
		out = append(out, cur)
	}
	return out
}
