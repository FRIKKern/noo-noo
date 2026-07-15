package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

// The leaks command is registered and refuses to guess a subcommand.
// (Deliberately no scan-invoking test here: a real leaks scan probes the
// host with lsof, which belongs in the live diagnose run, not unit tests.)
func TestLeaksUsageWithoutSubcommand(t *testing.T) {
	var out, errOut bytes.Buffer
	app := &App{Out: &out, Err: &errOut}
	code := app.Run(context.Background(), []string{"noo-noo", "leaks"})
	if code != 2 {
		t.Errorf("expected exit 2, got %d", code)
	}
	if !strings.Contains(errOut.String(), "list|scan|clean") {
		t.Errorf("expected usage line in stderr, got: %s", errOut.String())
	}
}
