package leaks

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/FRIKKern/noo-noo/internal/core"
	"github.com/FRIKKern/noo-noo/internal/modules"
)

const (
	aliveUUID = "4549e2d9-58e6-4924-b33b-02b73e4f4755"
	deadUUID  = "0a1b2c3d-1111-2222-3333-444444444444"
)

func TestArgsCarrySession(t *testing.T) {
	ps := "/bin/zsh -c foo\nnode /x/cli.js --session-id " + aliveUUID + " --resume\nclaude --session-id=" + deadUUID + "a\n"
	if !argsCarrySession(ps, aliveUUID) {
		t.Error("space-separated --session-id not detected")
	}
	if !argsCarrySession("claude --session-id="+aliveUUID, aliveUUID) {
		t.Error("--session-id=<uuid> not detected")
	}
	if argsCarrySession(ps, deadUUID) {
		t.Error("uuid followed by extra hex char must not match")
	}
	if argsCarrySession("nothing here", aliveUUID) {
		t.Error("absent id matched")
	}
}

func TestTranscriptProbe(t *testing.T) {
	home := t.TempDir()
	proj := filepath.Join(home, ".claude", "projects", "-Users-x")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(proj, aliveUUID+".jsonl"), 10)
	old := time.Now().Add(-2 * time.Hour)
	writeFile(t, filepath.Join(proj, deadUUID+".jsonl"), 10)
	if err := os.Chtimes(filepath.Join(proj, deadUUID+".jsonl"), old, old); err != nil {
		t.Fatal(err)
	}
	probe := SessionProbeFor(home, time.Now)
	if alive, proof := probe(context.Background(), aliveUUID, 30*time.Minute); !alive {
		t.Errorf("fresh transcript must prove life, got %q", proof)
	}
	if alive, proof := probe(context.Background(), deadUUID, 30*time.Minute); alive {
		t.Errorf("2h-old transcript and no process must be dead, got %q", proof)
	}
	if alive, _ := probe(context.Background(), "ffffffff-0000-0000-0000-000000000000", 30*time.Minute); alive {
		t.Error("no transcript, no process: must be dead")
	}
}

// sessionFixture: a /private/tmp/claude-<uid>/<project>/<uuid> tree with a
// live session (idle + fresh scratch entries), a dead session, and a
// non-uuid gocache sibling under the same signature.
func sessionFixture(t *testing.T) (string, *Module) {
	t.Helper()
	base := resolvedTempDir(t)
	proj := filepath.Join(base, "claude-501", "-Users-x")
	old := time.Now().Add(-2 * time.Hour)
	ancient := time.Now().Add(-8 * 24 * time.Hour)
	mkTree := func(dir string, when time.Time) {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		writeFile(t, filepath.Join(dir, "f"), 4096)
		for _, p := range []string{filepath.Join(dir, "f"), dir} {
			if err := os.Chtimes(p, when, when); err != nil {
				t.Fatal(err)
			}
		}
	}
	alive := filepath.Join(proj, aliveUUID)
	mkTree(filepath.Join(alive, "scratchpad", "idle-build"), ancient)
	mkTree(filepath.Join(alive, "scratchpad", "fresh-notes"), time.Now())
	mkTree(filepath.Join(proj, deadUUID), old)
	mkTree(filepath.Join(proj, "foo-gocache-1"), ancient)

	sig := Signature{
		ID:        "test-scratch",
		Globs:     []string{filepath.Join(base, "claude-*", "*", "*")},
		Staleness: StaleWhenSessionDead,
		Session: &SessionRule{
			ScratchDir: "scratchpad", DeadAfter: time.Hour, EntryIdle: 24 * time.Hour, TranscriptWindow: 30 * time.Minute,
		},
		MinAge: 7 * 24 * time.Hour,
		Risk:   modules.RiskLow,
	}
	m := New([]Signature{sig}, core.NewSafety(nil, nil))
	m.probe = fakeProbe(false, "fake: nothing open")
	m.session = func(_ context.Context, uuid string, _ time.Duration) (bool, string) {
		if uuid == aliveUUID {
			return true, "fake: alive"
		}
		return false, "fake: dead"
	}
	return base, m
}

func TestSessionLivenessGate(t *testing.T) {
	base, m := sessionFixture(t)
	proj := filepath.Join(base, "claude-501", "-Users-x")
	rep, err := m.Scan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]modules.Item{}
	for _, it := range rep.Items {
		got[it.Path] = it
	}
	want := map[string]string{
		filepath.Join(proj, aliveUUID):                              "live",
		filepath.Join(proj, aliveUUID, "scratchpad", "idle-build"):  "stale",
		filepath.Join(proj, aliveUUID, "scratchpad", "fresh-notes"): "live",
		filepath.Join(proj, deadUUID):                               "stale",
		filepath.Join(proj, "foo-gocache-1"):                        "stale",
	}
	for p, w := range want {
		it, ok := got[p]
		if !ok {
			t.Errorf("missing item %s; have %v", p, keys(got))
			continue
		}
		if it.Evidence["staleness"] != w {
			t.Errorf("%s = %q, want %s (age=%q session=%q)", p, it.Evidence["staleness"], w, it.Evidence["age"], it.Evidence["session"])
		}
	}
	if len(got) != len(want) {
		t.Errorf("got %d items, want %d: %v", len(got), len(want), keys(got))
	}
	if ev := got[filepath.Join(proj, aliveUUID)].Evidence; ev["session_alive"] != "true" || ev["session_uuid"] != aliveUUID {
		t.Errorf("alive session evidence incomplete: %+v", ev)
	}
	if ev := got[filepath.Join(proj, deadUUID)].Evidence; ev["session_alive"] != "false" || ev["min_age"] != "1h0m0s" {
		t.Errorf("dead session must be gated by DeadAfter, got %+v", ev)
	}
	if ev := got[filepath.Join(proj, "foo-gocache-1")].Evidence; ev["staleness_rule"] != "aged+lsof-empty" || ev["min_age"] != "168h0m0s" {
		t.Errorf("non-uuid path must fall back to the 7d age gate, got %+v", ev)
	}

	// Plan: dead session, idle entry, gocache — never the live session or
	// its fresh entry.
	actions := m.Plan(rep)
	targets := map[string]bool{}
	for _, a := range actions {
		targets[a.Target] = true
	}
	if len(actions) != 3 || targets[filepath.Join(proj, aliveUUID)] || targets[filepath.Join(proj, aliveUUID, "scratchpad", "fresh-notes")] {
		t.Fatalf("Plan = %+v", actions)
	}

	// Apply the idle entry inside the live session: the entry rule stands
	// on its own, so it is deleted; the session dir itself is refused.
	entry := filepath.Join(proj, aliveUUID, "scratchpad", "idle-build")
	if _, err := m.Apply(context.Background(), modules.Action{Module: "leaks", Op: "delete", Target: entry}); err != nil {
		t.Fatalf("Apply idle entry: %v", err)
	}
	if _, err := os.Stat(entry); !os.IsNotExist(err) {
		t.Errorf("idle entry should be gone, stat err: %v", err)
	}
	if _, err := m.Apply(context.Background(), modules.Action{Module: "leaks", Op: "delete", Target: filepath.Join(proj, aliveUUID)}); err == nil || !strings.Contains(err.Error(), "LIVE") {
		t.Errorf("live session dir must be refused, got %v", err)
	}
}

func TestDeadSessionStillSettling(t *testing.T) {
	base, m := sessionFixture(t)
	proj := filepath.Join(base, "claude-501", "-Users-x")
	now := time.Now()
	if err := os.Chtimes(filepath.Join(proj, deadUUID, "f"), now, now); err != nil {
		t.Fatal(err)
	}
	rep, err := m.Scan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range rep.Items {
		if it.Path == filepath.Join(proj, deadUUID) && it.Evidence["staleness"] != "live" {
			t.Errorf("dead session written seconds ago must stay live until DeadAfter, got %+v", it.Evidence)
		}
	}
}
