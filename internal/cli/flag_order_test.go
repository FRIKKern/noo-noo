package cli

import (
	"bytes"
	"context"
	"flag"
	"strings"
	"testing"
)

// bindTestFlags mirrors the flag shape every clean-family subcommand binds
// (json / y / dry-run, plus dev's roots) so parseVerb can be exercised in
// isolation without touching the host.
func bindTestFlags(app *App) (fs *flag.FlagSet, yes, asJSON, dryRun *bool, roots *string) {
	fs = flag.NewFlagSet("test", flag.ContinueOnError)
	fs.SetOutput(app.Err)
	asJSON = fs.Bool("json", false, "")
	yes = fs.Bool("y", false, "")
	dryRun = fs.Bool("dry-run", false, "")
	roots = fs.String("roots", "default", "")
	return
}

// parseVerb is the one seam all five subcommands share, so proving it here
// proves the verb-first fix for caches/dev/leaks/offload/startup at once. Each
// row is the NATURAL "verb then flags" order that was silently broken on main,
// plus the legacy "flags then verb" order that must stay green, plus the
// hard-error paths.
func TestParseVerbOrdering(t *testing.T) {
	type want struct {
		verb   string
		ok     bool
		code   int
		yes    bool
		dryRun bool
		roots  string
	}
	tests := []struct {
		name string
		args []string
		want want
	}{
		{"natural verb then -y", []string{"clean", "-y"},
			want{verb: "clean", ok: true, yes: true, roots: "default"}},
		{"natural verb then -y -dry-run", []string{"clean", "-y", "-dry-run"},
			want{verb: "clean", ok: true, yes: true, dryRun: true, roots: "default"}},
		{"natural verb then -roots value", []string{"list", "-roots", "/tmp/x"},
			want{verb: "list", ok: true, roots: "/tmp/x"}},
		{"legacy flags then verb (masking order)", []string{"-y", "list"},
			want{verb: "list", ok: true, yes: true, roots: "default"}},
		{"legacy -roots value then verb", []string{"-roots", "/tmp/x", "list"},
			want{verb: "list", ok: true, roots: "/tmp/x"}},
		{"interspersed flags around verb", []string{"-json", "clean", "-y"},
			want{verb: "clean", ok: true, yes: true, roots: "default"}},
		{"bare verb only", []string{"scan"},
			want{verb: "scan", ok: true, roots: "default"}},
		{"trailing bare dash is a positional not a flag", []string{"list", "-"},
			want{verb: "list", ok: true, roots: "default"}},
		{"empty args -> missing verb", []string{},
			want{ok: false, code: 2}},
		{"only flags, no verb", []string{"-y"},
			want{ok: false, code: 2}},
		{"unknown flag after verb hard-errors", []string{"list", "--bogus"},
			want{ok: false, code: 2}},
		{"unknown flag before verb hard-errors", []string{"--bogus", "list"},
			want{ok: false, code: 2}},
		{"flag hidden behind -- terminator hard-errors", []string{"list", "--", "-y"},
			want{ok: false, code: 2}},
		{"positional behind -- terminator is tolerated", []string{"list", "--", "leftover"},
			want{verb: "list", ok: true, roots: "default"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out, errOut bytes.Buffer
			app := &App{Out: &out, Err: &errOut}
			fs, yes, _, dryRun, roots := bindTestFlags(app)
			verb, code, ok := parseVerb(app, fs, tt.args, "usage")
			if ok != tt.want.ok {
				t.Fatalf("ok=%v, want %v (stderr: %s)", ok, tt.want.ok, errOut.String())
			}
			if !tt.want.ok {
				if code != tt.want.code {
					t.Errorf("code=%d, want %d", code, tt.want.code)
				}
				return
			}
			if verb != tt.want.verb {
				t.Errorf("verb=%q, want %q", verb, tt.want.verb)
			}
			if *yes != tt.want.yes {
				t.Errorf("yes=%v, want %v (the founding -y trust bug)", *yes, tt.want.yes)
			}
			if *dryRun != tt.want.dryRun {
				t.Errorf("dryRun=%v, want %v", *dryRun, tt.want.dryRun)
			}
			if *roots != tt.want.roots {
				t.Errorf("roots=%q, want %q", *roots, tt.want.roots)
			}
		})
	}
}

// Every one of the five subcommands must reject a flag-looking token that
// followed the verb rather than silently no-op. Parsing happens before any
// host scan, so this is deterministic without a live daemon or real caches.
func TestSubcommandsHardErrorOnUnknownFlagAfterVerb(t *testing.T) {
	cases := []struct {
		cmd  string
		verb string
	}{
		{"caches", "list"},
		{"dev", "list"},
		{"leaks", "list"},
		{"offload", "scan"},
		{"startup", "list"},
	}
	for _, c := range cases {
		t.Run(c.cmd, func(t *testing.T) {
			var out, errOut bytes.Buffer
			app := &App{Out: &out, Err: &errOut}
			code := app.Run(context.Background(),
				[]string{"noo-noo", c.cmd, c.verb, "--bogus"})
			if code != 2 {
				t.Fatalf("%s %s --bogus: exit %d, want 2 (stdout: %s / stderr: %s)",
					c.cmd, c.verb, code, out.String(), errOut.String())
			}
			if !strings.Contains(errOut.String(), "bogus") {
				t.Errorf("%s: expected the offending flag named in stderr, got: %s", c.cmd, errOut.String())
			}
		})
	}
}
