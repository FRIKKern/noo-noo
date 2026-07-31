package worktrees

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/FRIKKern/noo-noo/internal/core"
	"github.com/FRIKKern/noo-noo/internal/leaks"
	"github.com/FRIKKern/noo-noo/internal/modules"
	"github.com/FRIKKern/noo-noo/internal/sizer"
)

// Config is the [worktrees] TOML section, re-declared here so the module
// does not import internal/config (mirrors autoclean's pattern).
type Config struct {
	// Roots to discover repos under. Empty = caller supplies scan roots.
	Roots []string
	// MinIdle is the freshness wall for candidacy (default 12h).
	MinIdle time.Duration
	// JudgeCmd is the operator-configured AI judge (e.g. "claude -p").
	// Empty = the judgment tier is report-only.
	JudgeCmd string
	// GraveDir receives bundles/archives before any judged force-removal.
	GraveDir string
}

// Module implements the modules.Module contract over the worktree tiers.
type Module struct {
	cfg   Config
	git   Git
	probe Prober
	now   func() time.Time
}

// New wires the production module. probe defaults to the leaks module's
// LsofProbe — the identical fail-safe liveness contract.
func New(cfg Config) *Module {
	if cfg.MinIdle <= 0 {
		cfg.MinIdle = 12 * time.Hour
	}
	return &Module{cfg: cfg, git: ExecGit, probe: leaks.LsofProbe, now: time.Now}
}

func (*Module) Name() string { return "worktrees" }

// Scan discovers repos, lists their worktrees, classifies each, and reports
// every OBVIOUS and JUDGMENT worktree with its full evidence trail. Active
// worktrees are silent — reporting the primary checkout of every repo as
// "not deletable" would be noise, not information.
func (m *Module) Scan(ctx context.Context) (modules.Report, error) {
	rep := modules.Report{Module: "worktrees"}
	for _, repo := range DiscoverRepos(m.cfg.Roots, 3) {
		out, err := m.git(ctx, repo, "worktree", "list", "--porcelain")
		if err != nil {
			continue // an unreadable repo yields nothing, never a guess
		}
		for _, wt := range ParseWorktreeList(out) {
			if err := ctx.Err(); err != nil {
				return rep, err
			}
			facts, class := Classify(ctx, m.git, m.probe, wt, m.now(), m.cfg.MinIdle)
			if class == ClassActive {
				continue
			}
			item := m.item(repo, facts, class)
			rep.Items = append(rep.Items, item)
			rep.Total += item.Size
		}
	}
	return rep, nil
}

// item renders one classified worktree as a report row. Size is the cheap
// st_blocks tier — worktree checkouts are real bytes, not clones.
func (m *Module) item(repo string, f Facts, class Class) modules.Item {
	ev := map[string]string{
		"repo":   repo,
		"tier":   class.String(),
		"branch": f.Info.Branch,
		"head":   f.Info.Head,
		"idle":   f.IdleFor.Round(time.Minute).String(),
	}
	if f.Info.Detached {
		ev["branch"] = "(detached)"
	}
	if f.Info.Prunable {
		ev["prunable"] = "directory already gone; only the registration remains"
	}
	if f.LastCommit != "" {
		ev["last_commit"] = f.LastCommit
	}
	if f.DirtySummary != "" {
		ev["dirty"] = f.DirtySummary
	}
	ev["remote_contained"] = strconv.FormatBool(f.RemoteContained)
	if f.AheadUpstream >= 0 {
		ev["ahead_of_upstream"] = strconv.Itoa(f.AheadUpstream)
	}
	if f.LiveProof != "" {
		ev["lsof"] = f.LiveProof
	}
	var size core.Bytes
	if !f.Info.Prunable {
		if ts, err := sizer.Blocks(f.Info.Path); err == nil {
			size = core.Bytes(ts.Blocks)
		}
	}
	return modules.Item{Path: f.Info.Path, Size: size, Evidence: ev}
}

// Plan proposes deletes ONLY for the obvious tier. Judgment rows never get
// an action from Plan — they exit through the judge (verdict-gated) or not
// at all.
func (m *Module) Plan(r modules.Report) []modules.Action {
	var out []modules.Action
	for _, it := range r.Items {
		if it.Evidence["tier"] != "obvious" {
			continue
		}
		out = append(out, modules.Action{
			Module: "worktrees",
			Op:     "delete",
			Target: it.Path,
			Size:   it.Size,
			Risk:   modules.RiskLow,
		})
	}
	return out
}

// Apply removes one obvious-tier worktree. The tier is RE-ESTABLISHED here
// (TOCTOU guard — a worktree a wave re-entered between Plan and Apply is
// refused), and the remover is `git worktree remove` WITHOUT --force, so
// git's own dirty/locked wall runs a second time under the repo lock.
// A fully-merged local branch left behind is deleted with `branch -d`
// (merged-only by definition); an unmerged one is left alone.
func (m *Module) Apply(ctx context.Context, a modules.Action) (modules.Result, error) {
	res := modules.Result{Action: a}
	fail := func(err error) (modules.Result, error) {
		res.Err = err
		return res, err
	}
	if a.Op != "delete" {
		return fail(fmt.Errorf("worktrees: unsupported op %q", a.Op))
	}
	repo, wt, err := m.locate(ctx, a.Target)
	if err != nil {
		return fail(err)
	}
	if wt.Prunable {
		if _, err := m.git(ctx, repo, "worktree", "prune"); err != nil {
			return fail(err)
		}
		return res, nil
	}
	facts, class := Classify(ctx, m.git, m.probe, wt, m.now(), m.cfg.MinIdle)
	if class != ClassObvious {
		return fail(fmt.Errorf("worktrees: %q re-classified %s between plan and apply — refusing (%s%s)",
			a.Target, class, facts.DirtySummary, facts.LiveProof))
	}
	freed, err := sizer.FreedByDelete(filepath.Dir(a.Target), func() error {
		_, rmErr := m.git(ctx, repo, "worktree", "remove", a.Target)
		return rmErr
	})
	if err != nil {
		return fail(err)
	}
	if wt.Branch != "" {
		// Best-effort: -d refuses anything unmerged, which is exactly the
		// contract — an obvious-tier branch is remote-contained already.
		_, _ = m.git(ctx, repo, "branch", "-d", wt.Branch)
	}
	res.BytesFreed = core.Bytes(freed)
	return res, nil
}

// locate resolves the repo owning target and target's current registration
// row. Refuses paths that are not a registered worktree of a discovered
// repo — the module can only ever delete what git itself lists.
func (m *Module) locate(ctx context.Context, target string) (repo string, wt Info, err error) {
	clean := filepath.Clean(target)
	for _, r := range DiscoverRepos(m.cfg.Roots, 3) {
		out, gerr := m.git(ctx, r, "worktree", "list", "--porcelain")
		if gerr != nil {
			continue
		}
		for _, w := range ParseWorktreeList(out) {
			if filepath.Clean(w.Path) == clean {
				if w.Primary {
					return "", Info{}, fmt.Errorf("worktrees: %q is a PRIMARY checkout — never removable", target)
				}
				return r, w, nil
			}
		}
	}
	return "", Info{}, fmt.Errorf("worktrees: %q is not a registered worktree of any discovered repo — refusing", target)
}

// Grave archives a worktree's committed and dirty state before a judged
// force-removal: a git bundle of HEAD (complete recovery of every commit)
// plus a tar.gz of the dirty/untracked files. Returns the grave paths.
func (m *Module) Grave(ctx context.Context, repo string, wt Info) (bundle, tarball string, err error) {
	if m.cfg.GraveDir == "" {
		return "", "", fmt.Errorf("worktrees: no grave_dir configured — refusing to force-remove without an archive")
	}
	if err := os.MkdirAll(m.cfg.GraveDir, 0o755); err != nil {
		return "", "", err
	}
	stamp := m.now().UTC().Format("20060102T150405Z")
	name := fmt.Sprintf("%s-%s", filepath.Base(wt.Path), stamp)
	bundle = filepath.Join(m.cfg.GraveDir, name+".bundle")
	if _, err := m.git(ctx, wt.Path, "bundle", "create", bundle, "HEAD"); err != nil {
		return "", "", fmt.Errorf("grave bundle: %w", err)
	}
	tarball = filepath.Join(m.cfg.GraveDir, name+"-dirty.tar.gz")
	wrote, terr := tarDirty(ctx, wt.Path, tarball)
	if terr != nil {
		return "", "", fmt.Errorf("grave tar: %w", terr)
	}
	if !wrote {
		tarball = "" // clean tree: bundle alone is the complete grave
	}
	_ = repo
	return bundle, tarball, nil
}

// ForceRemove is the judge-verdict remover: grave first, then
// `git worktree remove --force`, then best-effort `branch -D` (the bundle
// holds the commits). NEVER reachable from Plan/Apply — only the judge
// path, carrying an explicit remove verdict, calls it.
func (m *Module) ForceRemove(ctx context.Context, target string) (freed core.Bytes, bundle, tarball string, err error) {
	repo, wt, err := m.locate(ctx, target)
	if err != nil {
		return 0, "", "", err
	}
	bundle, tarball, err = m.Grave(ctx, repo, wt)
	if err != nil {
		return 0, "", "", err
	}
	n, err := sizer.FreedByDelete(filepath.Dir(target), func() error {
		_, rmErr := m.git(ctx, repo, "worktree", "remove", "--force", target)
		return rmErr
	})
	if err != nil {
		return 0, bundle, tarball, err
	}
	if wt.Branch != "" {
		_, _ = m.git(ctx, repo, "branch", "-D", wt.Branch)
	}
	return core.Bytes(n), bundle, tarball, nil
}
