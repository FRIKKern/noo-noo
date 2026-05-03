// Command noo-noo-app is the macOS menubar companion for the noo-noo daemon.
// LSUIElement so no dock icon appears (set in Info.plist).
package main

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"

	"github.com/FRIKKern/noo-noo/internal/ipc"
	"github.com/FRIKKern/noo-noo/internal/menubar"
)

// Tray is the subset of the Wails tray we depend on, so tests can pass a
// fake without booting Wails.
type Tray interface {
	SetTitle(string)
	SetIcon(menubar.Icon)
	SetMenu(*menubar.Menu)
}

type buildOpts struct {
	// Headless skips creating the system tray (used by tests so CI doesn't
	// need a display server).
	Headless bool
}

func buildApp(_ buildOpts) *application.App {
	return application.New(application.Options{
		Name:        "Noo-Noo",
		Description: "Smart cleanup for Mac developers",
		Mac: application.MacOptions{
			ActivationPolicy: application.ActivationPolicyAccessory, // == LSUIElement
		},
		// Services exposes Bindings methods to the Settings webview JS.
		// Real GetConfig / SaveConfig methods are appended in tasks 62-63.
		Services: []application.Service{application.NewService(NewBindings())},
	})
}

// refreshTray re-renders the tray title, icon, and dropdown menu from a
// freshly polled status. Pure function over the Tray interface so tests can
// drive it with a fake.
func refreshTray(t Tray, st menubar.Status) {
	switch {
	case !st.Running:
		t.SetTitle("!")
		t.SetIcon(menubar.ForState(menubar.StateIdle))
	case st.OpenSuggestions > 0:
		t.SetTitle(fmt.Sprintf("%d", st.OpenSuggestions))
		t.SetIcon(menubar.ForState(menubar.StateHasSuggestions))
	default:
		t.SetTitle("")
		t.SetIcon(menubar.ForState(menubar.StateIdle))
	}
	t.SetMenu(menubar.Build(st, nil)) // suggestions submenu wired in Task 57
}

// appHandler implements menubar.Handler against the live Wails app and IPC
// client. Click dispatch funnels into these methods via menubar.Dispatch.
type appHandler struct {
	app         *application.App
	cli         *ipc.Client
	settingsWin *application.WebviewWindow
}

func newAppHandler(app *application.App, cli *ipc.Client) *appHandler {
	return &appHandler{app: app, cli: cli}
}

func (h *appHandler) OnScanNow() { /* Task 58 */ }

// OnOpenSettings opens (or re-focuses) the singleton Settings webview window.
// The window is allocated on first click; subsequent clicks Show()+Focus()
// the existing window so we never end up with multiple settings panels open.
// On window close we clear the field so the next click re-creates it.
func (h *appHandler) OnOpenSettings() {
	if h.settingsWin != nil {
		h.settingsWin.Show()
		h.settingsWin.Focus()
		return
	}
	h.settingsWin = h.app.Window.NewWithOptions(application.WebviewWindowOptions{
		Title:  "Noo-Noo Settings",
		Width:  520,
		Height: 640,
		URL:    "/#/settings",
		Mac: application.MacWindow{
			InvisibleTitleBarHeight: 0,
			TitleBar:                application.MacTitleBarDefault,
		},
	})
	h.settingsWin.OnWindowEvent(events.Common.WindowClosing, func(_ *application.WindowEvent) {
		h.settingsWin = nil
	})
}

func (h *appHandler) OnQuit()            { h.app.Quit() }
func (h *appHandler) OnSuggestion(_ int) { /* Task 57 */ }

// ipcClientShim adapts *ipc.Client (which returns ipc.StatusResponse) to the
// menubar.StatusClient interface. Lives here so internal/ipc stays unaware
// of the menubar package.
type ipcClientShim struct{ c *ipc.Client }

func (s *ipcClientShim) Status() (menubar.Status, error) {
	r, err := s.c.DaemonStatus()
	if err != nil {
		return menubar.Status{}, err
	}
	st := menubar.Status{Running: r.Running}
	// Suggestion count is a best-effort second call; failure is non-fatal so
	// the tray still updates the daemon-up badge.
	if items, err := s.c.SuggestionsList(); err == nil {
		st.OpenSuggestions = len(items)
	}
	return st, nil
}

// trayAdapter wraps the Wails tray so it satisfies our Tray interface.
// The handler is captured so menu clicks can be routed through
// menubar.Dispatch when SetMenu is called.
type trayAdapter struct {
	inner   *application.SystemTray
	handler menubar.Handler
}

func newTrayAdapter(t *application.SystemTray, h menubar.Handler) *trayAdapter {
	return &trayAdapter{inner: t, handler: h}
}
func (a *trayAdapter) SetTitle(s string) { a.inner.SetLabel(s) }
func (a *trayAdapter) SetIcon(i menubar.Icon) {
	if i.Template {
		a.inner.SetTemplateIcon(i.PNG)
		return
	}
	a.inner.SetIcon(i.PNG)
}

// SetMenu translates the dependency-free menubar.Menu into a real
// *application.Menu (with click callbacks routed back through
// menubar.Dispatch via the captured handler) and attaches it.
func (a *trayAdapter) SetMenu(m *menubar.Menu) {
	a.inner.SetMenu(buildWailsMenu(m, a.handler))
}

func main() {
	app := buildApp(buildOpts{})
	cli, err := ipc.Dial(ipc.SocketEnv())
	if err != nil {
		log.Fatalf("ipc: %v", err)
	}
	defer func() { _ = cli.Close() }()

	tray := app.SystemTray.New()
	handler := newAppHandler(app, cli)
	adapter := newTrayAdapter(tray, handler)

	poller := menubar.NewPoller(&ipcClientShim{c: cli}, 30*time.Second,
		func(st menubar.Status) { refreshTray(adapter, st) })
	go poller.Run(context.Background())

	if err := app.Run(); err != nil {
		log.Fatalf("noo-noo-app: %v", err)
	}
}
