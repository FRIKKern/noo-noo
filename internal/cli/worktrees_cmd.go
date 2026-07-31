package cli

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/FRIKKern/noo-noo/internal/audit"
	"github.com/FRIKKern/noo-noo/internal/config"
	"github.com/FRIKKern/noo-noo/internal/modules"
	"github.com/FRIKKern/noo-noo/internal/worktrees"
)

func init() { Register("worktrees", worktreesCmd) }

// worktreesModuleFromConfig builds the module off ~/.config/noo-noo/config.toml
// (missing file = compiled defaults), with [worktrees] roots falling back to
// the [scan] roots so one setting drives both the daemon and this CLI.
func worktreesModuleFromConfig() (*worktrees.Module, config.Config, error) {
	cfg, err := config.Load(filepath.Join(homeDir(), ".config", "noo-noo", "config.toml"))
	if err != nil {
		return nil, cfg, err
	}
	roots := cfg.Worktrees.Roots
	if len(roots) == 0 {
		roots = cfg.Scan.Roots
	}
	m := worktrees.New(worktrees.Config{
		Roots:    roots,
		MinIdle:  time.Duration(cfg.Worktrees.MinIdleHours) * time.Hour,
		JudgeCmd: cfg.Worktrees.JudgeCmd,
		GraveDir: cfg.Worktrees.GraveDir,
	})
	return m, cfg, nil
}

func worktreesCmd(ctx context.Context, app *App, args []string) int {
	fs := flag.NewFlagSet("worktrees", flag.ContinueOnError)
	fs.SetOutput(app.Err)
	asJSON := fs.Bool("json", false, "output NDJSON")
	yes := fs.Bool("y", false, "skip confirmation")
	dryRun := fs.Bool("dry-run", false, "show what would happen")
	verb, code, ok := parseVerb(app, fs, args, "Usage: noo-noo worktrees [list|scan|clean|judge]")
	if !ok {
		return code
	}
	switch verb {
	case "list", "scan", "clean", "judge":
	default:
		_, _ = fmt.Fprintf(app.Err, "unknown worktrees subcommand %q\n", verb)
		return 2
	}
	m, _, err := worktreesModuleFromConfig()
	if err != nil {
		_, _ = fmt.Fprintln(app.Err, "config:", err)
		return 1
	}
	rep, err := m.Scan(ctx)
	if err != nil {
		_, _ = fmt.Fprintln(app.Err, "scan:", err)
		return 1
	}
	switch verb {
	case "list", "scan":
		_ = PrintReport(app.Out, rep, *asJSON)
		if !*asJSON {
			printWorktreeDetails(app, rep)
		}
		return 0
	case "clean":
		return worktreesClean(ctx, app, m, rep, *asJSON, *yes, *dryRun)
	case "judge":
		return worktreesJudge(ctx, app, m, rep, *yes, *dryRun)
	default: // unreachable
		return 2
	}
}

// worktreesClean applies the obvious tier: Plan filters to it, Apply
// re-proves it per target.
func worktreesClean(ctx context.Context, app *App, m *worktrees.Module, rep modules.Report, asJSON, yes, dryRun bool) int {
	actions := m.Plan(rep)
	if len(actions) == 0 {
		_, _ = fmt.Fprintln(app.Out, "No provably-dead worktrees. (Judgment-tier candidates need `noo-noo worktrees judge`.)")
		return 0
	}
	_ = PrintReport(app.Out, rep, asJSON)
	var planned int64
	for _, a := range actions {
		planned += int64(a.Size)
	}
	if !Confirm(os.Stdin, app.Out,
		fmt.Sprintf("Remove %d provably-dead worktree(s), ~%s?", len(actions), fmtBytes(planned)), yes) {
		_, _ = fmt.Fprintln(app.Out, "Aborted.")
		return 0
	}
	log, _ := audit.New(auditDir())
	defer func() { _ = log.Close() }()
	var freed int64
	for _, a := range actions {
		if dryRun {
			_, _ = fmt.Fprintf(app.Out, "would remove: %s (%s)\n", a.Target, a.Size)
			continue
		}
		res, err := m.Apply(ctx, a)
		outcome, errStr := outcomeOf(err)
		_ = log.Write(audit.Record{
			Module: "worktrees", Op: a.Op, Target: a.Target,
			Size: int64(res.BytesFreed), Outcome: outcome, Error: errStr,
		})
		if err == nil {
			freed += int64(res.BytesFreed)
			_, _ = fmt.Fprintf(app.Out, "removed: %s (freed %s, statfs-measured)\n", a.Target, res.BytesFreed)
		} else {
			_, _ = fmt.Fprintf(app.Err, "refused/failed: %s — %v\n", a.Target, err)
		}
	}
	if !dryRun {
		_, _ = fmt.Fprintf(app.Out, "Freed %s (real, statfs-measured).\n", fmtBytes(freed))
	}
	return 0
}

// worktreesJudge runs the configured AI judge over every judgment-tier row,
// prints each ruling, and — after one summary confirmation — applies the
// remove verdicts through the grave-then-force path.
func worktreesJudge(ctx context.Context, app *App, m *worktrees.Module, rep modules.Report, yes, dryRun bool) int {
	var dossiers []worktrees.Dossier
	for _, it := range rep.Items {
		if it.Evidence["tier"] == "judgment" {
			dossiers = append(dossiers, worktrees.DossierFromItem(it))
		}
	}
	if len(dossiers) == 0 {
		_, _ = fmt.Fprintln(app.Out, "Nothing needs judgment.")
		return 0
	}
	type ruling struct {
		d worktrees.Dossier
		v worktrees.Verdict
	}
	var removals []ruling
	for _, d := range dossiers {
		v, err := m.Judge(ctx, d)
		if err != nil {
			_, _ = fmt.Fprintf(app.Err, "KEEP (judge failed) %s — %v\n", d.Path, err)
			continue
		}
		_, _ = fmt.Fprintf(app.Out, "%s  %s\n    %s\n", verdictTag(v.Verdict), d.Path, v.Reason)
		if v.Verdict == "remove" {
			removals = append(removals, ruling{d, v})
		}
	}
	if len(removals) == 0 {
		_, _ = fmt.Fprintln(app.Out, "Judge kept everything.")
		return 0
	}
	if dryRun {
		_, _ = fmt.Fprintf(app.Out, "dry-run: %d removal verdict(s) not applied.\n", len(removals))
		return 0
	}
	if !Confirm(os.Stdin, app.Out,
		fmt.Sprintf("Apply %d removal verdict(s)? Each is grave-bundled (commits + dirty files) first.", len(removals)), yes) {
		_, _ = fmt.Fprintln(app.Out, "Aborted — verdicts printed above, nothing removed.")
		return 0
	}
	log, _ := audit.New(auditDir())
	defer func() { _ = log.Close() }()
	for _, r := range removals {
		freed, bundle, tarball, err := m.ForceRemove(ctx, r.d.Path)
		outcome, errStr := outcomeOf(err)
		grave, _ := json.Marshal(map[string]string{"bundle": bundle, "tarball": tarball, "judge_reason": r.v.Reason})
		_ = log.Write(audit.Record{
			Module: "worktrees", Op: "judged-remove", Target: r.d.Path,
			Size: int64(freed), Outcome: outcome, Error: errStr + " " + string(grave),
		})
		if err != nil {
			_, _ = fmt.Fprintf(app.Err, "refused/failed: %s — %v\n", r.d.Path, err)
			continue
		}
		_, _ = fmt.Fprintf(app.Out, "removed: %s (freed %s; grave %s)\n", r.d.Path, freed, bundle)
	}
	return 0
}

func verdictTag(v string) string {
	if v == "remove" {
		return "REMOVE"
	}
	return "KEEP  "
}

// printWorktreeDetails renders the per-row evidence trail in human mode.
func printWorktreeDetails(app *App, rep modules.Report) {
	for _, it := range rep.Items {
		ev := it.Evidence
		_, _ = fmt.Fprintf(app.Out, "\n%s [%s] %s\n", strupperTier(ev["tier"]), ev["branch"], it.Path)
		_, _ = fmt.Fprintf(app.Out, "  repo: %s\n  idle: %s  remote-contained: %s\n", ev["repo"], ev["idle"], ev["remote_contained"])
		if ev["last_commit"] != "" {
			_, _ = fmt.Fprintf(app.Out, "  last commit: %s\n", ev["last_commit"])
		}
		if ev["dirty"] != "" {
			_, _ = fmt.Fprintf(app.Out, "  dirty: %s\n", ev["dirty"])
		}
		if ev["ahead_of_upstream"] != "" && ev["ahead_of_upstream"] != "0" {
			_, _ = fmt.Fprintf(app.Out, "  ahead of upstream: %s commit(s)\n", ev["ahead_of_upstream"])
		}
		if ev["prunable"] != "" {
			_, _ = fmt.Fprintf(app.Out, "  prunable: %s\n", ev["prunable"])
		}
	}
}

func strupperTier(s string) string {
	switch s {
	case "obvious":
		return "OBVIOUS "
	case "judgment":
		return "JUDGMENT"
	default:
		return s
	}
}

func fmtBytes(n int64) string {
	const k = 1024
	if n < k {
		return fmt.Sprintf("%d B", n)
	}
	units := []string{"KB", "MB", "GB", "TB"}
	f := float64(n) / k
	u := 0
	for f >= k && u < len(units)-1 {
		f /= k
		u++
	}
	return fmt.Sprintf("%.1f %s", f, units[u])
}
