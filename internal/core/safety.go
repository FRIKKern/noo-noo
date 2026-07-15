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

// leakCategoricallyBlocked are prefixes no leak signature may ever target.
// This is the categorical floor of the leak carve-out: /private/ is
// deliberately absent (that is the carve-out), everything else stays walled.
var leakCategoricallyBlocked = []string{
	"/System/",
	"/Library/",
	"/usr/",
	"/bin/",
	"/sbin/",
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

// CanDeleteLeakTarget is the signature-scoped parallel predicate for leak
// deletes. Generic CanDelete stays UNCHANGED and keeps hard-blocking all of
// /private/ — this predicate never widens that wall; it is a separate,
// NARROWER gate that leak modules use instead of it:
//
//   - the path is resolved (abs, symlinks evaluated, cleaned) and the
//     RESOLVED path must exactly match one of the signature's globs — a
//     matched LEAF. A glob's parent directory never matches a full pattern,
//     so parents are structurally rejected, and a symlink pointing outside
//     the signature's shape resolves away from the glob and is rejected too;
//   - /System/, /Library/, /usr/, /bin/, /sbin/ are categorically rejected
//     (checked both before and after symlink resolution), even if a
//     misauthored signature glob were to match inside them;
//   - the caller-supplied blocklist (e.g. ".git") still applies.
//
// SECURITY-SENSITIVE: keep this narrow. It exists so the flagship Chrome
// code_sign_clone fix can ship an Apply without weakening the general wall.
func (s *Safety) CanDeleteLeakTarget(path string, sigGlobs []string) error {
	abs, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("absolutize %q: %w", path, err)
	}
	if err := leakCategoricalCheck(filepath.Clean(abs)); err != nil {
		return err
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return fmt.Errorf("resolve %q: %w — refusing unresolvable leak target", abs, err)
	}
	clean := filepath.Clean(resolved)
	if err := leakCategoricalCheck(clean); err != nil {
		return err
	}

	for _, b := range s.blocked {
		for _, part := range strings.Split(clean, string(filepath.Separator)) {
			if part == b {
				return fmt.Errorf("path %q contains blocked component %q", clean, b)
			}
		}
	}

	for _, g := range sigGlobs {
		ok, err := filepath.Match(g, clean)
		if err != nil {
			continue // malformed pattern can never authorize anything
		}
		if ok {
			return nil
		}
	}
	return fmt.Errorf("resolved path %q matches no leak-signature glob %v — only glob-matched leaf paths are deletable", clean, sigGlobs)
}

func leakCategoricalCheck(clean string) error {
	for _, b := range leakCategoricallyBlocked {
		if strings.HasPrefix(clean+"/", b) {
			return fmt.Errorf("leak target %q is in categorically-blocked system area %q", clean, b)
		}
	}
	return nil
}
