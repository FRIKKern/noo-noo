package core

// Permanent protective baseline for the /private/ leak carve-out (charter
// D4). The generic Safety.CanDelete wall and the signature-scoped
// CanDeleteLeakTarget predicate are PARALLEL: the carve-out must never widen
// the generic wall, and the predicate itself must accept nothing but
// glob-matched leaf paths. If any test in this file fails, a safety
// regression has been introduced — do not "fix" the test.

import (
	"os"
	"path/filepath"
	"testing"
)

// The founding incident's literal path shape: a leaked Chrome code-sign
// clone under the darwin per-user temp tree.
const leakedClonePath = "/private/var/folders/hb/18wmml0n5495w28s_1z_8flh0000gn/X/com.google.Chrome.code_sign_clone/leaked-clone-42"

// TestCanDeleteRejectsLeakedClonePathEvenWhenAllowlisted proves the generic
// wall is unchanged by the leak work: even when a caller explicitly
// allowlists the clone's parent directory, CanDelete still refuses, because
// alwaysBlocked (/private/) wins over caller roots by ordering.
func TestCanDeleteRejectsLeakedClonePathEvenWhenAllowlisted(t *testing.T) {
	s := NewSafety([]string{filepath.Dir(leakedClonePath)}, nil)
	if err := s.CanDelete(leakedClonePath); err == nil {
		t.Fatalf("generic CanDelete permitted %q despite the allowlist — the /private/ wall has been breached", leakedClonePath)
	}
	// The whole /private/ subtree stays generically blocked.
	s = NewSafety([]string{"/private"}, nil)
	if err := s.CanDelete("/private/tmp/claude-something"); err == nil {
		t.Fatal("generic CanDelete permitted a /private/tmp path — the /private/ wall has been breached")
	}
}

// cloneFixture builds a real on-disk replica of the clone layout inside a
// temp dir and returns the (symlink-resolved) leaf, its parent, and a glob
// with the same shape as the shipped chrome-code-sign-clone signature.
func cloneFixture(t *testing.T) (leaf, parent, glob string) {
	t.Helper()
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	parent = filepath.Join(base, "com.google.Chrome.code_sign_clone")
	leaf = filepath.Join(parent, "code_sign_clone.abc123")
	if err := os.MkdirAll(leaf, 0o755); err != nil {
		t.Fatal(err)
	}
	glob = filepath.Join(base, "*.code_sign_clone", "code_sign_clone.??????")
	return leaf, parent, glob
}

// TestCanDeleteLeakTargetAcceptsOnlyGlobMatchedLeaf proves the predicate's
// positive and negative space: the exact glob-matched leaf is accepted; the
// glob's parent dir, non-matching siblings, and arbitrary /private/tmp paths
// are all rejected.
func TestCanDeleteLeakTargetAcceptsOnlyGlobMatchedLeaf(t *testing.T) {
	leaf, parent, glob := cloneFixture(t)
	s := NewSafety(nil, nil)

	if err := s.CanDeleteLeakTarget(leaf, []string{glob}); err != nil {
		t.Errorf("expected glob-matched leaf %q accepted, got: %v", leaf, err)
	}
	if err := s.CanDeleteLeakTarget(parent, []string{glob}); err == nil {
		t.Errorf("glob PARENT dir %q was accepted — parents must never be deletable", parent)
	}
	sibling := filepath.Join(parent, "unrelated-file")
	if err := os.WriteFile(sibling, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := s.CanDeleteLeakTarget(sibling, []string{glob}); err == nil {
		t.Errorf("non-matching sibling %q was accepted", sibling)
	}
	if err := s.CanDeleteLeakTarget("/private/tmp/definitely-not-a-leak", []string{glob}); err == nil {
		t.Error("non-matching /private/tmp path was accepted")
	}
}

// TestCanDeleteLeakTargetCategoricallyRejectsSystemAreas proves that even a
// misauthored (or malicious) signature glob cannot authorize deletes inside
// /System/, /Library/, /usr/, /bin/, /sbin/.
func TestCanDeleteLeakTargetCategoricallyRejectsSystemAreas(t *testing.T) {
	s := NewSafety(nil, nil)
	cases := []struct{ path, glob string }{
		{"/System/Library/CoreServices", "/System/Library/*"},
		{"/Library/Preferences", "/Library/*"},
		{"/usr/local", "/usr/*"},
		{"/bin/ls", "/bin/*"},
		{"/sbin/mount", "/sbin/*"},
	}
	for _, c := range cases {
		if err := s.CanDeleteLeakTarget(c.path, []string{c.glob}); err == nil {
			t.Errorf("categorically-blocked path %q was accepted via glob %q", c.path, c.glob)
		}
	}
}

// TestCanDeleteLeakTargetRejectsSymlinkEscape proves symlink resolution: a
// leaf-shaped symlink pointing outside the signature's shape resolves away
// from the glob and is rejected.
func TestCanDeleteLeakTargetRejectsSymlinkEscape(t *testing.T) {
	leaf, parent, glob := cloneFixture(t)
	// Replace the real leaf with a symlink to a victim dir elsewhere.
	if err := os.RemoveAll(leaf); err != nil {
		t.Fatal(err)
	}
	victim := filepath.Join(filepath.Dir(parent), "victim-data")
	if err := os.MkdirAll(victim, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(victim, leaf); err != nil {
		t.Fatal(err)
	}

	s := NewSafety(nil, nil)
	if err := s.CanDeleteLeakTarget(leaf, []string{glob}); err == nil {
		t.Errorf("symlink %q -> %q was accepted — resolution must defeat the escape", leaf, victim)
	}
}

// TestCanDeleteLeakTargetHonorsCallerBlocklist proves the caller-supplied
// blocklist still applies inside the carve-out.
func TestCanDeleteLeakTargetHonorsCallerBlocklist(t *testing.T) {
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(base, ".git")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	s := NewSafety(nil, []string{".git"})
	if err := s.CanDeleteLeakTarget(target, []string{filepath.Join(base, "*")}); err == nil {
		t.Error("blocked component .git was accepted by CanDeleteLeakTarget")
	}
}
