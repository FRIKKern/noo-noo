package menubar

import (
	"strconv"
	"strings"
)

// Handler is the interface the menubar logic calls into when an item is
// clicked. cmd/noo-noo-app implements this against its real ipc.Client and
// Wails app handle; tests pass a recording stub.
type Handler interface {
	OnScanNow()
	OnOpenSettings()
	OnQuit()
	OnSuggestion(id int)
}

// TriggerThenRefresh is a sugar wrapper for the "Run Scan Now" handler:
// scanFn() runs the IPC TriggerScan call and, on success, refreshFn() does
// a one-shot status poll so the badge updates immediately instead of waiting
// for the next 30 s tick. On scan error the refresh is skipped (the caller
// is expected to log).
func TriggerThenRefresh(scanFn func() error, refreshFn func()) {
	if err := scanFn(); err != nil {
		return // caller logs
	}
	refreshFn()
}

// Dispatch routes a clicked menu item ID to the appropriate handler call.
// Unknown IDs are ignored (so future menu items can land without crashing
// older binaries).
func Dispatch(h Handler, id string) {
	switch id {
	case "scan-now":
		h.OnScanNow()
	case "settings":
		h.OnOpenSettings()
	case "quit":
		h.OnQuit()
	default:
		if rest, ok := strings.CutPrefix(id, "sugg-"); ok {
			n, err := strconv.Atoi(rest)
			if err == nil {
				h.OnSuggestion(n)
			}
		}
	}
}
