package cli

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/FRIKKern/noo-noo/internal/audit"
	"github.com/FRIKKern/noo-noo/internal/config"
	"github.com/FRIKKern/noo-noo/internal/modules"
	"github.com/FRIKKern/noo-noo/internal/modules/offload"
)

func init() { Register("offload", offloadCmd) }

// defaultConfigPath is the same file the daemon and the menubar app read.
func defaultConfigPath() string {
	return filepath.Join(homeDir(), ".config", "noo-noo", "config.toml")
}

// newOffloadModule builds the offload module from the user config.
// Diagnose-by-default: an unconfigured [offload] section still scans and
// reports — it just cannot plan or apply anything.
func newOffloadModule() *offload.Module {
	cfg, err := config.Load(defaultConfigPath())
	if err != nil {
		cfg = config.Defaults()
	}
	return offload.New(offload.Config{
		DestRoot:       cfg.Offload.DestRoot,
		DestVolumeUUID: cfg.Offload.DestVolumeUUID,
	}, nil, offload.Deps{})
}

func offloadCmd(ctx context.Context, app *App, args []string) int {
	fs := flag.NewFlagSet("offload", flag.ContinueOnError)
	fs.SetOutput(app.Err)
	asJSON := fs.Bool("json", false, "output NDJSON")
	yes := fs.Bool("y", false, "skip confirmation")
	dryRun := fs.Bool("dry-run", false, "show what would happen")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	rest := fs.Args()
	if len(rest) == 0 {
		_, _ = fmt.Fprintln(app.Err, "Usage: noo-noo offload [scan|plan|apply]")
		return 2
	}
	m := newOffloadModule()

	rep, err := m.Scan(ctx)
	if err != nil {
		_, _ = fmt.Fprintln(app.Err, "scan:", err)
		return 1
	}
	switch rest[0] {
	case "scan":
		_ = PrintReport(app.Out, rep, *asJSON)
		if !*asJSON {
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
	case "apply":
		actions := m.Plan(rep)
		if len(actions) == 0 {
			_, _ = fmt.Fprintln(app.Out, "Nothing to relocate. Configure [offload] dest_root + dest_volume_uuid, or use the native commands from `noo-noo offload scan`.")
			return 0
		}
		for _, a := range actions {
			_, _ = fmt.Fprintf(app.Out, "relocate: %s → %s (frees %s locally)\n", a.Target, a.Destination, a.Size)
		}
		if !Confirm(os.Stdin, app.Out,
			fmt.Sprintf("Relocate %d asset(s)? Data is copied, verified, then symlinked — nothing is deleted until the copy is proven.", len(actions)),
			*yes) {
			_, _ = fmt.Fprintln(app.Out, "Aborted.")
			return 0
		}
		log, _ := audit.New(auditDir())
		defer func() { _ = log.Close() }()
		exit := 0
		for _, a := range actions {
			if *dryRun {
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
			if err == nil {
				_, _ = fmt.Fprintf(app.Out, "relocated: %s → %s (freed %s locally)\n", a.Target, a.Destination, res.BytesFreed)
			} else {
				_, _ = fmt.Fprintf(app.Err, "failed: %s — %v\n", a.Target, err)
				exit = 1
			}
		}
		return exit
	default:
		_, _ = fmt.Fprintf(app.Err, "unknown offload subcommand %q\n", rest[0])
		return 2
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
