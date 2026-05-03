package cli

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/FRIKKern/noo-noo/internal/audit"
	"github.com/FRIKKern/noo-noo/internal/modules"
	"github.com/FRIKKern/noo-noo/internal/modules/startup"
)

func init() { Register("startup", startupCmd) }

// defaultStartupConfig mirrors the bash startup-cleanup.sh lists.
// Edit your local config.toml (Phase 0.2) to override.
func defaultStartupConfig() startup.Config {
	return startup.Config{
		UserAgents: []string{
			"com.google.GoogleUpdater.wake",
			"com.google.keystone.agent",
			"com.google.keystone.xpcservice",
		},
		SystemAgents: []string{
			"com.adobe.AdobeCreativeCloud",
			"com.adobe.ccxprocess",
			"com.google.keystone.agent",
			"com.google.keystone.xpcservice",
			"com.razer.rzupdater",
			"com.razerzone.rzdeviceengine",
		},
		SystemDaemons: []string{
			"com.adobe.ARMDC.Communicator",
			"com.adobe.ARMDC.SMJobBlessHelper",
			"com.adobe.acc.installer.v2",
			"com.google.GoogleUpdater.wake.system",
			"com.google.keystone.daemon",
			"com.teamviewer.Helper",
			"com.teamviewer.UninstallerHelper",
			"com.teamviewer.UninstallerWatcher",
		},
	}
}

func startupCmd(ctx context.Context, app *App, args []string) int {
	fs := flag.NewFlagSet("startup", flag.ContinueOnError)
	fs.SetOutput(app.Err)
	asJSON := fs.Bool("json", false, "output NDJSON")
	yes := fs.Bool("y", false, "skip confirmation")
	dryRun := fs.Bool("dry-run", false, "show what would happen")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	rest := fs.Args()
	if len(rest) == 0 {
		_, _ = fmt.Fprintln(app.Err, "Usage: noo-noo startup [list|disable|restore]")
		return 2
	}

	cfg := defaultStartupConfig()
	restoreLog := filepath.Join(auditDir(), "startup-restore.jsonl")
	m := startup.New(cfg, startup.ExecRunner{}, restoreLog, os.Getuid())

	rep, err := m.Scan(ctx)
	if err != nil {
		_, _ = fmt.Fprintln(app.Err, "scan:", err)
		return 1
	}

	switch rest[0] {
	case "list":
		_ = PrintReport(app.Out, rep, *asJSON)
		return 0
	case "disable":
		actions := m.Plan(rep)
		_ = PrintReport(app.Out, rep, *asJSON)
		if !Confirm(os.Stdin, app.Out,
			fmt.Sprintf("Disable %d service(s)? (system items will prompt for sudo)", len(actions)),
			*yes) {
			_, _ = fmt.Fprintln(app.Out, "Aborted.")
			return 0
		}
		log, _ := audit.New(auditDir())
		defer func() { _ = log.Close() }()
		for _, a := range actions {
			if *dryRun {
				_, _ = fmt.Fprintf(app.Out, "would disable: %s\n", a.Target)
				continue
			}
			_, err := m.Apply(ctx, a)
			outcome, errStr := outcomeOf(err)
			_ = log.Write(audit.Record{
				Module: "startup", Op: "disable", Target: a.Target,
				Outcome: outcome, Error: errStr,
			})
			if err == nil {
				_, _ = fmt.Fprintf(app.Out, "disabled: %s\n", a.Target)
			} else {
				_, _ = fmt.Fprintf(app.Err, "failed: %s — %v\n", a.Target, err)
			}
		}
		return 0
	case "restore":
		log, _ := audit.New(auditDir())
		defer func() { _ = log.Close() }()
		for _, it := range rep.Items {
			a := modules.Action{
				Module: "startup", Op: "enable", Target: it.Path,
			}
			if *dryRun {
				_, _ = fmt.Fprintf(app.Out, "would enable: %s\n", a.Target)
				continue
			}
			_, err := m.Apply(ctx, a)
			outcome, errStr := outcomeOf(err)
			_ = log.Write(audit.Record{
				Module: "startup", Op: "enable", Target: a.Target,
				Outcome: outcome, Error: errStr,
			})
			if err == nil {
				_, _ = fmt.Fprintf(app.Out, "enabled: %s\n", a.Target)
			} else {
				_, _ = fmt.Fprintf(app.Err, "failed: %s — %v\n", a.Target, err)
			}
		}
		return 0
	default:
		_, _ = fmt.Fprintf(app.Err, "unknown startup subcommand %q\n", rest[0])
		return 2
	}
}
