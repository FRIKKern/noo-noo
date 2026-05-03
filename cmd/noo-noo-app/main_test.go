package main

import (
	"testing"

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

type fakeTray struct {
	title string
	icon  menubar.Icon
	menu  *menubar.Menu
}

func (f *fakeTray) SetTitle(s string)       { f.title = s }
func (f *fakeTray) SetIcon(i menubar.Icon)  { f.icon = i }
func (f *fakeTray) SetMenu(m *menubar.Menu) { f.menu = m }
