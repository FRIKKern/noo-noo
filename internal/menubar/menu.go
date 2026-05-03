package menubar

import (
	"fmt"
	"time"
)

// MenuItem is a single row in the dropdown.
type MenuItem struct {
	ID        string
	Label     string
	Tooltip   string
	Disabled  bool
	Separator bool
	Submenu   *Menu
}

// Menu is an ordered list of items.
type Menu struct {
	Items []MenuItem
}

// Status mirrors the daemon's Daemon.Status RPC reply (subset).
type Status struct {
	Running         bool
	OpenSuggestions int
	LastScanAt      time.Time
}

// Suggestion mirrors heuristics.Suggestion (defined in Phase 0.2 task 30).
// Re-declared here to keep internal/menubar dependency-free.
type Suggestion struct {
	ID        int
	Module    string
	Reason    string
	Severity  string
	SizeBytes int64
}

// Build returns the menu structure for the given daemon state.
// Suggestions submenu rendering is added in Task 57; this is the base layout.
func Build(st Status, suggs []Suggestion) *Menu {
	_ = suggs // submenu wiring lands in Task 57
	statusLabel := "Daemon: down"
	if st.Running {
		statusLabel = fmt.Sprintf("Daemon: up · %d open", st.OpenSuggestions)
	}
	return &Menu{Items: []MenuItem{
		{ID: "status", Label: statusLabel, Disabled: true},
		{Separator: true},
		{ID: "scan-now", Label: "Run Scan Now", Disabled: !st.Running},
		{ID: "settings", Label: "Open Settings…"},
		{Separator: true},
		{ID: "quit", Label: "Quit Noo-Noo"},
	}}
}
