// Package worktrees finds git worktrees that are finished and will never be
// used again, removes the provably-dead ones, and packages everything
// ambiguous into evidence dossiers an AI judge (or a human) can rule on.
//
// The founding population: one repo carried 315 registered worktrees — wave
// builders, workflow agents and probe scripts each mint one and nothing ever
// swept them; the workflow-worktree dir alone held 30.8 GB.
//
// Two tiers, by proof standard:
//
//   - OBVIOUS — removable instinctively. Every condition is mechanical: not
//     the primary checkout, not locked, working tree clean, HEAD (and its
//     branch tip, when one exists) reachable from some REMOTE ref — the work
//     is on a server, deleting the checkout destroys nothing — idle past the
//     gate, and no process holds a file open under it. `git worktree remove`
//     (never --force) is the deleter, so git's own dirty/locked wall re-runs
//     at apply time.
//
//   - JUDGMENT — dirty files, unpushed commits, no remote containment, or a
//     branch ahead of its upstream. Mechanics cannot rule here; the module
//     emits a dossier (branch, ahead counts, dirty summary, age, size) and
//     defers. An operator-configured judge command (e.g. headless claude)
//     can rule remove/keep; a remove verdict is applied ONLY after the
//     committed state is grave-bundled and dirty files are archived, so a
//     wrong verdict loses nothing.
package worktrees

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Git runs one git invocation rooted at dir and returns trimmed stdout.
// Injectable so classification logic is testable without a filesystem.
type Git func(ctx context.Context, dir string, args ...string) (string, error)

// ExecGit is the production Git: plain exec, 30s cap per call.
// --no-optional-locks matters: a plain `git status` REFRESHES the index,
// which is one of the three mtimes idleFor reads — without the flag every
// scan reset the idle clock it was measuring and no worktree could ever
// leave ACTIVE (caught by the integration test's plan→apply re-check).
func ExecGit(ctx context.Context, dir string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir, "--no-optional-locks"}, args...)...)
	out, err := cmd.Output()
	if err != nil {
		var stderr string
		if ee, ok := err.(*exec.ExitError); ok {
			stderr = strings.TrimSpace(string(ee.Stderr))
		}
		return "", fmt.Errorf("git -C %s %s: %w (%s)", dir, strings.Join(args, " "), err, stderr)
	}
	return strings.TrimSpace(string(out)), nil
}

// Info is one worktree row parsed from `git worktree list --porcelain`.
type Info struct {
	Path     string
	Head     string
	Branch   string // "refs/heads/x" trimmed to "x"; empty when detached
	Detached bool
	Locked   bool
	Prunable bool
	Primary  bool // first row = the main checkout; NEVER a candidate
}

// ParseWorktreeList parses `git worktree list --porcelain` output.
func ParseWorktreeList(out string) []Info {
	var list []Info
	var cur *Info
	flush := func() {
		if cur != nil {
			list = append(list, *cur)
			cur = nil
		}
	}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimRight(line, "\r")
		switch {
		case strings.HasPrefix(line, "worktree "):
			flush()
			cur = &Info{Path: strings.TrimPrefix(line, "worktree "), Primary: len(list) == 0}
		case cur == nil:
			continue
		case strings.HasPrefix(line, "HEAD "):
			cur.Head = strings.TrimPrefix(line, "HEAD ")
		case strings.HasPrefix(line, "branch "):
			cur.Branch = strings.TrimPrefix(strings.TrimPrefix(line, "branch "), "refs/heads/")
		case line == "detached":
			cur.Detached = true
		case line == "locked" || strings.HasPrefix(line, "locked "):
			cur.Locked = true
		case line == "prunable" || strings.HasPrefix(line, "prunable "):
			cur.Prunable = true
		}
	}
	flush()
	return list
}

// Class is the tier a worktree lands in.
type Class int

const (
	// ClassActive: primary, locked, or too recently touched — not a
	// candidate at all this pass.
	ClassActive Class = iota
	// ClassObvious: every mechanical proof holds; removable instinctively.
	ClassObvious
	// ClassJudgment: something only judgment can rule on (dirty files,
	// unpushed commits, no remote containment).
	ClassJudgment
)

func (c Class) String() string {
	switch c {
	case ClassActive:
		return "active"
	case ClassObvious:
		return "obvious"
	case ClassJudgment:
		return "judgment"
	default:
		return "unknown"
	}
}

// Prober reports whether any process holds a file open under dir — same
// contract as the leaks module's prober, injectable for tests.
type Prober func(ctx context.Context, dir string) (live bool, proof string)

// Facts is everything Classify establishes about one worktree, kept so the
// evidence trail (and the judge dossier) cites data, not conclusions.
type Facts struct {
	Info            Info
	Clean           bool
	DirtySummary    string // first few `status --porcelain` lines + count
	RemoteContained bool   // HEAD reachable from some remote ref
	AheadUpstream   int    // commits ahead of @{upstream}; -1 = no upstream
	IdleFor         time.Duration
	LastCommit      string // "<iso date> <subject>"
	LiveProof       string // lsof verdict, only consulted for obvious tier
}

// Classify establishes the facts for one worktree and assigns its tier.
// minIdle is the freshness wall: anything touched more recently is ACTIVE
// regardless of other proofs, because a wave that is between operations
// looks clean+merged for minutes at a time.
func Classify(ctx context.Context, git Git, probe Prober, wt Info, now time.Time, minIdle time.Duration) (Facts, Class) {
	f := Facts{Info: wt, AheadUpstream: -1}
	if wt.Primary || wt.Locked {
		return f, ClassActive
	}
	if wt.Prunable {
		// Directory already gone; `git worktree prune` handles it. Report
		// as obvious with zero size — removing the registration is free.
		f.Clean = true
		f.RemoteContained = true
		return f, ClassObvious
	}

	f.IdleFor = idleFor(wt.Path, now)
	if f.IdleFor < minIdle {
		return f, ClassActive
	}

	if !gatherFacts(ctx, git, wt, &f) {
		return f, ClassJudgment
	}
	if !f.Clean || !f.RemoteContained || f.AheadUpstream > 0 {
		return f, ClassJudgment
	}

	// Final wall for the obvious tier: nothing may hold a file open here.
	live, proof := probe(ctx, wt.Path)
	f.LiveProof = proof
	if live {
		return f, ClassActive
	}
	return f, ClassObvious
}

// gatherFacts runs the git side of classification into f. false means git
// itself could not read the worktree — a judgment call, never a delete.
func gatherFacts(ctx context.Context, git Git, wt Info, f *Facts) bool {
	status, err := git(ctx, wt.Path, "status", "--porcelain")
	if err != nil {
		f.DirtySummary = "status unreadable: " + err.Error()
		return false
	}
	f.Clean = status == ""
	if !f.Clean {
		f.DirtySummary = dirtySummary(status)
	}
	if out, err := git(ctx, wt.Path, "log", "-1", "--format=%cI %s"); err == nil {
		f.LastCommit = out
	}
	// Remote containment: the HEAD commit reachable from ANY remote ref
	// means the committed work lives on a server and the checkout is a
	// disposable copy. Local-only merges do NOT count — cycles have
	// stranded built work on local branches before.
	if out, err := git(ctx, wt.Path, "branch", "-r", "--contains", "HEAD"); err == nil && strings.TrimSpace(out) != "" {
		f.RemoteContained = true
	}
	// A branch checked out here may sit AHEAD of the commit remote
	// containment vouched for — count against its upstream when one exists.
	if !wt.Detached && wt.Branch != "" {
		if out, err := git(ctx, wt.Path, "rev-list", "--count", "@{upstream}..HEAD"); err == nil {
			if n, perr := strconv.Atoi(strings.TrimSpace(out)); perr == nil {
				f.AheadUpstream = n
			}
		}
	}
	return true
}

// idleFor derives idleness from the cheap signals git activity always
// touches: the worktree root, its .git link file, and the per-worktree
// index. A full tree walk would pay _build/node_modules tax on every scan
// for no additional truth — any git operation refreshes the index.
func idleFor(wtPath string, now time.Time) time.Duration {
	newest := time.Time{}
	consider := func(p string) {
		if fi, err := os.Lstat(p); err == nil && fi.ModTime().After(newest) {
			newest = fi.ModTime()
		}
	}
	consider(wtPath)
	consider(filepath.Join(wtPath, ".git"))
	if b, err := os.ReadFile(filepath.Join(wtPath, ".git")); err == nil {
		if dir, ok := strings.CutPrefix(strings.TrimSpace(string(b)), "gitdir: "); ok {
			consider(filepath.Join(dir, "index"))
		}
	}
	if newest.IsZero() {
		return 0 // unreadable → treat as just-touched (fail toward ACTIVE)
	}
	return now.Sub(newest)
}

// dirtySummary compresses `status --porcelain` output into evidence: total
// count plus the first few paths, enough for a judge to smell junk vs work.
func dirtySummary(status string) string {
	lines := strings.Split(status, "\n")
	head := lines
	if len(head) > 5 {
		head = head[:5]
	}
	return fmt.Sprintf("%d dirty path(s): %s", len(lines), strings.Join(head, "; "))
}

// DiscoverRepos walks roots (depth-capped) for primary git checkouts. A
// repo's worktree list then names every worktree WHEREVER it lives — /tmp,
// scratchpads — so only the repos themselves need discovering.
func DiscoverRepos(roots []string, maxDepth int) []string {
	var repos []string
	seen := map[string]bool{}
	for _, root := range roots {
		walk(root, 0, maxDepth, seen, &repos)
	}
	return repos
}

func walk(dir string, depth, maxDepth int, seen map[string]bool, out *[]string) {
	if depth > maxDepth || seen[dir] {
		return
	}
	seen[dir] = true
	fi, err := os.Stat(filepath.Join(dir, ".git"))
	if err == nil && fi.IsDir() {
		*out = append(*out, dir)
		return // a repo's SUBDIRS are its own business (worktree list covers them)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() && !strings.HasPrefix(e.Name(), ".") {
			walk(filepath.Join(dir, e.Name()), depth+1, maxDepth, seen, out)
		}
	}
}
