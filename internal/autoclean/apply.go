package autoclean

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/FRIKKern/noo-noo/internal/core"
)

// AutoCleanEvent is one row in the auto_clean_events table. The autoclean
// engine writes one of these BEFORE the os.RemoveAll (outcome=in_progress)
// and updates it after (outcome=deleted | skipped | errored). The pre-row
// is the crash pivot: a daemon crash mid-delete still leaves a forensic
// trail.
type AutoCleanEvent struct {
	StartedAtUnix      int64
	EndedAtUnix        int64
	Trigger            string // 'daily' | 'manual'
	Outcome            string // 'in_progress' | 'deleted' | 'skipped' | 'errored'
	SkipReason         string
	TargetPath         string
	Module             string
	TargetSizeBytes    int64
	FreedBytes         int64
	IdleDaysAtDecision int
	SuggestionID       string
	ErrorMsg           string
}

// AutoCleanEventUpdate carries the post-delete fields written into an
// already-recorded row.
type AutoCleanEventUpdate struct {
	EndedAtUnix int64
	Outcome     string
	FreedBytes  int64
	ErrorMsg    string
}

// EventStore is the narrow audit-write surface autoclean needs from the
// store. The real *store.Store satisfies this; tests use an in-memory
// SQLite-backed implementation in apply_test.go. Keeping it an interface
// means autoclean does not import internal/store directly (avoids the
// migration timing dance and keeps the coupling honest).
type EventStore interface {
	// RecordAutoCleanEvent inserts a new event row and returns the new
	// row's id. Called twice per delete: once with outcome=in_progress
	// before the work, once with outcome=skipped/errored if the safety
	// guard rejects up front.
	RecordAutoCleanEvent(e AutoCleanEvent) (int64, error)
	// UpdateAutoCleanEvent patches the in-progress row with the final
	// outcome (deleted | errored). The id is what RecordAutoCleanEvent
	// returned for the in_progress row.
	UpdateAutoCleanEvent(id int64, u AutoCleanEventUpdate) error
}

// Deleter is the per-module file-removal callback. The default
// implementation calls os.RemoveAll and re-stats up-front to compute
// FreedBytes; tests inject a stub that returns canned values.
type Deleter func(ctx context.Context, path string) (int64, error)

// DefaultDeleter is the production Deleter. It computes the directory
// size BEFORE removal (so we can report freed bytes accurately) and
// then calls os.RemoveAll.
func DefaultDeleter(_ context.Context, path string) (int64, error) {
	size, _ := core.DirSize(path)
	if err := os.RemoveAll(path); err != nil {
		return 0, err
	}
	return int64(size), nil
}

// Engine carries the dependencies needed to evaluate, gate, and apply
// auto-clean actions.
type Engine struct {
	store    EventStore
	cfg      Config
	roots    []string
	safety   *core.Safety
	deleters map[string]Deleter // module name -> deleter
}

// New constructs an Engine. roots is the daemon's configured ScanRoots
// (Apply rejects targets that escape every root). safety is the same
// core.Safety used by the on-demand CLI — autoclean never has weaker
// safety than the manual path. deleters maps module names to the
// per-module deletion callback; in production this is just
// {"dev": DefaultDeleter}.
func New(st EventStore, cfg Config, roots []string, safety *core.Safety, deleters map[string]Deleter) *Engine {
	if deleters == nil {
		deleters = map[string]Deleter{}
	}
	return &Engine{
		store:    st,
		cfg:      cfg,
		roots:    roots,
		safety:   safety,
		deleters: deleters,
	}
}

// Result is one successful Apply outcome.
type Result struct {
	SuggestionID int64
	TargetPath   string
	FreedBytes   int64
}

// SkipResult records a gate or safety-guard refusal.
type SkipResult struct {
	SuggestionID int64
	Reason       string
}

// ErrorResult records a delete that was attempted and failed.
type ErrorResult struct {
	SuggestionID int64
	Err          error
}

// errPressureTrigger is returned when Apply is called with any trigger
// other than "daily". The plan's reasoning: pressure events correlate
// with active dev work where mid-flight deletions are most dangerous.
var errPressureTrigger = errors.New("autoclean: trigger must be 'daily' (pressure-driven deletes are forbidden)")

// safetyGuard does last-line checks at delete time. Failures here ALWAYS
// abort the deletion. Three checks:
//
//  1. target resolves under one of the configured scan roots
//  2. action.Module is in cfg.ModulesAllowed (defense in depth: the
//     evaluate cascade already checks this)
//  3. core.Safety.CanDelete agrees — same gate the manual CLI uses
func (e *Engine) safetyGuard(action Action) error {
	abs, err := filepath.Abs(action.TargetPath)
	if err != nil {
		return fmt.Errorf("safety: abs path: %w", err)
	}

	// 1. resolves under one of the configured roots.
	if len(e.roots) > 0 {
		underRoot := false
		for _, root := range e.roots {
			rabs, err := filepath.Abs(root)
			if err != nil {
				continue
			}
			rel, err := filepath.Rel(rabs, abs)
			if err != nil {
				continue
			}
			if !strings.HasPrefix(rel, "..") && rel != "." {
				underRoot = true
				break
			}
		}
		if !underRoot {
			return errors.New("safety: target not under any configured root")
		}
	}

	// 2. module name in allowlist (defense in depth).
	allowed := false
	for _, m := range e.cfg.ModulesAllowed {
		if m == action.Module {
			allowed = true
			break
		}
	}
	if !allowed {
		return errors.New("safety: module not in allowlist")
	}

	// 3. core.Safety.CanDelete — NEVER bypass. Same logic as manual CLI.
	if e.safety != nil {
		if err := e.safety.CanDelete(abs); err != nil {
			return fmt.Errorf("safety: %w", err)
		}
	}

	// 4. module-specific deleter must exist.
	if _, ok := e.deleters[action.Module]; !ok {
		return fmt.Errorf("safety: no deleter registered for module %q", action.Module)
	}

	return nil
}

// suggestionIDStr is a small helper because the suggestions table uses
// int64 ids while the audit table stores them as TEXT (so a non-DB
// suggestion source — manual run, fixture — can also have an id).
func suggestionIDStr(id int64) string {
	return fmt.Sprintf("%d", id)
}

// Apply performs a single delete with the audit-before, audit-after
// pattern. Steps in order:
//
//  1. reject any trigger != "daily" (pressure-triggered deletes are
//     forbidden by design).
//  2. safetyGuard. If it fails, write a single skipped row and return.
//  3. write the in_progress audit row.
//  4. invoke the module's Deleter.
//  5. update the audit row with deleted | errored.
//
// The in_progress row in step 3 is the crash pivot. If the process is
// killed between 3 and 5, recovery sees a stranded in_progress row and
// can re-stat the path.
func (e *Engine) Apply(ctx context.Context, action Action, trigger string) (Result, error) {
	if trigger != "daily" {
		return Result{}, errPressureTrigger
	}
	now := time.Now().Unix()

	if err := e.safetyGuard(action); err != nil {
		_, _ = e.store.RecordAutoCleanEvent(AutoCleanEvent{
			StartedAtUnix:      now,
			EndedAtUnix:        now,
			Trigger:            trigger,
			Outcome:            "skipped",
			SkipReason:         "safety_guard",
			TargetPath:         action.TargetPath,
			Module:             action.Module,
			TargetSizeBytes:    action.SizeBytes,
			IdleDaysAtDecision: action.IdleDays,
			SuggestionID:       suggestionIDStr(action.SuggestionID),
			ErrorMsg:           err.Error(),
		})
		return Result{}, err
	}

	// Pre-delete audit row. Crash pivot.
	rowID, err := e.store.RecordAutoCleanEvent(AutoCleanEvent{
		StartedAtUnix:      now,
		Trigger:            trigger,
		Outcome:            "in_progress",
		TargetPath:         action.TargetPath,
		Module:             action.Module,
		TargetSizeBytes:    action.SizeBytes,
		IdleDaysAtDecision: action.IdleDays,
		SuggestionID:       suggestionIDStr(action.SuggestionID),
	})
	if err != nil {
		return Result{}, fmt.Errorf("audit pre-delete: %w", err)
	}

	// The actual delete via the module's Deleter.
	deleter := e.deleters[action.Module]
	freed, applyErr := deleter(ctx, action.TargetPath)

	// Post-delete audit row update.
	outcome := "deleted"
	var errMsg string
	if applyErr != nil {
		outcome = "errored"
		errMsg = applyErr.Error()
	}
	_ = e.store.UpdateAutoCleanEvent(rowID, AutoCleanEventUpdate{
		EndedAtUnix: time.Now().Unix(),
		Outcome:     outcome,
		FreedBytes:  freed,
		ErrorMsg:    errMsg,
	})

	if applyErr != nil {
		return Result{}, applyErr
	}
	return Result{
		SuggestionID: action.SuggestionID,
		TargetPath:   action.TargetPath,
		FreedBytes:   freed,
	}, nil
}
