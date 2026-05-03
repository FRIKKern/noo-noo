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

// badgeLabel renders the dropdown's first (disabled) row, summarising daemon
// liveness and the open-suggestions count. Singular/plural is handled
// explicitly so "1 tip" never reads "1 tips".
func badgeLabel(st Status) string {
	switch {
	case !st.Running:
		return "Daemon: down"
	case st.OpenSuggestions == 0:
		return "Daemon: up · idle"
	case st.OpenSuggestions == 1:
		return "Daemon: up · 1 tip"
	default:
		return fmt.Sprintf("Daemon: up · %d tips", st.OpenSuggestions)
	}
}

// Build returns the menu structure for the given daemon state. When suggs is
// non-empty a "Suggestions" submenu row is inserted between the status badge
// and the first separator; the submenu shows the top 3 inline plus a
// "See all N in CLI…" trailer when more remain.
func Build(st Status, suggs []Suggestion) *Menu {
	items := []MenuItem{{ID: "status", Label: badgeLabel(st), Disabled: true}}
	if len(suggs) > 0 {
		items = append(items, MenuItem{
			ID:      "suggestions",
			Label:   "Suggestions",
			Submenu: suggestionsSubmenu(suggs),
		})
	}
	items = append(items,
		MenuItem{Separator: true},
		MenuItem{ID: "scan-now", Label: "Run Scan Now", Disabled: !st.Running},
		MenuItem{ID: "settings", Label: "Open Settings…"},
		MenuItem{Separator: true},
		MenuItem{ID: "quit", Label: "Quit Noo-Noo"},
	)
	return &Menu{Items: items}
}

// suggestionsSubmenu renders up to three inline suggestion rows followed by a
// "See all N in CLI…" trailer when the suggestion list exceeds the inline cap.
// Each inline row label is "<reason> · <human bytes>".
func suggestionsSubmenu(suggs []Suggestion) *Menu {
	const inlineMax = 3
	items := make([]MenuItem, 0, inlineMax+1)
	n := len(suggs)
	if n > inlineMax {
		n = inlineMax
	}
	for i := 0; i < n; i++ {
		s := suggs[i]
		items = append(items, MenuItem{
			ID:    fmt.Sprintf("sugg-%d", s.ID),
			Label: fmt.Sprintf("%s · %s", s.Reason, humanBytes(s.SizeBytes)),
		})
	}
	if len(suggs) > inlineMax {
		items = append(items, MenuItem{
			ID:    "see-all",
			Label: fmt.Sprintf("See all %d in CLI…", len(suggs)),
		})
	}
	return &Menu{Items: items}
}

// humanBytes prints a byte count in 1024-based units (KB/MB/GB/TB) with one
// decimal place for non-byte sizes.
func humanBytes(b int64) string {
	const k = 1024
	if b < k {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(k), 0
	for n := b / k; n >= k; n /= k {
		div *= k
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(b)/float64(div), "KMGT"[exp])
}
