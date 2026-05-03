// Package notify sends macOS user notifications by shelling out to osascript.
// CGo-free; no Cocoa dependency.
package notify

import (
	"fmt"
	"os/exec"
	"strings"
)

// Runner abstracts os/exec for tests.
type Runner interface {
	Run(name string, args ...string) ([]byte, error)
}

type execRunner struct{}

func (execRunner) Run(name string, args ...string) ([]byte, error) {
	return exec.Command(name, args...).CombinedOutput()
}

// Notifier wraps a Runner.
type Notifier struct{ r Runner }

// New returns a Notifier backed by os/exec.
func New() *Notifier { return &Notifier{r: execRunner{}} }

// NewWithRunner returns a Notifier with a custom Runner (tests).
func NewWithRunner(r Runner) *Notifier { return &Notifier{r: r} }

// Send displays an AppleScript notification. The deepLink is currently unused
// but kept for the Phase 0.4 bundle-id-aware version.
func (n *Notifier) Send(title, body, deepLink string) error {
	_ = deepLink // reserved for Phase 0.4
	script := fmt.Sprintf(`display notification "%s" with title "%s"`,
		escape(body), escape(title))
	if _, err := n.r.Run("osascript", "-e", script); err != nil {
		return fmt.Errorf("osascript: %w", err)
	}
	return nil
}

// escape quotes for AppleScript string literals.
func escape(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	return s
}

// Send is a package-level convenience using the default Notifier.
func Send(title, body, deepLink string) error {
	return New().Send(title, body, deepLink)
}
