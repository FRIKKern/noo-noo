package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestDispatchUnknownCommand(t *testing.T) {
	var out, errOut bytes.Buffer
	app := &App{Out: &out, Err: &errOut}
	code := app.Run(context.Background(), []string{"noo-noo", "wat"})
	if code == 0 {
		t.Error("expected non-zero exit")
	}
	if !strings.Contains(errOut.String(), "unknown") {
		t.Errorf("expected 'unknown' in stderr, got: %s", errOut.String())
	}
}

func TestDispatchHelp(t *testing.T) {
	var out, errOut bytes.Buffer
	app := &App{Out: &out, Err: &errOut}
	code := app.Run(context.Background(), []string{"noo-noo"})
	if code != 0 {
		t.Errorf("expected exit 0 for no-arg help, got %d", code)
	}
	if !strings.Contains(out.String(), "Usage:") {
		t.Errorf("expected 'Usage:' in stdout, got: %s", out.String())
	}
}
