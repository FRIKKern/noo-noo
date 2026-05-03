package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	toml "github.com/pelletier/go-toml/v2"

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

// SaveConfig writes cfg to b.configPath atomically (write-then-rename).
// Returns the marshalling or fs error verbatim so the Svelte side can show it.
func (b *Bindings) SaveConfig(cfg config.Config) error {
	body, err := toml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(b.configPath), 0o755); err != nil {
		return fmt.Errorf("mkdir: %w", err)
	}
	tmp := b.configPath + ".tmp"
	if err := os.WriteFile(tmp, body, 0o600); err != nil {
		return fmt.Errorf("write tmp: %w", err)
	}
	if err := os.Rename(tmp, b.configPath); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("rename: %w", err)
	}
	return nil
}
