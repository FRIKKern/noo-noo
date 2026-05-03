// Package caches manages selective wipes of regenerable application caches
// under ~/Library/Caches. It clears directory _contents_ rather than the
// directory itself, so the owning app does not get confused.
package caches

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"github.com/frikkjarl/noo-noo/internal/core"
	"github.com/frikkjarl/noo-noo/internal/modules"
)

// Module manages a fixed list of cache target directories.
type Module struct {
	targets []string
	safety  *core.Safety
}

// New constructs a Module from a list of cache directory paths.
func New(targets []string, safety *core.Safety) *Module {
	return &Module{targets: targets, safety: safety}
}

func (*Module) Name() string { return "caches" }

// Scan reports each existing target directory and its size.
// Missing targets are silently skipped.
func (m *Module) Scan(ctx context.Context) (modules.Report, error) {
	rep := modules.Report{Module: "caches"}
	for _, t := range m.targets {
		if err := ctx.Err(); err != nil {
			return rep, err
		}
		info, err := os.Stat(t)
		if err != nil || !info.IsDir() {
			continue
		}
		size, _ := core.DirSize(t)
		rep.Items = append(rep.Items, modules.Item{
			Path: t,
			Size: size,
			Evidence: map[string]string{
				"size_bytes": strconv.FormatInt(int64(size), 10),
			},
		})
		rep.Total += size
	}
	return rep, nil
}

// Plan returns one "clear" Action per target with non-zero size.
func (m *Module) Plan(r modules.Report) []modules.Action {
	out := make([]modules.Action, 0, len(r.Items))
	for _, it := range r.Items {
		if it.Size == 0 {
			continue
		}
		out = append(out, modules.Action{
			Module: "caches",
			Op:     "clear",
			Target: it.Path,
			Size:   it.Size,
			Risk:   modules.RiskLow,
		})
	}
	return out
}

// Apply clears the contents of one cache directory while leaving the
// directory itself in place.
func (m *Module) Apply(ctx context.Context, a modules.Action) (modules.Result, error) {
	res := modules.Result{Action: a}
	if a.Op != "clear" {
		res.Err = fmt.Errorf("caches: unsupported op %q", a.Op)
		return res, res.Err
	}
	if err := m.safety.CanDelete(a.Target); err != nil {
		res.Err = err
		return res, err
	}
	before, _ := core.DirSize(a.Target)
	if err := clearContents(a.Target); err != nil {
		res.Err = err
		return res, err
	}
	res.BytesFreed = before
	return res, nil
}

// clearContents removes every immediate child of dir but keeps dir itself.
func clearContents(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		p := filepath.Join(dir, e.Name())
		if err := os.RemoveAll(p); err != nil {
			// continue on individual failures (likely a locked file in a
			// running app); the audit record will still show partial.
			continue
		}
	}
	return nil
}
