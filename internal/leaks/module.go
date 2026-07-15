package leaks

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"github.com/FRIKKern/noo-noo/internal/core"
	"github.com/FRIKKern/noo-noo/internal/modules"
	"github.com/FRIKKern/noo-noo/internal/sizer"
)

// Module scans a signature registry for leak hits, plans deletes for the
// provably-stale ones, and applies them behind a signature-scoped safety
// predicate plus an apply-time staleness re-check.
type Module struct {
	sigs   []Signature
	safety *core.Safety
	probe  Prober // liveness probe; defaults to LsofProbe, injectable in tests
	uid    int    // only hits owned by this uid are considered
	now    func() time.Time
}

// New constructs a Module over a signature registry. safety supplies the
// signature-scoped CanDeleteLeakTarget predicate (its generic allowlist is
// deliberately NOT consulted for leak targets — charter D4).
func New(sigs []Signature, safety *core.Safety) *Module {
	return &Module{
		sigs:   sigs,
		safety: safety,
		probe:  LsofProbe,
		uid:    os.Getuid(),
		now:    time.Now,
	}
}

func (*Module) Name() string { return "leaks" }

// Scan expands every signature's globs, keeps user-owned hits, classifies
// each stale-vs-live, and truth-sizes it. Every hit — live or stale — is
// reported; only Plan filters to the deletable subset.
func (m *Module) Scan(ctx context.Context) (modules.Report, error) {
	rep := modules.Report{Module: "leaks"}
	seen := map[string]bool{}
	for _, sig := range m.sigs {
		for _, g := range sig.Globs {
			matches, err := filepath.Glob(g)
			if err != nil {
				// Bad pattern is a registry-authoring bug; skip the glob,
				// never the whole scan.
				continue
			}
			for _, p := range matches {
				if err := ctx.Err(); err != nil {
					return rep, err
				}
				p = filepath.Clean(p)
				if seen[p] {
					continue
				}
				seen[p] = true
				item, ok := m.inspect(ctx, sig, p)
				if !ok {
					continue
				}
				rep.Items = append(rep.Items, item)
				rep.Total += item.Size
			}
		}
	}
	return rep, nil
}

// Plan emits delete actions ONLY for stale hits. Live hits stay report-only:
// no action is ever proposed for them, and Apply refuses them regardless.
func (m *Module) Plan(r modules.Report) []modules.Action {
	var out []modules.Action
	for _, it := range r.Items {
		if it.Evidence["staleness"] != "stale" {
			continue
		}
		risk := modules.RiskLow
		if sig, ok := m.signatureFor(it.Path); ok {
			risk = sig.Risk
		}
		out = append(out, modules.Action{
			Module: "leaks",
			Op:     "delete",
			Target: it.Path,
			Size:   it.Size,
			Risk:   risk,
		})
	}
	return out
}

// Apply deletes one stale leak hit. Staleness is RE-ESTABLISHED here, at
// apply time — a hit that went live between Plan and Apply is refused
// (TOCTOU guard) — and the target must pass the signature-scoped
// CanDeleteLeakTarget predicate. Freed bytes are measured for real via
// sizer.FreedByDelete (statfs delta), never inferred from the plan size.
func (m *Module) Apply(ctx context.Context, a modules.Action) (modules.Result, error) {
	res := modules.Result{Action: a}
	fail := func(err error) (modules.Result, error) {
		res.Err = err
		return res, err
	}
	if a.Op != "delete" {
		return fail(fmt.Errorf("leaks: unsupported op %q", a.Op))
	}
	sig, ok := m.signatureFor(a.Target)
	if !ok {
		return fail(fmt.Errorf("leaks: %q matches no registered leak signature — refusing", a.Target))
	}
	// TOCTOU guard: never trust staleness carried over from Scan/Plan.
	item, ok := m.inspect(ctx, sig, a.Target)
	if !ok {
		return fail(fmt.Errorf("leaks: %q no longer inspectable (gone, symlink, or not owned by uid %d) — refusing", a.Target, m.uid))
	}
	if item.Evidence["staleness"] != "stale" {
		return fail(fmt.Errorf("leaks: %q went LIVE between plan and apply — refusing (%s)",
			a.Target, item.Evidence["lsof"]))
	}
	if err := m.safety.CanDeleteLeakTarget(a.Target, sig.Globs); err != nil {
		return fail(err)
	}
	freed, err := sizer.FreedByDelete(filepath.Dir(a.Target), func() error {
		return os.RemoveAll(a.Target)
	})
	if err != nil {
		return fail(err)
	}
	res.BytesFreed = core.Bytes(freed)
	return res, nil
}

// signatureFor returns the registry entry whose glob fully matches path.
func (m *Module) signatureFor(path string) (Signature, bool) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return Signature{}, false
	}
	clean := filepath.Clean(abs)
	for _, sig := range m.sigs {
		for _, g := range sig.Globs {
			if ok, err := filepath.Match(g, clean); err == nil && ok {
				return sig, true
			}
		}
	}
	return Signature{}, false
}

// inspect classifies and truth-sizes one glob hit. ok=false means the hit is
// not a candidate at all (vanished, a symlink, or not owned by the scanning
// user) and must not appear in reports.
func (m *Module) inspect(ctx context.Context, sig Signature, path string) (modules.Item, bool) {
	fi, err := os.Lstat(path)
	if err != nil {
		return modules.Item{}, false
	}
	// Never follow a matched symlink: the leak classes are real dirs/files;
	// a symlink here is somebody else's data wearing our name.
	if fi.Mode()&os.ModeSymlink != 0 {
		return modules.Item{}, false
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok || int(st.Uid) != m.uid {
		return modules.Item{}, false
	}

	ev := map[string]string{
		"signature":      sig.ID,
		"staleness_rule": sig.Staleness.String(),
		"workaround":     sig.Workaround,
	}

	stale := m.classify(ctx, sig, path, ev)
	if stale {
		ev["staleness"] = "stale"
	} else {
		ev["staleness"] = "live"
	}

	// Truth-sizing: Blocks is what du would say; UniqueAllocated is what
	// deleting can actually reclaim (APFS-clone- and sparse-aware). The
	// delta is the du-fiction the founding incident was made of.
	var uniq, blocks int64
	if ts, err := sizer.UniqueAllocated(path); err == nil {
		uniq = ts.UniqueAllocated
		ev["unique_allocated_bytes"] = strconv.FormatInt(uniq, 10)
	} else {
		ev["unique_allocated_error"] = err.Error()
	}
	if ts, err := sizer.Blocks(path); err == nil {
		blocks = ts.Blocks
		ev["blocks_bytes"] = strconv.FormatInt(blocks, 10)
	} else {
		ev["blocks_error"] = err.Error()
	}
	if fiction := blocks - uniq; fiction > 0 {
		ev["du_fiction_bytes"] = strconv.FormatInt(fiction, 10)
	}

	return modules.Item{Path: path, Size: core.Bytes(uniq), Evidence: ev}, true
}

// classify runs the signature's staleness rule, recording the proof trail
// into ev. Any inability to prove staleness classifies LIVE (fail-safe).
func (m *Module) classify(ctx context.Context, sig Signature, path string, ev map[string]string) bool {
	if sig.Staleness == StaleWhenAgedAndLsofEmpty {
		newest, err := newestMtime(path)
		if err != nil {
			ev["age"] = "unprovable (" + err.Error() + ") — treating as LIVE (fail-safe)"
			return false
		}
		age := m.now().Sub(newest)
		ev["newest_mtime"] = newest.UTC().Format(time.RFC3339)
		ev["min_age"] = sig.MinAge.String()
		if age < sig.MinAge {
			ev["age"] = fmt.Sprintf("newest mtime %s old < min age %s — protecting possible live session", age.Round(time.Second), sig.MinAge)
			return false
		}
	}
	live, proof := m.probe(ctx, path)
	ev["lsof"] = proof
	return !live
}

// newestMtime returns the newest modification time anywhere in the tree at
// path, including path itself. Any walk error makes age unprovable.
func newestMtime(path string) (time.Time, error) {
	var newest time.Time
	err := filepath.WalkDir(path, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		fi, err := d.Info()
		if err != nil {
			return err
		}
		if mt := fi.ModTime(); mt.After(newest) {
			newest = mt
		}
		return nil
	})
	if err != nil {
		return time.Time{}, err
	}
	return newest, nil
}
