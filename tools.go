//go:build tools

// Package tools holds blank imports for runtime dependencies that later
// Phase 0.2 tasks will use (config TOML parser and pure-Go SQLite driver).
//
// The 'tools' build tag keeps this file out of normal builds while ensuring
// `go mod tidy` retains the deps in go.mod until task 23+ wires them in.
package tools

import (
	_ "github.com/pelletier/go-toml/v2"
	_ "modernc.org/sqlite"
)
