package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/FRIKKern/noo-noo/internal/ipc"
)

func init() { Register("daemon", daemonEntry) }

// daemonRunner is the small shell-out interface (mirrors internal/launchd.Runner
// so tests can swap it). Defined locally to avoid an import cycle.
type daemonRunner interface {
	Run(name string, args ...string) ([]byte, error)
}

type daemonExecRunner struct{}

func (daemonExecRunner) Run(name string, args ...string) ([]byte, error) {
	return exec.Command(name, args...).CombinedOutput()
}

type statusClient interface {
	DaemonStatus() (ipc.StatusResponse, error)
	Close() error
}

type daemonOpts struct {
	PlistPath string
	Runner    daemonRunner
	Dial      func() (statusClient, error)
	Out       io.Writer
}

type daemonCmd struct{ opts daemonOpts }

func newDaemonCmd(o daemonOpts) *daemonCmd {
	if o.Runner == nil {
		o.Runner = daemonExecRunner{}
	}
	if o.Out == nil {
		o.Out = os.Stdout
	}
	return &daemonCmd{opts: o}
}

// daemonEntry adapts the daemonCmd to the App.Run dispatcher signature.
func daemonEntry(_ context.Context, app *App, args []string) int {
	home, _ := os.UserHomeDir()
	cmd := newDaemonCmd(daemonOpts{
		PlistPath: filepath.Join(home, "Library", "LaunchAgents", "io.noo-noo.d.plist"),
		Runner:    daemonExecRunner{},
		Dial: func() (statusClient, error) {
			c, err := ipc.Dial(ipc.SocketEnv())
			if err != nil {
				return nil, err
			}
			return c, nil
		},
		Out: app.Out,
	})
	if err := cmd.Run(args); err != nil {
		_, _ = fmt.Fprintln(app.Err, err)
		return 1
	}
	return 0
}

func (d *daemonCmd) Run(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: noo-noo daemon [start|stop|status]")
	}
	switch args[0] {
	case "start":
		out, err := d.opts.Runner.Run("launchctl", "load", d.opts.PlistPath)
		if err != nil {
			return fmt.Errorf("launchctl load: %w (%s)", err, out)
		}
		_, _ = fmt.Fprintln(d.opts.Out, "Daemon started.")
		return nil
	case "stop":
		out, err := d.opts.Runner.Run("launchctl", "unload", d.opts.PlistPath)
		if err != nil {
			return fmt.Errorf("launchctl unload: %w (%s)", err, out)
		}
		_, _ = fmt.Fprintln(d.opts.Out, "Daemon stopped.")
		return nil
	case "status":
		c, err := d.opts.Dial()
		if err != nil {
			return fmt.Errorf("daemon not reachable: %w", err)
		}
		defer func() { _ = c.Close() }()
		s, err := c.DaemonStatus()
		if err != nil {
			return err
		}
		_, _ = fmt.Fprintf(d.opts.Out, "noo-nood: running=%t version=%s uptime=%s\n",
			s.Running, s.Version, s.Uptime)
		return nil
	default:
		return fmt.Errorf("unknown subcommand %q", args[0])
	}
}
