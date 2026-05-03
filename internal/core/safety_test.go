package core

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSafetyAllow(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	allowedDir := filepath.Join(home, "ok")
	if err := os.MkdirAll(allowedDir, 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(allowedDir, "subdir")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}

	s := NewSafety([]string{allowedDir}, nil)
	if err := s.CanDelete(target); err != nil {
		t.Errorf("expected allowed, got: %v", err)
	}
}

func TestSafetyOutsideAllowlist(t *testing.T) {
	s := NewSafety([]string{"/Users/somebody/ok"}, nil)
	if err := s.CanDelete("/etc/passwd"); err == nil {
		t.Error("expected denial for /etc/passwd")
	}
}

func TestSafetyBlockedSubpath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	allowedDir := filepath.Join(home, "ok")
	if err := os.MkdirAll(filepath.Join(allowedDir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}

	s := NewSafety([]string{allowedDir}, []string{".git"})
	if err := s.CanDelete(filepath.Join(allowedDir, ".git")); err == nil {
		t.Error("expected denial for .git")
	}
}

func TestSafetySystemPathsAlwaysBlocked(t *testing.T) {
	s := NewSafety([]string{"/"}, nil)
	for _, p := range []string{"/System/Library/whatever", "/Library/LaunchDaemons/foo"} {
		if err := s.CanDelete(p); err == nil {
			t.Errorf("expected denial for system path %q", p)
		}
	}
}

func TestSafetyTraversal(t *testing.T) {
	s := NewSafety([]string{"/Users/foo/ok"}, nil)
	if err := s.CanDelete("/Users/foo/ok/../bad"); err == nil {
		t.Error("expected denial for path traversal")
	}
}
