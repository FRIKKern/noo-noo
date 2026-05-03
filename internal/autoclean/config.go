// Package autoclean is the safety-critical engine that lets the noo-noo
// daemon delete files unattended. Every deletion passes a multi-gate
// cascade (master switch, risk acknowledgement, module allowlist,
// idleness, minimum size, per-tick budget, last-line safety guard)
// before any bytes leave disk. All deletions are also recorded in an
// audit table BEFORE the os.RemoveAll, so a crash mid-delete still
// leaves a forensic trail.
//
// The whole package defaults to disabled, and even when enabled
// requires the operator to set RiskAcknowledgedAt explicitly — a
// hand-edited config that turns Enabled=true without the timestamp
// is treated as not-acknowledged.
package autoclean

import (
	"github.com/FRIKKern/noo-noo/internal/heuristics"
)

// Config mirrors the [auto_clean] TOML section in internal/config. We
// re-declare here so internal/autoclean does not import internal/config
// (would form a cycle: config -> autoclean -> config).
//
// Defaults (set by internal/config when the section is absent):
//
//	Enabled            = false                // master switch off
//	ModulesAllowed     = []string{"dev"}      // never caches/startup
//	MinIdleDays        = 90                   // touch-too-recent guard
//	MinSizeMB          = 1024                 // skip-tiny guard
//	SizeCapPerTickGB   = 10                   // daily budget cap
//	RiskAcknowledgedAt = ""                   // unsigned waiver
type Config struct {
	Enabled            bool
	ModulesAllowed     []string
	MinIdleDays        int
	MinSizeMB          int
	SizeCapPerTickGB   int
	RiskAcknowledgedAt string
}

// Action is the in-memory description of a candidate delete. It carries
// the data EvaluateSuggestion needs and downstream Apply consumes.
// SkipReason is populated only when EvaluateSuggestion returns false.
type Action struct {
	SuggestionID int64
	Module       string
	TargetPath   string
	SizeBytes    int64
	IdleDays     int
	SkipReason   string
}

// suggestionSize extracts size_bytes from a Suggestion's evidence map.
// Heuristics encode size as either an int64 or a float64 (JSON round
// trip), so we accept both. Returns 0 if the field is missing or the
// wrong type — callers treat 0 as "fails the size gate".
func suggestionSize(s heuristics.Suggestion) int64 {
	if s.Evidence == nil {
		return 0
	}
	v, ok := s.Evidence["size_bytes"]
	if !ok {
		return 0
	}
	switch n := v.(type) {
	case int64:
		return n
	case int:
		return int64(n)
	case float64:
		return int64(n)
	}
	return 0
}

// suggestionIdleDays extracts idle_days from a Suggestion's evidence
// map, accepting int / int64 / float64. Returns 0 if absent.
func suggestionIdleDays(s heuristics.Suggestion) int {
	if s.Evidence == nil {
		return 0
	}
	v, ok := s.Evidence["idle_days"]
	if !ok {
		return 0
	}
	switch n := v.(type) {
	case int:
		return n
	case int64:
		return int(n)
	case float64:
		return int(n)
	}
	return 0
}

// EvaluateSuggestion returns the Action to take and a bool indicating
// whether the suggestion qualifies for auto-clean. When the bool is
// false, Action.SkipReason carries the gate name that blocked.
//
// Gate order is fixed and significant: cheaper gates first, so the hot
// path (auto-clean disabled) returns immediately.
//
//  1. master switch (Enabled)
//     1a. risk acknowledgement timestamp non-empty
//  2. module in allowlist
//  3. idle days >= MinIdleDays
//  4. size bytes >= MinSizeMB * MiB
func EvaluateSuggestion(s heuristics.Suggestion, cfg Config) (Action, bool) {
	a := Action{
		SuggestionID: s.ID,
		Module:       s.Module,
		TargetPath:   s.Target,
		SizeBytes:    suggestionSize(s),
		IdleDays:     suggestionIdleDays(s),
	}

	// Gate 1: master switch.
	if !cfg.Enabled {
		a.SkipReason = "not_enabled"
		return a, false
	}

	// Gate 1a: risk acknowledgement timestamp must be present. Catches
	// hand-edited config and inherited config files where the operator
	// flipped Enabled but never went through the multi-step opt-in.
	if cfg.RiskAcknowledgedAt == "" {
		a.SkipReason = "risk_not_acknowledged"
		return a, false
	}

	// Gate 2: module allowlist.
	allowed := false
	for _, m := range cfg.ModulesAllowed {
		if m == s.Module {
			allowed = true
			break
		}
	}
	if !allowed {
		a.SkipReason = "module_not_allowed"
		return a, false
	}

	// Gate 3: idleness.
	if a.IdleDays < cfg.MinIdleDays {
		a.SkipReason = "idle_too_short"
		return a, false
	}

	// Gate 4: minimum size. Convert MiB threshold into bytes.
	minBytes := int64(cfg.MinSizeMB) * 1024 * 1024
	if a.SizeBytes < minBytes {
		a.SkipReason = "size_too_small"
		return a, false
	}

	return a, true
}
