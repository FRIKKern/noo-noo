package cli

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/FRIKKern/noo-noo/internal/audit"
	"github.com/FRIKKern/noo-noo/internal/core"
	"github.com/FRIKKern/noo-noo/internal/modules/dev"
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
	verb, code, ok := parseVerb(app, fs, args, "Usage: noo-noo dev [list|clean] [flags]")
	if !ok {
		return code
	}
	rootList := splitCSV(*roots)
	safety := core.NewSafety(rootList, []string{".git"})
	m := dev.New(rootList, safety)

	rep, err := m.Scan(ctx)
	if err != nil {
		_, _ = fmt.Fprintln(app.Err, "scan:", err)
		return 1
	}

	switch verb {
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
		_, _ = fmt.Fprintf(app.Err, "unknown dev subcommand %q\n", verb)
		return 2
	}
}

// parseVerb separates the subcommand verb from its flags and parses the flags,
// tolerating BOTH "<verb> -flags" (natural) and "-flags <verb>" (legacy)
// ordering. Go's flag package stops parsing at the first non-flag token, so a
// naive fs.Parse(args) silently drops every flag written AFTER the verb — the
// proven trust bug where `caches clean -y` printed the plan, then prompted
// anyway and aborted. Permuting flag parsing around the first bare positional
// fixes the natural order without regressing the legacy flags-first order.
//
// On success it returns the verb and ok=true; the caller switches on the verb.
// It returns ok=false with the exit code already decided for: a flag parse
// error (undefined flag, bad value — code 2), a missing verb (usage printed —
// code 2), or a flag-like token that survived parsing behind a "--" terminator
// (never silently dropped; hard-errored — code 2). Flags are read through the
// pointers the caller already bound on fs before calling.
func parseVerb(app *App, fs *flag.FlagSet, args []string, usage string) (verb string, code int, ok bool) {
	remaining := args
	var extra []string
	for {
		if err := fs.Parse(remaining); err != nil {
			return "", 2, false
		}
		tail := fs.Args()
		if len(tail) == 0 {
			break
		}
		if verb == "" && !flagLike(tail[0]) {
			verb, remaining = tail[0], tail[1:]
			continue
		}
		// Verb already found, or a flag-like token flag.Parse refused to
		// consume (only reachable past a "--" terminator). Stop; the loop
		// has settled and `tail` is the unconsumed remainder.
		extra = tail
		break
	}
	if verb == "" {
		_, _ = fmt.Fprintln(app.Err, usage)
		return "", 2, false
	}
	if bad := firstFlagLike(extra); bad != "" {
		_, _ = fmt.Fprintf(app.Err, "unknown flag %q after subcommand %q\n", bad, verb)
		return "", 2, false
	}
	return verb, 0, true
}

// flagLike reports whether a token looks like a flag ("-x" / "--x"). A bare
// "-" (the stdin convention) is a positional, not a flag.
func flagLike(tok string) bool { return len(tok) > 1 && tok[0] == '-' }

// firstFlagLike returns the first flag-like token in args, or "" if none.
func firstFlagLike(args []string) string {
	for _, a := range args {
		if flagLike(a) {
			return a
		}
	}
	return ""
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
