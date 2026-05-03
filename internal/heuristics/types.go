// Package heuristics scores observed disk state and emits Suggestions.
// All heuristics share the same Suggestion type so the store, the IPC layer,
// and the CLI render path can treat them uniformly.
package heuristics

import "time"

// RiskLevel orders suggestions by destructiveness.
type RiskLevel string

const (
	RiskLow    RiskLevel = "low"    // safe to act on (e.g. delete build artifacts)
	RiskMedium RiskLevel = "medium" // user should confirm (e.g. cache prune)
	RiskHigh   RiskLevel = "high"   // potentially destructive (e.g. delete repo)
)

// Suggestion is what every heuristic emits and what the daemon persists.
type Suggestion struct {
	ID        int64          `json:"id,omitempty"`
	Module    string         `json:"module"`
	Target    string         `json:"target"`
	Reason    string         `json:"reason"`
	Evidence  map[string]any `json:"evidence,omitempty"`
	RiskLevel RiskLevel      `json:"risk_level"`
	CreatedAt time.Time      `json:"created_at"`
}
