package procsig

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/FRIKKern/noo-noo/internal/modules"
)

// fakeRunner serves canned ps output — tests never scan the live process
// table and (except for the spawned-child Apply tests below) never signal
// a real process.
type fakeRunner struct {
	out string
	err error
}

func (f fakeRunner) Output(context.Context, []byte, string, ...string) ([]byte, error) {
	if f.err != nil {
		return nil, f.err
	}
	return []byte(f.out), nil
}

// killRecorder is a signal seam fake proving which signals were (not) sent.
type killRecorder struct {
	calls []string
	err   error
}

func (k *killRecorder) kill(pid int, sig syscall.Signal) error {
	k.calls = append(k.calls, fmt.Sprintf("%d:%d", pid, int(sig)))
	return k.err
}

// testSigs is a registry mirroring the shipped headless-browser signature,
// pinned to a fixed glob so tests don't depend on the machine's home dir.
func testSigs() []ProcessSignature {
	return []ProcessSignature{{
		ID:               "headless-chrome-orphan",
		Title:            "Orphaned headless browser",
		CmdlineContains:  []string{"--headless", "--user-data-dir="},
		UserDataDirGlobs: []string{"/private/tmp/*", "/var/folders/*/*/T/*"},
		Risk:             modules.RiskLow,
		Workaround:       "kill browsers when the job ends",
	}}
}

// testModule wires a Module entirely onto fakes.
func testModule(psOut string, kill *killRecorder) *Module {
	m := New(testSigs())
	m.runner = fakeRunner{out: psOut}
	if kill != nil {
		m.kill = kill.kill
	}
	m.termWait = 200 * time.Millisecond
	m.killWait = 200 * time.Millisecond
	m.pollEvery = 20 * time.Millisecond
	return m
}

const psHeader = "  PID  PPID   UID ELAPSED COMMAND\n"

func psRow(pid, ppid, uid int, etime, cmd string) string {
	return fmt.Sprintf("%5d %5d %5d %s %s\n", pid, ppid, uid, etime, cmd)
}

func TestParsePS(t *testing.T) {
	out := psHeader +
		psRow(42, 1, 501, "3-04:12:56", "/bin/thing --flag value") +
		"garbage line\n" +
		"1 2\n" + // too few fields
		psRow(43, 42, 501, "00:01", "sleep 60")
	procs := ParsePS(out)
	if len(procs) != 2 {
		t.Fatalf("want 2 rows, got %d: %+v", len(procs), procs)
	}
	p := procs[0]
	if p.PID != 42 || p.PPID != 1 || p.UID != 501 || p.Etime != "3-04:12:56" {
		t.Errorf("row 0 misparsed: %+v", p)
	}
	if p.Command != "/bin/thing --flag value" {
		t.Errorf("command misjoined: %q", p.Command)
	}
}

func TestUserDataDir(t *testing.T) {
	dir, ok := UserDataDir("chrome --headless --user-data-dir=/private/tmp/claude-501/job-1/profile --no-sandbox")
	if !ok || dir != "/private/tmp/claude-501/job-1/profile" {
		t.Errorf("got %q ok=%v", dir, ok)
	}
	if _, ok := UserDataDir("chrome --headless"); ok {
		t.Error("no user-data-dir must not extract")
	}
	if _, ok := UserDataDir("chrome --user-data-dir="); ok {
		t.Error("empty user-data-dir must not extract")
	}
}

// TestScan is the incident-shape table: the proven orphan matches; every
// near-miss (live parent, real profile, foreign uid, non-headless) does not.
func TestScan(t *testing.T) {
	uid := os.Getuid()
	orphanCmd := "/Applications/Chrome.app/Contents/MacOS/Chrome --headless --user-data-dir=/private/tmp/claude-501/job-9/profile about:blank"
	cases := []struct {
		name string
		row  string
		want int
	}{
		{"orphan headless in scratch matches", psRow(4242, 1, uid, "3-01:00:00", orphanCmd), 1},
		{"live parent does NOT match", psRow(4242, 999, uid, "3-01:00:00", orphanCmd), 0},
		{"real user profile does NOT match", psRow(4242, 1, uid, "01:00",
			"/Applications/Chrome.app/Contents/MacOS/Chrome --headless --user-data-dir=/Users/x/Library/Application Support/Chrome"), 0},
		{"foreign uid does NOT match", psRow(4242, 1, uid+1, "3-01:00:00", orphanCmd), 0},
		{"non-headless does NOT match", psRow(4242, 1, uid, "3-01:00:00",
			"/Applications/Chrome.app/Contents/MacOS/Chrome --user-data-dir=/private/tmp/claude-501/job-9/profile"), 0},
		{"headless without user-data-dir does NOT match", psRow(4242, 1, uid, "01:00",
			"/Applications/Chrome.app/Contents/MacOS/Chrome --headless about:blank"), 0},
		{"var-folders scratch matches", psRow(4243, 1, uid, "1-00:00:00",
			"chrome --headless --user-data-dir=/var/folders/ab/cd/T/agent-job/profile"), 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := testModule(psHeader+tc.row, &killRecorder{})
			rep, err := m.Scan(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if len(rep.Items) != tc.want {
				t.Fatalf("want %d item(s), got %d: %+v", tc.want, len(rep.Items), rep.Items)
			}
			if tc.want == 1 {
				it := rep.Items[0]
				if it.Evidence["pid"] == "" || it.Evidence["signature"] != "headless-chrome-orphan" {
					t.Errorf("evidence incomplete: %+v", it.Evidence)
				}
				if !strings.HasPrefix(it.Path, "/private/tmp/") && !strings.HasPrefix(it.Path, "/var/folders/") {
					t.Errorf("Path must be the user-data-dir, got %q", it.Path)
				}
			}
		})
	}
}

func TestPlanEmitsTerminate(t *testing.T) {
	uid := os.Getuid()
	m := testModule(psHeader+psRow(4242, 1, uid, "3-01:00:00",
		"chrome --headless --user-data-dir=/private/tmp/claude-501/job-9/profile"), &killRecorder{})
	rep, err := m.Scan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	actions := m.Plan(rep)
	if len(actions) != 1 {
		t.Fatalf("want 1 action, got %d", len(actions))
	}
	a := actions[0]
	if a.Op != "terminate" || a.Target != "4242" || a.Module != "orphans" {
		t.Errorf("bad action: %+v", a)
	}
}

// TestApplyRefusals proves the safety predicate: every refusal path exits
// WITHOUT a single signal being sent (the recorder stays empty).
func TestApplyRefusals(t *testing.T) {
	uid := os.Getuid()
	okCmd := "chrome --headless --user-data-dir=/private/tmp/claude-501/job-9/profile"
	action := func(target string) modules.Action {
		return modules.Action{Module: "orphans", Op: "terminate", Target: target}
	}
	cases := []struct {
		name    string
		psOut   string
		action  modules.Action
		wantErr string
	}{
		{"wrong op", psHeader, modules.Action{Module: "orphans", Op: "delete", Target: "4242"}, "unsupported op"},
		{"non-numeric target", psHeader, action("4242; rm -rf /"), "not a pid"},
		{"pid 1", psHeader, action("1"), "never signal"},
		{"pid 0", psHeader, action("0"), "never signal"},
		{"negative pid", psHeader, action("-1"), "never signal"},
		{"not running", psHeader, action("4242"), "not running"},
		{"foreign uid", psHeader + psRow(4242, 1, uid+1, "01:00", okCmd), action("4242"), "owned by uid"},
		{"regained a parent", psHeader + psRow(4242, 777, uid, "01:00", okCmd), action("4242"), "live parent"},
		{"cmdline changed (pid recycled)", psHeader + psRow(4242, 1, uid, "00:01", "postgres -D /data"), action("4242"), "no longer matches"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := &killRecorder{}
			m := testModule(tc.psOut, rec)
			_, err := m.Apply(context.Background(), tc.action)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("want error containing %q, got %v", tc.wantErr, err)
			}
			if len(rec.calls) != 0 {
				t.Fatalf("refusal path SENT SIGNALS: %v", rec.calls)
			}
		})
	}
}

func TestApplyRefusesSelf(t *testing.T) {
	rec := &killRecorder{}
	m := testModule(psHeader, rec)
	_, err := m.Apply(context.Background(),
		modules.Action{Module: "orphans", Op: "terminate", Target: strconv.Itoa(os.Getpid())})
	if err == nil || !strings.Contains(err.Error(), "own process") {
		t.Fatalf("want own-process refusal, got %v", err)
	}
	if len(rec.calls) != 0 {
		t.Fatalf("self-refusal SENT SIGNALS: %v", rec.calls)
	}
}

// spawnChild starts a throwaway child of THIS test and reaps it in the
// background, so kill(pid, 0) flips to ESRCH once it dies (an unreaped
// zombie would still answer). Returns the pid and a waiter channel.
func spawnChild(t *testing.T, name string, args ...string) (int, <-chan error) {
	t.Helper()
	cmd := exec.Command(name, args...)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		done <- cmd.Wait()
		close(done) // later receives return immediately (test + cleanup)
	}()
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
		}
	})
	return cmd.Process.Pid, done
}

// cannedFor fabricates the apply-time ps view for a real child pid: the ps
// row asserts the signature shape (ppid 1, our uid, headless cmdline) while
// the SIGNALS land on the real throwaway child — the only live process any
// test ever touches.
func cannedFor(pid int) string {
	return psHeader + psRow(pid, 1, os.Getuid(), "3-01:00:00",
		"chrome --headless --user-data-dir=/private/tmp/claude-501/job-9/profile")
}

// TestApplyTerminatesChild: a child that honors SIGTERM dies on the first
// signal; Apply reports success without escalating.
func TestApplyTerminatesChild(t *testing.T) {
	pid, done := spawnChild(t, "sleep", "60")
	m := testModule(cannedFor(pid), nil) // nil recorder = REAL syscall.Kill
	res, err := m.Apply(context.Background(),
		modules.Action{Module: "orphans", Op: "terminate", Target: strconv.Itoa(pid)})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if res.Err != nil {
		t.Fatalf("Result.Err: %v", res.Err)
	}
	select {
	case <-done:
		// child reaped — really dead
	case <-time.After(5 * time.Second):
		t.Fatal("child still running after Apply returned success")
	}
}

// TestApplyEscalatesToSigkill: a child that traps SIGTERM survives the
// grace period; Apply must escalate to SIGKILL and still succeed.
func TestApplyEscalatesToSigkill(t *testing.T) {
	pid, done := spawnChild(t, "/bin/sh", "-c", `trap "" TERM; sleep 30 & wait`)
	m := testModule(cannedFor(pid), nil) // real signals
	res, err := m.Apply(context.Background(),
		modules.Action{Module: "orphans", Op: "terminate", Target: strconv.Itoa(pid)})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if res.Err != nil {
		t.Fatalf("Result.Err: %v", res.Err)
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("child survived SIGKILL escalation")
	}
}
