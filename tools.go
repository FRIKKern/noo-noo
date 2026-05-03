//go:build tools

// Package tools holds blank imports for runtime dependencies that later
// Phase 0.2/0.3 tasks will use (config TOML parser, pure-Go SQLite driver,
// and the Wails v3 alpha menubar framework).
//
// The 'tools' build tag keeps this file out of normal builds while ensuring
// `go mod tidy` retains the deps in go.mod until they have real importers.
package tools

import (
	_ "github.com/pelletier/go-toml/v2"
	_ "github.com/wailsapp/wails/v3"
	_ "modernc.org/sqlite"
)
