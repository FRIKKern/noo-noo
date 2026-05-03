package launchd

import (
	"fmt"
	"os"
	"os/exec"
)

// Runner abstracts the shell-out so tests can capture calls without exec.
type Runner interface {
	Run(name string, args ...string) ([]byte, error)
}

type execRunner struct{}

func (execRunner) Run(name string, args ...string) ([]byte, error) {
	return exec.Command(name, args...).CombinedOutput()
}

// Manager wraps launchctl invocations.
type Manager struct {
	r Runner
}

// New returns a Manager backed by os/exec.
func New() *Manager { return &Manager{r: execRunner{}} }

// NewWithRunner returns a Manager backed by a custom Runner (for tests).
func NewWithRunner(r Runner) *Manager { return &Manager{r: r} }

// Install runs `launchctl bootstrap gui/<uid> <plist>`.
func (m *Manager) Install(plistPath string) error {
	domain := fmt.Sprintf("gui/%d", os.Getuid())
	out, err := m.r.Run("launchctl", "bootstrap", domain, plistPath)
	if err != nil {
		return fmt.Errorf("launchctl bootstrap: %w (%s)", err, out)
	}
	return nil
}

// Uninstall runs `launchctl bootout gui/<uid> <plist>`.
func (m *Manager) Uninstall(plistPath string) error {
	domain := fmt.Sprintf("gui/%d", os.Getuid())
	out, err := m.r.Run("launchctl", "bootout", domain, plistPath)
	if err != nil {
		return fmt.Errorf("launchctl bootout: %w (%s)", err, out)
	}
	return nil
}
