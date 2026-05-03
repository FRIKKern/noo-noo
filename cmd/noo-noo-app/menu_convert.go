// Wails-only conversion from the dependency-free menubar.Menu shape into a
// real *application.Menu with click callbacks that route back through
// menubar.Dispatch. Lives in cmd/noo-noo-app so internal/menubar stays
// Wails-free.
package main

import (
	"github.com/wailsapp/wails/v3/pkg/application"

	"github.com/FRIKKern/noo-noo/internal/menubar"
)

// buildWailsMenu converts the abstract menu produced by menubar.Build into
// a real Wails menu. Every clickable item gets an OnClick callback that
// invokes menubar.Dispatch(h, item.ID).
//
// Separators map to AddSeparator. Submenus recurse. Disabled items are
// added without an OnClick (Wails has no enable/disable on a per-item
// basis in alpha.84, so we just skip the click handler).
func buildWailsMenu(m *menubar.Menu, h menubar.Handler) *application.Menu {
	out := application.NewMenu()
	if m == nil {
		return out
	}
	for _, it := range m.Items {
		appendItem(out, it, h)
	}
	return out
}

func appendItem(parent *application.Menu, it menubar.MenuItem, h menubar.Handler) {
	if it.Separator {
		parent.AddSeparator()
		return
	}
	if it.Submenu != nil {
		sub := parent.AddSubmenu(it.Label)
		for _, child := range it.Submenu.Items {
			appendItem(sub, child, h)
		}
		return
	}
	mi := parent.Add(it.Label)
	if it.Tooltip != "" {
		mi.SetTooltip(it.Tooltip)
	}
	if it.Disabled {
		mi.SetEnabled(false)
		return
	}
	if it.ID == "" {
		return
	}
	id := it.ID // capture by value for closure safety
	mi.OnClick(func(*application.Context) {
		menubar.Dispatch(h, id)
	})
}
