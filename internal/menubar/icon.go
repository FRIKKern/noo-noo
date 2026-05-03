// Package menubar contains the platform-independent logic for the noo-noo
// menubar app: icon resolution, menu construction, click dispatch, and the
// status-poll loop. Wails-specific wiring lives in cmd/noo-noo-app.
package menubar

import _ "embed"

//go:embed assets/icon-idle.png
var iconIdle []byte

//go:embed assets/icon-scanning.png
var iconScanning []byte

//go:embed assets/icon-suggest.png
var iconSuggest []byte

// State is the menubar visual state.
type State int

const (
	StateIdle State = iota
	StateScanning
	StateHasSuggestions
)

// Icon bundles the PNG bytes plus a flag instructing macOS to treat the image
// as a "template" (recolored automatically for light/dark menubar).
type Icon struct {
	PNG      []byte
	Template bool
}

// Default returns the icon used at app launch (idle state).
func Default() Icon { return ForState(StateIdle) }

// ForState returns the embedded icon for the given visual state.
func ForState(s State) Icon {
	switch s {
	case StateScanning:
		return Icon{PNG: iconScanning, Template: true}
	case StateHasSuggestions:
		return Icon{PNG: iconSuggest, Template: true}
	default:
		return Icon{PNG: iconIdle, Template: true}
	}
}
