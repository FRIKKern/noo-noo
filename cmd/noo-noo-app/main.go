// Command noo-noo-app is the macOS menubar companion for the noo-noo daemon.
// LSUIElement so no dock icon appears (set in Info.plist).
package main

import (
	"log"

	"github.com/wailsapp/wails/v3/pkg/application"
)

type buildOpts struct {
	// Headless skips creating the system tray (used by tests so CI doesn't
	// need a display server).
	Headless bool
}

func buildApp(opts buildOpts) *application.App {
	app := application.New(application.Options{
		Name:        "Noo-Noo",
		Description: "Smart cleanup for Mac developers",
		Mac: application.MacOptions{
			ActivationPolicy: application.ActivationPolicyAccessory, // == LSUIElement
		},
	})
	if !opts.Headless {
		// Real tray creation lands in Task 55 once internal/menubar is wired.
		_ = app.SystemTray.New()
	}
	return app
}

func main() {
	app := buildApp(buildOpts{})
	if err := app.Run(); err != nil {
		log.Fatalf("noo-noo-app: %v", err)
	}
}
