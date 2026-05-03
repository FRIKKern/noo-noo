package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/FRIKKern/noo-noo/internal/config"
)

// Bindings exposes Go methods to the Svelte frontend via Wails. Methods must
// have JSON-marshallable parameter and return types.
type Bindings struct {
	// configPath is the file the Settings UI reads/writes. Defaults to
	// ~/.config/noo-noo/config.toml; tests override.
	configPath string
}

// NewBindings constructs a Bindings rooted at the default config path.
func NewBindings() *Bindings {
	home, _ := os.UserHomeDir()
	return &Bindings{configPath: filepath.Join(home, ".config", "noo-noo", "config.toml")}
}

// GetConfig reads ~/.config/noo-noo/config.toml (or returns defaults if
// missing) and returns it for Svelte two-way binding.
func (b *Bindings) GetConfig() (config.Config, error) {
	return config.Load(b.configPath)
}

// OpenConfigInEditor opens the raw TOML file in the user's default text editor
// via "open -t". Returns immediately; does not wait for the editor to close.
func (b *Bindings) OpenConfigInEditor() error {
	if _, err := os.Stat(b.configPath); os.IsNotExist(err) {
		if err := os.MkdirAll(filepath.Dir(b.configPath), 0o755); err != nil {
			return fmt.Errorf("mkdir: %w", err)
		}
		if err := os.WriteFile(b.configPath, []byte("# noo-noo config\n"), 0o600); err != nil {
			return fmt.Errorf("seed: %w", err)
		}
	}
	return exec.Command("open", "-t", b.configPath).Start()
}
