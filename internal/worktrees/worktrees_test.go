package worktrees

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/FRIKKern/noo-noo/internal/modules"
)

func TestParseWorktreeList(t *testing.T) {
	out := strings.Join([]string{
		"worktree /repo",
		"HEAD aaaa",
		"branch refs/heads/main",
		"",
		"worktree /tmp/wt-detached",
		"HEAD bbbb",
		"detached",
		"",
		"worktree /tmp/wt-locked",
		"HEAD cccc",
		"branch refs/heads/x",
		"locked agent in flight",
		"",
		"worktree /tmp/wt-gone",
		"HEAD dddd",
		"branch refs/heads/y",
		"prunable gitdir file points to non-existent location",
	}, "\n")
	got := ParseWorktreeList(out)
	if len(got) != 4 {
		t.Fatalf("parsed %d rows, want 4", len(got))
	}
	if !got[0].Primary || got[0].Branch != "main" {
		t.Fatalf("row 0 = %+v", got[0])
	}
	if got[1].Primary || !got[1].Detached || got[1].Head != "bbbb" {
		t.Fatalf("row 1 = %+v", got[1])
	}
	if !got[2].Locked || got[2].Branch != "x" {
		t.Fatalf("row 2 = %+v", got[2])
	}
	if !got[3].Prunable {
		t.Fatalf("row 3 = %+v", got[3])
	}
}

// fakeGit scripts per-command responses; keys are the joined args.
func fakeGit(responses map[string]string) Git {
	return func(_ context.Context, _ string, args ...string) (string, error) {
		key := strings.Join(args, " ")
		if v, ok := responses[key]; ok {
			if strings.HasPrefix(v, "ERR:") {
				return "", fmt.Errorf("%s", v[4:])
			}
			return v, nil
		}
		return "", fmt.Errorf("fakeGit: unscripted call %q", key)
	}
}

func deadProbe(context.Context, string) (bool, string) { return false, "fake: nothing open" }
func liveProbe(context.Context, string) (bool, string) { return true, "fake: held open" }

// oldDir returns a real directory whose mtimes sit safely past the idle wall
// (Classify reads idleness from the filesystem even under a fake git).
func oldDir(t *testing.T) string {
	t.Helper()
	d := t.TempDir()
	past := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(d, past, past); err != nil {
		t.Fatal(err)
	}
	return d
}

func TestClassifyTiers(t *testing.T) {
	now := time.Now()
	minIdle := 12 * time.Hour

	// Primary and locked are ACTIVE unconditionally.
	for _, wt := range []Info{{Path: "/r", Primary: true}, {Path: "/r/wt", Locked: true}} {
		if _, c := Classify(context.Background(), fakeGit(nil), deadProbe, wt, now, minIdle); c != ClassActive {
			t.Fatalf("%+v classified %s, want active", wt, c)
		}
	}

	// Prunable: obvious with no git calls at all.
	if _, c := Classify(context.Background(), fakeGit(nil), deadProbe, Info{Path: "/gone", Prunable: true}, now, minIdle); c != ClassObvious {
		t.Fatal("prunable not obvious")
	}

	// Fresh mtime → ACTIVE before any git call.
	fresh := t.TempDir()
	if _, c := Classify(context.Background(), fakeGit(nil), deadProbe, Info{Path: fresh}, now, minIdle); c != ClassActive {
		t.Fatal("freshly-touched worktree not active")
	}

	clean := map[string]string{
		"status --porcelain":                 "",
		"log -1 --format=%cI %s":             "2026-07-01T00:00:00Z landed thing",
		"branch -r --contains HEAD":          "  origin/main",
		"rev-list --count @{upstream}..HEAD": "0",
	}

	// Clean + remote-contained + idle + lsof-empty → OBVIOUS.
	f, c := Classify(context.Background(), fakeGit(clean), deadProbe, Info{Path: oldDir(t), Branch: "x"}, now, minIdle)
	if c != ClassObvious || !f.RemoteContained || !f.Clean {
		t.Fatalf("class=%s facts=%+v, want obvious", c, f)
	}

	// Same but a process holds a file → ACTIVE (never judgment: mechanics
	// say wait, not think).
	if _, c := Classify(context.Background(), fakeGit(clean), liveProbe, Info{Path: oldDir(t), Branch: "x"}, now, minIdle); c != ClassActive {
		t.Fatal("lsof-held worktree not active")
	}

}

func TestClassifyJudgmentTier(t *testing.T) {
	now := time.Now()
	minIdle := 12 * time.Hour
	clean := map[string]string{
		"status --porcelain":                 "",
		"log -1 --format=%cI %s":             "2026-07-01T00:00:00Z landed thing",
		"branch -r --contains HEAD":          "  origin/main",
		"rev-list --count @{upstream}..HEAD": "0",
	}

	// Dirty → JUDGMENT.
	dirty := map[string]string{}
	for k, v := range clean {
		dirty[k] = v
	}
	dirty["status --porcelain"] = " M internal/thing.go\n?? scratch.log"
	f, c := Classify(context.Background(), fakeGit(dirty), deadProbe, Info{Path: oldDir(t), Branch: "x"}, now, minIdle)
	if c != ClassJudgment || !strings.Contains(f.DirtySummary, "2 dirty path(s)") {
		t.Fatalf("class=%s dirty=%q, want judgment", c, f.DirtySummary)
	}

	// Clean but NOT on any remote → JUDGMENT.
	local := map[string]string{}
	for k, v := range clean {
		local[k] = v
	}
	local["branch -r --contains HEAD"] = ""
	if _, c := Classify(context.Background(), fakeGit(local), deadProbe, Info{Path: oldDir(t), Branch: "x"}, now, minIdle); c != ClassJudgment {
		t.Fatal("remote-uncontained worktree not judgment")
	}

	// Ahead of upstream → JUDGMENT even when the HEAD commit is contained.
	ahead := map[string]string{}
	for k, v := range clean {
		ahead[k] = v
	}
	ahead["rev-list --count @{upstream}..HEAD"] = "3"
	if _, c := Classify(context.Background(), fakeGit(ahead), deadProbe, Info{Path: oldDir(t), Branch: "x"}, now, minIdle); c != ClassJudgment {
		t.Fatal("ahead-of-upstream worktree not judgment")
	}

	// Unreadable status → JUDGMENT (never a delete on a broken read).
	broken := map[string]string{"status --porcelain": "ERR:boom"}
	if _, c := Classify(context.Background(), fakeGit(broken), deadProbe, Info{Path: oldDir(t)}, now, minIdle); c != ClassJudgment {
		t.Fatal("status-unreadable worktree not judgment")
	}
}

func TestParseVerdict(t *testing.T) {
	for raw, want := range map[string]string{
		`{"verdict":"remove","reason":"spike residue"}`:                               "remove",
		"Sure! Here is my ruling:\n{\"verdict\":\"keep\",\"reason\":\"real work\"}\n": "keep",
	} {
		v, err := parseVerdict(raw)
		if err != nil || v.Verdict != want {
			t.Fatalf("parseVerdict(%q) = %+v, %v", raw, v, err)
		}
	}
	for _, bad := range []string{"", "no json here", `{"verdict":"maybe"}`} {
		if v, err := parseVerdict(bad); err == nil {
			t.Fatalf("parseVerdict(%q) accepted: %+v", bad, v)
		}
	}
}

// TestIntegrationRealGit builds a real repo with a bare "remote", one merged
// clean worktree (obvious) and one unpushed dirty worktree (judgment), then
// drives Scan → Plan → Apply and the grave path end-to-end.
func TestIntegrationRealGit(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	run := func(dir string, args ...string) string {
		t.Helper()
		out, err := ExecGit(context.Background(), dir, args...)
		if err != nil {
			t.Fatalf("git %v: %v", args, err)
		}
		return out
	}
	repo, wtA, wtB := buildFixtureRepo(t, root, run)

	m := New(Config{Roots: []string{root}, GraveDir: filepath.Join(root, "graves")})
	m.probe = deadProbe // hermetic: no lsof dependency in CI

	rep, err := m.Scan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	tiers := map[string]string{}
	for _, it := range rep.Items {
		tiers[filepath.Base(it.Path)] = it.Evidence["tier"]
	}
	if tiers["wt-merged"] != "obvious" || tiers["wt-dirty"] != "judgment" {
		t.Fatalf("tiers = %v", tiers)
	}

	// Apply removes the obvious one and deletes its merged branch.
	actions := m.Plan(rep)
	if len(actions) != 1 || actions[0].Target != wtA {
		t.Fatalf("plan = %+v", actions)
	}
	if _, err := m.Apply(context.Background(), actions[0]); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(wtA); !os.IsNotExist(err) {
		t.Fatal("obvious worktree still on disk")
	}

	// Apply on the judgment one must refuse (it is not in Plan, but a
	// forged action must not slip through either).
	if _, err := m.Apply(context.Background(), modules.Action{Module: "worktrees", Op: "delete", Target: wtB}); err == nil {
		t.Fatal("judgment worktree deleted by plain Apply")
	}

	assertGraveForceRemove(t, m, repo, wtB, run)
}

// assertGraveForceRemove drives the judged force path: grave first, then
// remove, with the unpushed commit provably recoverable from the bundle.
func assertGraveForceRemove(t *testing.T, m *Module, repo, wtB string, run func(string, ...string) string) {
	t.Helper()
	freed, bundle, tarball, err := m.ForceRemove(context.Background(), wtB)
	if err != nil {
		t.Fatal(err)
	}
	_ = freed
	if _, err := os.Stat(bundle); err != nil {
		t.Fatalf("grave bundle missing: %v", err)
	}
	if tarball == "" {
		t.Fatal("dirty worktree graved without a tarball")
	}
	if _, err := os.Stat(tarball); err != nil {
		t.Fatalf("grave tarball missing: %v", err)
	}
	if _, err := os.Stat(wtB); !os.IsNotExist(err) {
		t.Fatal("judged worktree still on disk")
	}
	// The unpushed commit is recoverable from the bundle.
	if out := run(repo, "bundle", "list-heads", bundle); !strings.Contains(out, "HEAD") {
		t.Fatalf("bundle heads: %q", out)
	}
}

// buildFixtureRepo makes a bare remote + primary clone with one merged
// clean worktree (obvious) and one unpushed dirty worktree (judgment),
// both aged past the idle wall.
func buildFixtureRepo(t *testing.T, root string, run func(string, ...string) string) (repo, wtA, wtB string) {
	t.Helper()
	bare := filepath.Join(root, "origin.git")
	if _, err := ExecGit(context.Background(), root, "init", "--bare", "-b", "main", bare); err != nil {
		t.Fatal(err)
	}
	repo = filepath.Join(root, "repo")
	run(root, "clone", bare, repo)
	run(repo, "config", "user.email", "t@t")
	run(repo, "config", "user.name", "t")
	if err := os.WriteFile(filepath.Join(repo, "f.txt"), []byte("v1"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(repo, "add", ".")
	run(repo, "commit", "-m", "init")
	run(repo, "push", "origin", "main")

	wtA = filepath.Join(root, "wt-merged")
	run(repo, "worktree", "add", "-b", "feat-a", wtA)
	run(wtA, "commit", "--allow-empty", "-m", "feat a")
	run(wtA, "push", "-u", "origin", "feat-a")

	wtB = filepath.Join(root, "wt-dirty")
	run(repo, "worktree", "add", "-b", "feat-b", wtB)
	run(wtB, "commit", "--allow-empty", "-m", "unpushed spike")
	if err := os.WriteFile(filepath.Join(wtB, "scratch note.log"), []byte("junk"), 0o644); err != nil {
		t.Fatal(err)
	}

	past := time.Now().Add(-48 * time.Hour)
	for _, p := range []string{wtA, wtB, filepath.Join(wtA, ".git"), filepath.Join(wtB, ".git")} {
		_ = os.Chtimes(p, past, past)
	}
	for _, name := range []string{"wt-merged", "wt-dirty"} {
		_ = os.Chtimes(filepath.Join(repo, ".git", "worktrees", name, "index"), past, past)
	}
	return repo, wtA, wtB
}

// TestPrimaryNeverRemovable pins the one inviolable rule.
func TestPrimaryNeverRemovable(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	repo := filepath.Join(root, "solo")
	if _, err := ExecGit(context.Background(), root, "init", "-b", "main", repo); err != nil {
		t.Fatal(err)
	}
	m := New(Config{Roots: []string{root}})
	if _, err := m.Apply(context.Background(), modules.Action{Module: "worktrees", Op: "delete", Target: repo}); err == nil || !strings.Contains(err.Error(), "PRIMARY") {
		t.Fatalf("primary checkout not refused: %v", err)
	}
}

// TestDiscoverNestedRepos pins the meta-repo layout: a folder that is
// itself a git repo with project clones inside must yield BOTH — stopping
// at the outer .git once hid 315 worktrees living one level deeper.
func TestDiscoverNestedRepos(t *testing.T) {
	root := t.TempDir()
	mk := func(parts ...string) {
		if err := os.MkdirAll(filepath.Join(parts...), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	mk(root, "meta", ".git")
	mk(root, "meta", "inner-a", ".git")
	mk(root, "meta", "sub", "inner-b", ".git")
	got := DiscoverRepos([]string{root}, 3)
	if len(got) != 3 {
		t.Fatalf("discovered %d repos (%v), want 3", len(got), got)
	}
}
