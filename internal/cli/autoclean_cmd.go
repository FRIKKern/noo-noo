// auto-clean CLI subcommand: enable | disable | status | history.
//
// Wire path: cli.App.Run dispatches "auto-clean" -> autoCleanEntry, which
// dials the daemon socket and routes the verb to AutoCleanCmd. The IPC
// methods are AutoClean.Status and AutoClean.Toggle (server side defined in
// internal/ipc/autoclean_method.go).
//
// Tests inject a fake rpcCaller so they never need a live daemon.

package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/rpc"
	"net/rpc/jsonrpc"
	"time"

	"github.com/FRIKKern/noo-noo/internal/ipc"
)

func init() { Register("auto-clean", autoCleanEntry) }

// rpcCaller is the narrow surface AutoCleanCmd needs from the JSON-RPC
// client. *rpc.Client satisfies it natively; tests use a fake.
type rpcCaller interface {
	Call(serviceMethod string, args, reply any) error
	Close() error
}

// autoCleanDial is the dial hook; tests override it to inject a fake.
var autoCleanDial = func() (rpcCaller, error) {
	conn, err := net.Dial("unix", ipc.SocketEnv())
	if err != nil {
		return nil, fmt.Errorf("dial daemon socket: %w", err)
	}
	return jsonrpc.NewClient(conn), nil
}

// autoCleanEntry adapts AutoCleanCmd to the App.Run dispatcher signature.
func autoCleanEntry(_ context.Context, app *App, args []string) int {
	if err := AutoCleanCmd(app.Out, autoCleanDial, args); err != nil {
		_, _ = fmt.Fprintln(app.Err, err)
		return 1
	}
	return 0
}

// AutoCleanCmd dispatches `noo-noo auto-clean <verb>`.
func AutoCleanCmd(out io.Writer, dial func() (rpcCaller, error), args []string) error {
	if len(args) == 0 {
		return errors.New("usage: noo-noo auto-clean enable|disable|status|history")
	}
	verb, rest := args[0], args[1:]
	switch verb {
	case "enable":
		return autoCleanEnable(out, dial, rest)
	case "disable":
		return autoCleanDisable(out, dial)
	case "status":
		return autoCleanStatus(out, dial)
	case "history":
		return autoCleanHistory(out, dial, rest)
	default:
		return fmt.Errorf("unknown verb %q (try enable|disable|status|history)", verb)
	}
}

func autoCleanEnable(out io.Writer, dial func() (rpcCaller, error), args []string) error {
	fs := flag.NewFlagSet("enable", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	ack := fs.Bool("i-understand-the-risks", false,
		"required: confirm you have read the safety design")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if !*ack {
		return errors.New("auto-clean is disabled by default. To enable, you must " +
			"explicitly acknowledge that this allows the daemon to delete files. " +
			"Run: noo-noo auto-clean enable --i-understand-the-risks")
	}
	c, err := dial()
	if err != nil {
		return err
	}
	defer func() { _ = c.Close() }()
	var resp ipc.AutoCleanToggleResponse
	if err := c.Call("AutoClean.Toggle", ipc.AutoCleanToggleRequest{
		Enabled:        true,
		AcknowledgedAt: time.Now().UTC().Format(time.RFC3339),
	}, &resp); err != nil {
		return err
	}
	_, _ = fmt.Fprintln(out, "auto-clean enabled. Use 'noo-noo auto-clean status' to inspect.")
	_, _ = fmt.Fprintln(out, "Disable any time with 'noo-noo auto-clean disable' (takes effect within 60 s).")
	return nil
}

func autoCleanDisable(out io.Writer, dial func() (rpcCaller, error)) error {
	c, err := dial()
	if err != nil {
		return err
	}
	defer func() { _ = c.Close() }()
	var resp ipc.AutoCleanToggleResponse
	if err := c.Call("AutoClean.Toggle", ipc.AutoCleanToggleRequest{Enabled: false}, &resp); err != nil {
		return err
	}
	_, _ = fmt.Fprintln(out, "auto-clean disabled.")
	return nil
}

func autoCleanStatus(out io.Writer, dial func() (rpcCaller, error)) error {
	c, err := dial()
	if err != nil {
		return err
	}
	defer func() { _ = c.Close() }()
	var resp ipc.AutoCleanStatusResponse
	if err := c.Call("AutoClean.Status", ipc.AutoCleanStatusRequest{}, &resp); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(out, "auto-clean: %s\n", ynStr(resp.Enabled))
	_, _ = fmt.Fprintf(out, "ack timestamp: %s\n", ifBlank(resp.AcknowledgedAt, "(never)"))
	_, _ = fmt.Fprintf(out, "modules allowed: %v\n", resp.ModulesAllowed)
	_, _ = fmt.Fprintf(out, "min idle days: %d\n", resp.MinIdleDays)
	_, _ = fmt.Fprintf(out, "min size MB: %d\n", resp.MinSizeMB)
	_, _ = fmt.Fprintf(out, "size cap per tick: %d GB\n", resp.SizeCapPerTickGB)
	_, _ = fmt.Fprintf(out, "recent activity:\n  deletions last 7d: %d\n  bytes freed last 7d: %d\n",
		resp.Deletions7d, resp.FreedBytes7d)
	return nil
}

func autoCleanHistory(out io.Writer, dial func() (rpcCaller, error), args []string) error {
	fs := flag.NewFlagSet("history", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	limit := fs.Int("limit", 50, "rows to show")
	if err := fs.Parse(args); err != nil {
		return err
	}
	c, err := dial()
	if err != nil {
		return err
	}
	defer func() { _ = c.Close() }()
	var resp ipc.AutoCleanStatusResponse
	if err := c.Call("AutoClean.Status",
		ipc.AutoCleanStatusRequest{HistoryLimit: *limit}, &resp); err != nil {
		return err
	}
	// History rendering: today AutoCleanStatusResponse exposes only 7-day
	// aggregates (Deletions7d/FreedBytes7d). A future Events []EventRow
	// field will let us render per-row history; for now we summarize.
	_, _ = fmt.Fprintf(out, "auto-clean history (last 7d): %d deletions, %d bytes freed\n",
		resp.Deletions7d, resp.FreedBytes7d)
	return nil
}

func ynStr(b bool) string {
	if b {
		return "enabled"
	}
	return "disabled"
}

func ifBlank(s, alt string) string {
	if s == "" {
		return alt
	}
	return s
}

// Compile-time check: *rpc.Client satisfies rpcCaller.
var _ rpcCaller = (*rpc.Client)(nil)
