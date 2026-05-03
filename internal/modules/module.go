// Package modules defines the contract that every cleanup module
// (dev, caches, startup, ...) implements. The CLI dispatches uniformly
// over this interface.
package modules

import (
	"context"

	"github.com/frikkjarl/noo-noo/internal/core"
)

// RiskLevel describes how confident we are that an action is safe.
type RiskLevel int

const (
	RiskLow RiskLevel = iota
	RiskMedium
	RiskHigh
)

func (r RiskLevel) String() string {
	switch r {
	case RiskLow:
		return "low"
	case RiskMedium:
		return "medium"
	case RiskHigh:
		return "high"
	default:
		return "unknown"
	}
}

// Module is a cleanup category. Each module discovers candidates (Scan),
// proposes actions (Plan), and applies them (Apply).
type Module interface {
	Name() string
	Scan(ctx context.Context) (Report, error)
	Plan(r Report) []Action
	Apply(ctx context.Context, a Action) (Result, error)
}

// Report is the output of a Scan.
type Report struct {
	Module string
	Items  []Item
	Total  core.Bytes
}

// Item is one discovered candidate.
type Item struct {
	Path     string
	Size     core.Bytes
	Evidence map[string]string
}

// Action is a proposed destructive operation. The CLI presents Actions
// to the user for confirmation before calling Apply.
type Action struct {
	Module string
	Op     string // "delete" | "disable" | "enable"
	Target string
	Size   core.Bytes
	Risk   RiskLevel
}

// Result is the outcome of one Apply call.
type Result struct {
	Action     Action
	BytesFreed core.Bytes
	Err        error
}
