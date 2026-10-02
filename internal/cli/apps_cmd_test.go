package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

// The apps command is registered and refuses to guess a subcommand. (No
// scan-invoking test: a real apps scan runs mdfind and walks ~/Library.)
func TestAppsUsageWithoutSubcommand(t *testing.T) {
	var out, errOut bytes.Buffer
	app := &App{Out: &out, Err: &errOut}
	code := app.Run(context.Background(), []string{"noo-noo", "apps"})
	if code != 2 {
		t.Errorf("expected exit 2, got %d", code)
	}
	if !strings.Contains(errOut.String(), "list|clean") {
		t.Errorf("expected usage line in stderr, got: %s", errOut.String())
	}
	code = app.Run(context.Background(), []string{"noo-noo", "apps", "wat"})
	if code != 2 || !strings.Contains(errOut.String(), "unknown apps subcommand") {
		t.Errorf("unknown verb: code %d, stderr %s", code, errOut.String())
	}
}
