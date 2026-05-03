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
