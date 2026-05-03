package cli

import (
	"context"
	"fmt"
	"io"
	"strconv"
	"text/tabwriter"
	"time"

	"github.com/FRIKKern/noo-noo/internal/heuristics"
	"github.com/FRIKKern/noo-noo/internal/ipc"
)

func init() { Register("suggestions", suggestionsEntry) }

type suggClient interface {
	SuggestionsList() ([]heuristics.Suggestion, error)
	SuggestionsDismiss(id int64) error
	Close() error
}

type suggOpts struct {
	Dial func() (suggClient, error)
	Out  io.Writer
}

type suggestionsCmd struct{ opts suggOpts }

func newSuggestionsCmd(o suggOpts) *suggestionsCmd { return &suggestionsCmd{opts: o} }

// suggestionsEntry adapts the suggestionsCmd to the App.Run dispatcher signature.
func suggestionsEntry(_ context.Context, app *App, args []string) int {
	cmd := newSuggestionsCmd(suggOpts{
		Dial: func() (suggClient, error) {
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

func (s *suggestionsCmd) Run(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: noo-noo suggestions [list|dismiss <id>]")
	}
	c, err := s.opts.Dial()
	if err != nil {
		return fmt.Errorf("connect daemon: %w", err)
	}
	defer func() { _ = c.Close() }()

	switch args[0] {
	case "list":
		sugs, err := c.SuggestionsList()
		if err != nil {
			return err
		}
		return s.renderList(sugs)
	case "dismiss":
		if len(args) < 2 {
			return fmt.Errorf("dismiss requires an id")
		}
		id, err := strconv.ParseInt(args[1], 10, 64)
		if err != nil {
			return fmt.Errorf("parse id: %w", err)
		}
		if err := c.SuggestionsDismiss(id); err != nil {
			return err
		}
		_, _ = fmt.Fprintf(s.opts.Out, "Dismissed suggestion %d.\n", id)
		return nil
	default:
		return fmt.Errorf("unknown subcommand %q", args[0])
	}
}

func (s *suggestionsCmd) renderList(sugs []heuristics.Suggestion) error {
	if len(sugs) == 0 {
		_, _ = fmt.Fprintln(s.opts.Out, "No open suggestions. Disk looks tidy.")
		return nil
	}
	tw := tabwriter.NewWriter(s.opts.Out, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "ID\tMODULE\tRISK\tAGE\tTARGET\tREASON")
	for _, sg := range sugs {
		_, _ = fmt.Fprintf(tw, "%d\t%s\t%s\t%s\t%s\t%s\n",
			sg.ID, sg.Module, sg.RiskLevel,
			time.Since(sg.CreatedAt).Round(time.Hour),
			sg.Target, sg.Reason,
		)
	}
	return tw.Flush()
}

// Compile-time check: *ipc.Client satisfies suggClient. The interface uses
// heuristics.Suggestion, and ipc.SuggestionAlias is an alias for it, so the
// method sets line up.
var _ suggClient = (*ipc.Client)(nil)
