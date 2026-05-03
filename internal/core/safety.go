package core

import (
	"fmt"
	"path/filepath"
	"strings"
)

// Safety enforces that destructive filesystem operations only target paths
// inside an allowlist of root prefixes and outside a hard blocklist.
type Safety struct {
	roots   []string // absolute, cleaned
	blocked []string // basename or path fragment to block (e.g. ".git", ".env")
}

// NewSafety constructs a Safety from a list of root prefixes that destructive
// ops are permitted to touch, plus a list of basenames that are always blocked.
func NewSafety(roots, blocked []string) *Safety {
	cleaned := make([]string, 0, len(roots))
	for _, r := range roots {
		abs, err := filepath.Abs(r)
		if err != nil {
			continue
		}
		cleaned = append(cleaned, filepath.Clean(abs))
	}
	return &Safety{roots: cleaned, blocked: blocked}
}

// alwaysBlocked is a hard-coded list of system paths that are never permitted
// regardless of caller configuration.
var alwaysBlocked = []string{
	"/System/",
	"/Library/",
	"/usr/",
	"/bin/",
	"/sbin/",
	"/private/",
}

// CanDelete returns nil if path is permitted to be removed, or an error
// describing why it is denied.
func (s *Safety) CanDelete(path string) error {
	abs, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("absolutize %q: %w", path, err)
	}
	clean := filepath.Clean(abs)

	// Always-blocked system paths.
	for _, b := range alwaysBlocked {
		if strings.HasPrefix(clean+"/", b) {
			return fmt.Errorf("path %q is in always-blocked system area %q", clean, b)
		}
	}

	// Caller-supplied blocklist (basenames in the path).
	for _, b := range s.blocked {
		for _, part := range strings.Split(clean, string(filepath.Separator)) {
			if part == b {
				return fmt.Errorf("path %q contains blocked component %q", clean, b)
			}
		}
	}

	// Must be inside one of the allowed roots.
	for _, r := range s.roots {
		rel, err := filepath.Rel(r, clean)
		if err != nil {
			continue
		}
		if !strings.HasPrefix(rel, "..") && rel != "." {
			return nil
		}
	}
	return fmt.Errorf("path %q is outside the allowed roots %v", clean, s.roots)
}
