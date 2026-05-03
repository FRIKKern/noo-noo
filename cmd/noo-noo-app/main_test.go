package main

import "testing"

// TestMainNoOp verifies that calling buildApp() with the headless option
// returns a non-nil Wails application instance and does not panic.
// We do NOT call app.Run() in tests — that would block on the event loop.
func TestMainNoOp(t *testing.T) {
	app := buildApp(buildOpts{Headless: true})
	if app == nil {
		t.Fatal("buildApp returned nil")
	}
}
