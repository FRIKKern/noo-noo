package main

import (
	"testing"

	"github.com/FRIKKern/noo-noo/internal/heuristics"
	"github.com/FRIKKern/noo-noo/internal/ipc"
	"github.com/FRIKKern/noo-noo/internal/menubar"
)

// TestMainNoOp verifies that calling buildApp() with the headless option
// returns a non-nil Wails application instance and does not panic.
// We do NOT call app.Run() in tests — that would block on the event loop.
func TestMainNoOp(t *testing.T) {
	app := buildApp(buildOpts{Headless: true})
	if app == nil {
		t.Fatal("buildApp returned nil")
	}
}

func TestWiring_HandlerImplementsInterface(t *testing.T) {
	app := buildApp(buildOpts{Headless: true})
	h := newAppHandler(app, nil) // nil ipc client OK in headless
	var _ menubar.Handler = h    // compile-time assertion
}

func TestWiring_RefreshUpdatesTrayTitle(t *testing.T) {
	app := buildApp(buildOpts{Headless: true})
	tray := &fakeTray{}
	st := menubar.Status{Running: true, OpenSuggestions: 3}
	refreshTray(tray, st)
	if tray.title != "3" {
		t.Errorf("title = %q, want %q", tray.title, "3")
	}
	if tray.menu == nil || len(tray.menu.Items) == 0 {
		t.Error("menu not set")
	}
	_ = app
}

// TestMapSuggestions pins the wire → menubar projection, including the
// first-class SizeBytes carry that makes submenu rows show real sizes.
func TestMapSuggestions(t *testing.T) {
	items := []ipc.SuggestionAlias{{
		ID:        7,
		Module:    "leaks",
		Target:    "/private/tmp/claude-x",
		Reason:    "chrome-code-sign-clone: 2.0 GB really reclaimable (proven stale)",
		RiskLevel: heuristics.RiskLow,
		SizeBytes: 2 << 30,
	}}
	got := mapSuggestions(items)
	if len(got) != 1 {
		t.Fatalf("mapped %d suggestions, want 1", len(got))
	}
	want := menubar.Suggestion{
		ID:        7,
		Module:    "leaks",
		Reason:    "chrome-code-sign-clone: 2.0 GB really reclaimable (proven stale)",
		Severity:  "low",
		SizeBytes: 2 << 30,
	}
	if got[0] != want {
		t.Errorf("mapSuggestions[0] = %+v, want %+v", got[0], want)
	}
}

// TestWiring_RefreshRendersSuggestionsSubmenu: the Task-57 nil is gone —
// a Status carrying suggestions must produce the Suggestions submenu row.
func TestWiring_RefreshRendersSuggestionsSubmenu(t *testing.T) {
	tray := &fakeTray{}
	st := menubar.Status{
		Running:         true,
		OpenSuggestions: 1,
		Suggestions: []menubar.Suggestion{{
			ID: 1, Module: "leaks", Reason: "leak", Severity: "low", SizeBytes: 1 << 30,
		}},
	}
	refreshTray(tray, st)
	if tray.menu == nil {
		t.Fatal("menu not set")
	}
	found := false
	for _, it := range tray.menu.Items {
		if it.ID == "suggestions" && it.Submenu != nil && len(it.Submenu.Items) == 1 {
			found = true
		}
	}
	if !found {
		t.Errorf("no populated Suggestions submenu in %+v", tray.menu.Items)
	}
}

type fakeTray struct {
	title string
	icon  menubar.Icon
	menu  *menubar.Menu
}

func (f *fakeTray) SetTitle(s string)       { f.title = s }
func (f *fakeTray) SetIcon(i menubar.Icon)  { f.icon = i }
func (f *fakeTray) SetMenu(m *menubar.Menu) { f.menu = m }

func TestOpenSettings_OpensSingletonWindow(t *testing.T) {
	app := buildApp(buildOpts{Headless: true})
	h := newAppHandler(app, nil)

	h.OnOpenSettings()
	first := h.settingsWin
	if first == nil {
		t.Fatal("settings window not created")
	}
	h.OnOpenSettings()
	if h.settingsWin != first {
		t.Error("second OnOpenSettings should re-use the existing window, not create a new one")
	}
}
