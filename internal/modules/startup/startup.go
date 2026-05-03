// Package startup disables and re-enables macOS LaunchAgents and
// LaunchDaemons via launchctl. Disabling a service requires sudo when the
// service lives in the system domain.
package startup

import (
	"context"
	"fmt"

	"github.com/frikkjarl/noo-noo/internal/core"
	"github.com/frikkjarl/noo-noo/internal/modules"
)

// Config lists the launchd labels the user wants disabled, grouped by
// domain.
type Config struct {
	UserAgents    []string // ~/Library/LaunchAgents — gui/<uid>/<label>
	SystemAgents  []string // /Library/LaunchAgents  — system/<label> (sudo)
	SystemDaemons []string // /Library/LaunchDaemons — system/<label> (sudo)
}

// Module disables/enables launchd services. restoreLog is the path of a
// JSONL file recording every disable so a future enable can reverse it.
type Module struct {
	cfg        Config
	runner     Runner
	restoreLog string
	uid        int
}

// New constructs a startup Module.
func New(cfg Config, runner Runner, restoreLog string, uid int) *Module {
	return &Module{cfg: cfg, runner: runner, restoreLog: restoreLog, uid: uid}
}

func (*Module) Name() string { return "startup" }

// Scan reports every configured label as an Item. Sizes are zero (this
// module measures *services*, not bytes).
func (m *Module) Scan(_ context.Context) (modules.Report, error) {
	rep := modules.Report{Module: "startup"}
	add := func(label, domain string, sudo bool) {
		ev := map[string]string{"domain": domain}
		if sudo {
			ev["sudo"] = "true"
		}
		rep.Items = append(rep.Items, modules.Item{
			Path:     fmt.Sprintf("%s/%s", domain, label),
			Size:     core.Bytes(0),
			Evidence: ev,
		})
	}
	for _, l := range m.cfg.UserAgents {
		add(l, fmt.Sprintf("gui/%d", m.uid), false)
	}
	for _, l := range m.cfg.SystemAgents {
		add(l, "system", true)
	}
	for _, l := range m.cfg.SystemDaemons {
		add(l, "system", true)
	}
	return rep, nil
}

// Plan returns one disable Action per Item.
func (m *Module) Plan(r modules.Report) []modules.Action {
	out := make([]modules.Action, 0, len(r.Items))
	for _, it := range r.Items {
		out = append(out, modules.Action{
			Module: "startup",
			Op:     "disable",
			Target: it.Path,
			Risk:   modules.RiskMedium, // higher than dev/caches; reversible but visible
		})
	}
	return out
}

// Apply executes a disable or enable Action. Disable runs `launchctl
// bootout` then `launchctl disable`; enable runs `launchctl enable`.
func (m *Module) Apply(ctx context.Context, a modules.Action) (modules.Result, error) {
	res := modules.Result{Action: a}
	sudo := isSystemDomain(a.Target)
	switch a.Op {
	case "disable":
		if err := m.runner.Run(ctx, sudo, "launchctl", "bootout", a.Target); err != nil {
			// bootout often fails harmlessly when the service is not loaded;
			// log via Result.Err but continue to disable.
			res.Err = err
		}
		if err := m.runner.Run(ctx, sudo, "launchctl", "disable", a.Target); err != nil {
			res.Err = err
			return res, err
		}
		res.Err = nil
	case "enable":
		if err := m.runner.Run(ctx, sudo, "launchctl", "enable", a.Target); err != nil {
			res.Err = err
			return res, err
		}
	default:
		res.Err = fmt.Errorf("startup: unsupported op %q", a.Op)
		return res, res.Err
	}
	return res, nil
}

func isSystemDomain(target string) bool {
	return len(target) >= 7 && target[:7] == "system/"
}
