// Package cli is the command-line dispatcher. It owns flag parsing,
// subcommand routing, and exit codes.
package cli

import (
	"context"
	"fmt"
	"io"
)

// App is the CLI entry point. Stdout/stderr are injected for testability.
type App struct {
	Out io.Writer
	Err io.Writer
}

// Command is one subcommand handler. Returns the desired exit code.
type Command func(ctx context.Context, app *App, args []string) int

// commands wires subcommand names to their handlers. Populated by Register
// from each *_cmd.go file in init().
var commands = map[string]Command{}

// Register adds a subcommand. Called from init() in each command file.
func Register(name string, cmd Command) {
	commands[name] = cmd
}

// Run parses argv and dispatches.
func (a *App) Run(ctx context.Context, argv []string) int {
	if len(argv) < 2 {
		a.printUsage()
		return 0
	}
	name := argv[1]
	cmd, ok := commands[name]
	if !ok {
		_, _ = fmt.Fprintf(a.Err, "noo-noo: unknown command %q\n\n", name)
		a.printUsage()
		return 2
	}
	return cmd(ctx, a, argv[2:])
}

func (a *App) printUsage() {
	_, _ = fmt.Fprint(a.Out, `Usage: noo-noo <command> [args]

Commands:
  status                   Machine storage posture: disk fill rate, external
                           headroom, offload health, the honest verdict
  trends                   Growth sparklines, days-until-full forecast, and
                           recurring leak patterns from recorded history
  report                   Full diagnosis: memory (RAM, swap, compressor, top
                           processes) then every module below, truth-sized
  startup [list|disable|restore]
                           Manage launchd auto-start services
  caches  [list|clean]     Manage ~/Library/Caches targets
  dev     [list|clean]     Manage build artifacts under scan roots
  leaks   [list|scan|clean]
                           Find known leak classes (truth-sized, lsof-gated)
  worktrees [list|scan|clean|judge]
                           Find dead git worktrees (merged, clean, nothing open)
  orphans [list|scan|kill] Find orphaned automation browsers (headless,
                           parent job gone) and terminate them safely
  offload [scan|plan|apply|pending|run-pending|cancel]
                           Offload bulky assets to a pinned external volume
                           (stop-gated applies can be queued with --defer and
                           re-checked later via run-pending)
  suggestions [list|dismiss <id>]
                           Review the daemon's recorded cleanup suggestions
  auto-clean [enable|disable|status|history]
                           Opt-in automatic cleanup of very-high-confidence targets
  daemon  [start|stop|status|force-scan]
                           Control the running noo-nood
  install / uninstall      Register / remove the noo-nood LaunchAgent
                           (logs in ~/Library/Logs/noo-noo)

Global flags:
  -y         Skip confirmation prompts
  --json     Output NDJSON instead of human text
  --dry-run  Show what would happen without doing it
`)
}
