// Package dev finds and removes regenerable build artifacts in code repos:
// node_modules, .next, .turbo, .cache, dist, build.
package dev

import (
	"context"
	"io/fs"
	"path/filepath"
	"strconv"
	"time"

	"github.com/frikkjarl/noo-noo/internal/core"
	"github.com/frikkjarl/noo-noo/internal/modules"
)

// Targets is the set of directory basenames recognized as build artifacts.
var Targets = map[string]bool{
	"node_modules": true,
	".next":        true,
	".turbo":       true,
	".cache":       true,
	"dist":         true,
	"build":        true,
}

// Module discovers and removes dev-artifact directories under one or more
// scan roots. All deletions are gated by a Safety guard.
type Module struct {
	roots  []string
	safety *core.Safety
}

// New constructs a Module that scans the given roots.
func New(roots []string, safety *core.Safety) *Module {
	return &Module{roots: roots, safety: safety}
}

func (*Module) Name() string { return "dev" }

// Scan walks each root, collects every target directory (pruning nested
// targets so node_modules-inside-node_modules is not double-counted),
// and reports their sizes.
func (m *Module) Scan(ctx context.Context) (modules.Report, error) {
	rep := modules.Report{Module: "dev"}
	for _, root := range m.roots {
		if err := ctx.Err(); err != nil {
			return rep, err
		}
		err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil // skip unreadable
			}
			if !d.IsDir() {
				return nil
			}
			if Targets[d.Name()] {
				size, _ := core.DirSize(p)
				ev := map[string]string{"size_bytes": strconv.FormatInt(int64(size), 10)}
				if mtime, err := dirMtime(p); err == nil {
					ev["mtime"] = mtime.Format(time.RFC3339)
				}
				rep.Items = append(rep.Items, modules.Item{
					Path: p, Size: size, Evidence: ev,
				})
				rep.Total += size
				return filepath.SkipDir // prune nested
			}
			return nil
		})
		if err != nil {
			return rep, err
		}
	}
	return rep, nil
}

// Plan returns one delete Action per item. Risk is always Low for dev
// artifacts because they are by definition regenerable.
func (m *Module) Plan(r modules.Report) []modules.Action {
	out := make([]modules.Action, 0, len(r.Items))
	for _, it := range r.Items {
		out = append(out, modules.Action{
			Module: "dev",
			Op:     "delete",
			Target: it.Path,
			Size:   it.Size,
			Risk:   modules.RiskLow,
		})
	}
	return out
}

// Apply executes one Action. Currently only "delete" is supported.
func (m *Module) Apply(ctx context.Context, a modules.Action) (modules.Result, error) {
	res := modules.Result{Action: a}
	if a.Op != "delete" {
		res.Err = errUnsupportedOp(a.Op)
		return res, res.Err
	}
	if err := m.safety.CanDelete(a.Target); err != nil {
		res.Err = err
		return res, err
	}
	size, _ := core.DirSize(a.Target)
	if err := removeAll(a.Target); err != nil {
		res.Err = err
		return res, err
	}
	res.BytesFreed = size
	return res, nil
}

func dirMtime(path string) (time.Time, error) {
	info, err := filepath.Abs(path)
	_ = info
	stat, err := osStat(path)
	if err != nil {
		return time.Time{}, err
	}
	return stat.ModTime(), nil
}

// indirection points (var-assigned for swap in tests if ever needed)
var (
	osStat    = func(p string) (fs.FileInfo, error) { return fileStat(p) }
	removeAll = func(p string) error { return removeAllImpl(p) }
)
