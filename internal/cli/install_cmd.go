package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/FRIKKern/noo-noo/internal/launchd"
)

func init() {
	Register("install", installEntry)
	Register("uninstall", uninstallEntry)
}

const launchAgentLabel = "io.noo-noo.d"

type installOpts struct {
	LaunchAgentDir string           // default: ~/Library/LaunchAgents
	ProgramPath    string           // default: result of os.Executable, swapped to noo-nood
	Runner         *launchd.Manager // launchctl wrapper from internal/launchd
	Out            io.Writer
}

type installCmd struct{ opts installOpts }
type uninstallCmd struct{ opts installOpts }

func newInstallCmd(o installOpts) *installCmd     { return &installCmd{opts: o} }
func newUninstallCmd(o installOpts) *uninstallCmd { return &uninstallCmd{opts: o} }

// installEntry adapts installCmd to the App.Run dispatcher signature.
func installEntry(_ context.Context, app *App, args []string) int {
	home, _ := os.UserHomeDir()
	cmd := newInstallCmd(installOpts{
		LaunchAgentDir: filepath.Join(home, "Library", "LaunchAgents"),
		ProgramPath:    defaultDaemonPath(),
		Runner:         launchd.New(),
		Out:            app.Out,
	})
	if err := cmd.Run(args); err != nil {
		_, _ = fmt.Fprintln(app.Err, err)
		return 1
	}
	return 0
}

// uninstallEntry adapts uninstallCmd to the App.Run dispatcher signature.
func uninstallEntry(_ context.Context, app *App, args []string) int {
	home, _ := os.UserHomeDir()
	cmd := newUninstallCmd(installOpts{
		LaunchAgentDir: filepath.Join(home, "Library", "LaunchAgents"),
		Runner:         launchd.New(),
		Out:            app.Out,
	})
	if err := cmd.Run(args); err != nil {
		_, _ = fmt.Fprintln(app.Err, err)
		return 1
	}
	return 0
}

func defaultDaemonPath() string {
	exe, err := os.Executable()
	if err != nil {
		return "/usr/local/bin/noo-nood"
	}
	return filepath.Join(filepath.Dir(exe), "noo-nood")
}

func (c *installCmd) Run(_ []string) error {
	if err := os.MkdirAll(c.opts.LaunchAgentDir, 0o755); err != nil {
		return fmt.Errorf("mkdir LaunchAgents: %w", err)
	}
	plist, err := launchd.GeneratePlist(launchAgentLabel, c.opts.ProgramPath, nil, true, true)
	if err != nil {
		return fmt.Errorf("generate plist: %w", err)
	}
	plistPath := filepath.Join(c.opts.LaunchAgentDir, launchAgentLabel+".plist")
	if err := os.WriteFile(plistPath, plist, 0o644); err != nil {
		return fmt.Errorf("write plist: %w", err)
	}
	if err := c.opts.Runner.Install(plistPath); err != nil {
		return fmt.Errorf("launchctl bootstrap: %w", err)
	}
	_, _ = fmt.Fprintf(c.opts.Out, "Installed %s. Daemon will start at login.\n", plistPath)
	return nil
}

func (c *uninstallCmd) Run(_ []string) error {
	plistPath := filepath.Join(c.opts.LaunchAgentDir, launchAgentLabel+".plist")
	// Best-effort bootout, then remove.
	_ = c.opts.Runner.Uninstall(plistPath)
	if err := os.Remove(plistPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove plist: %w", err)
	}
	_, _ = fmt.Fprintf(c.opts.Out, "Uninstalled %s.\n", launchAgentLabel)
	return nil
}
