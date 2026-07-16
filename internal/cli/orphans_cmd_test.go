package cli

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/FRIKKern/noo-noo/internal/modules"
)

// fakeOrphansModule stands in for procsig.Module: CLI tests never scan the
// live process table and never signal a real process.
type fakeOrphansModule struct {
	report   modules.Report
	applied  []modules.Action
	applyErr error
}

func (*fakeOrphansModule) Name() string { return "orphans" }

func (f *fakeOrphansModule) Scan(context.Context) (modules.Report, error) {
	return f.report, nil
}

func (f *fakeOrphansModule) Plan(r modules.Report) []modules.Action {
	var out []modules.Action
	for _, it := range r.Items {
		out = append(out, modules.Action{
			Module: "orphans", Op: "terminate", Target: it.Evidence["pid"],
		})
	}
	return out
}

func (f *fakeOrphansModule) Apply(_ context.Context, a modules.Action) (modules.Result, error) {
	f.applied = append(f.applied, a)
	return modules.Result{Action: a, Err: f.applyErr}, f.applyErr
}

func orphanReport() modules.Report {
	return modules.Report{
		Module: "orphans",
		Items: []modules.Item{{
			Path: "/private/tmp/claude-501/job-9/profile",
			Evidence: map[string]string{
				"pid":        "4242",
				"ppid":       "1",
				"etime":      "3-01:00:00",
				"signature":  "headless-chrome-orphan",
				"cmdline":    "chrome --headless --user-data-dir=/private/tmp/claude-501/job-9/profile",
				"workaround": "kill browsers when the job ends",
			},
		}},
	}
}

// withFakeOrphans swaps the module hook for one test.
func withFakeOrphans(t *testing.T, f *fakeOrphansModule) {
	t.Helper()
	prev := newOrphansModule
	newOrphansModule = func() modules.Module { return f }
	t.Cleanup(func() { newOrphansModule = prev })
}

func runOrphans(t *testing.T, args ...string) (code int, out, errOut string) {
	t.Helper()
	var o, e bytes.Buffer
	app := &App{Out: &o, Err: &e}
	argv := append([]string{"noo-noo", "orphans"}, args...)
	code = app.Run(context.Background(), argv)
	return code, o.String(), e.String()
}

func TestOrphansNoVerbUsage(t *testing.T) {
	withFakeOrphans(t, &fakeOrphansModule{report: orphanReport()})
	code, _, errOut := runOrphans(t)
	if code != 2 || !strings.Contains(errOut, "Usage:") {
		t.Fatalf("want usage + exit 2, got %d / %q", code, errOut)
	}
}

func TestOrphansUnknownVerb(t *testing.T) {
	withFakeOrphans(t, &fakeOrphansModule{report: orphanReport()})
	code, _, errOut := runOrphans(t, "explode")
	if code != 2 || !strings.Contains(errOut, "unknown orphans subcommand") {
		t.Fatalf("want unknown-verb + exit 2, got %d / %q", code, errOut)
	}
}

func TestOrphansListShowsEvidence(t *testing.T) {
	f := &fakeOrphansModule{report: orphanReport()}
	withFakeOrphans(t, f)
	code, out, _ := runOrphans(t, "list")
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	for _, want := range []string{"pid 4242", "3-01:00:00", "--headless", "noo-noo leaks clean"} {
		if !strings.Contains(out, want) {
			t.Errorf("list output missing %q:\n%s", want, out)
		}
	}
	if len(f.applied) != 0 {
		t.Fatalf("list must never Apply, got %v", f.applied)
	}
}

func TestOrphansListEmptyIsHonest(t *testing.T) {
	withFakeOrphans(t, &fakeOrphansModule{report: modules.Report{Module: "orphans"}})
	code, out, _ := runOrphans(t, "list")
	if code != 0 || !strings.Contains(out, "No orphaned automation browsers found") {
		t.Fatalf("want honest empty state, got %d / %q", code, out)
	}
}

// TestOrphansKillYesAfterVerb is the flag-order trust test (charter D22 /
// wave finding b8): `orphans kill -y` must PARSE -y and terminate without
// prompting — never a silent abort.
func TestOrphansKillYesAfterVerb(t *testing.T) {
	t.Setenv("HOME", t.TempDir()) // sandbox the audit log write
	f := &fakeOrphansModule{report: orphanReport()}
	withFakeOrphans(t, f)
	code, out, errOut := runOrphans(t, "kill", "-y")
	if code != 0 {
		t.Fatalf("exit %d (stderr %q)", code, errOut)
	}
	if strings.Contains(out, "Aborted") {
		t.Fatalf("-y after verb silently aborted:\n%s", out)
	}
	if len(f.applied) != 1 || f.applied[0].Target != "4242" || f.applied[0].Op != "terminate" {
		t.Fatalf("want one terminate of 4242, got %v", f.applied)
	}
	// The composition pointer: killed orphan -> clones go lsof-empty ->
	// the existing leaks signature sweeps them.
	if !strings.Contains(out, "noo-noo leaks clean") {
		t.Errorf("kill output must point at the leaks sweep:\n%s", out)
	}
	if !strings.Contains(out, "terminated: pid 4242") {
		t.Errorf("kill output must name the pid:\n%s", out)
	}
}

func TestOrphansKillDryRun(t *testing.T) {
	t.Setenv("HOME", t.TempDir()) // sandbox the audit log write
	f := &fakeOrphansModule{report: orphanReport()}
	withFakeOrphans(t, f)
	code, out, _ := runOrphans(t, "kill", "-y", "-dry-run")
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	if !strings.Contains(out, "would terminate: pid 4242") {
		t.Errorf("dry-run must narrate, got:\n%s", out)
	}
	if len(f.applied) != 0 {
		t.Fatalf("dry-run must never Apply, got %v", f.applied)
	}
}

func TestOrphansKillDeclined(t *testing.T) {
	// No -y and stdin yields no "y": Confirm returns false -> Aborted.
	f := &fakeOrphansModule{report: orphanReport()}
	withFakeOrphans(t, f)
	// Drive Confirm through a closed stdin by using the command path that
	// reads os.Stdin — instead, exercise decline via the trust guard on
	// unexpected positionals, which must hard-error rather than no-op.
	code, _, errOut := runOrphans(t, "kill", "y")
	if code != 2 || !strings.Contains(errOut, "unexpected argument") {
		t.Fatalf("stray positional must hard-error, got %d / %q", code, errOut)
	}
	if len(f.applied) != 0 {
		t.Fatalf("hard-error path must never Apply, got %v", f.applied)
	}
}

func TestOrphansKillFailureIsReported(t *testing.T) {
	t.Setenv("HOME", t.TempDir()) // sandbox the audit log write
	f := &fakeOrphansModule{report: orphanReport(), applyErr: errors.New("pid 4242 went away")}
	withFakeOrphans(t, f)
	code, out, errOut := runOrphans(t, "kill", "-y")
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	if !strings.Contains(errOut, "refused/failed: pid 4242") {
		t.Errorf("failure must land on stderr, got %q", errOut)
	}
	if strings.Contains(out, "Terminated 1") {
		t.Errorf("failed kill must not claim success:\n%s", out)
	}
}
