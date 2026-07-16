package scan

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLastActiveUsesNodeModulesMtime(t *testing.T) {
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, ".git", "refs", "heads"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(repo, "node_modules"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Set node_modules mtime to 5 days ago.
	five := time.Now().Add(-5 * 24 * time.Hour)
	if err := os.Chtimes(filepath.Join(repo, "node_modules"), five, five); err != nil {
		t.Fatal(err)
	}
	got := lastActive(repo)
	if got.Before(five.Add(-time.Hour)) || got.After(five.Add(time.Hour)) {
		t.Errorf("lastActive = %v, want ~%v", got, five)
	}
}

func TestLastActiveZeroForEmptyRepo(t *testing.T) {
	repo := t.TempDir()
	got := lastActive(repo)
	if !got.IsZero() {
		t.Errorf("lastActive on empty dir = %v, want zero", got)
	}
}

// TestScanReposWritesRow asserts that walking a root containing a .git dir
// upserts a repo_idleness row that ListIdleRepos can find.
func TestScanReposWritesRow(t *testing.T) {
	root := t.TempDir()
	repo := filepath.Join(root, "myrepo")
	if err := os.MkdirAll(filepath.Join(repo, ".git", "refs", "heads"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(repo, "node_modules"), 0o755); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-90 * 24 * time.Hour)
	if err := os.Chtimes(filepath.Join(repo, "node_modules"), old, old); err != nil {
		t.Fatal(err)
	}

	st := openTestStore(t)
	if err := scanRepos(context.Background(), []string{root}, st); err != nil {
		t.Fatalf("scanRepos: %v", err)
	}

	rows, err := st.ListIdleRepos(30, 0, time.Now())
	if err != nil {
		t.Fatalf("ListIdleRepos: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected 1 idle repo row, got %d", len(rows))
	}
	// scanOneRoot now resolves the root's symlinks before walking (charter
	// D25), so recorded paths are canonical. On macOS t.TempDir() lives under
	// /var -> /private/var, so the stored path is the resolved form.
	wantPath, err := filepath.EvalSymlinks(repo)
	if err != nil {
		t.Fatal(err)
	}
	if rows[0].Path != wantPath {
		t.Errorf("path = %q, want %q", rows[0].Path, wantPath)
	}
}

// TestScanReposFollowsSymlinkedRoot pins charter D25: when a scan root is
// itself a symlink to a directory (the ~/Documents/GitHub -> /Volumes/SATECHI
// home-relocation shape), the repo inside the target must still be discovered.
// filepath.WalkDir does NOT descend a symlinked root — it visits the symlink as
// one entry and stops — so this FAILS on main; scanOneRoot's EvalSymlinks makes
// it pass.
func TestScanReposFollowsSymlinkedRoot(t *testing.T) {
	real := t.TempDir()
	repo := filepath.Join(real, "myrepo")
	if err := os.MkdirAll(filepath.Join(repo, ".git", "refs", "heads"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(repo, "node_modules"), 0o755); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-90 * 24 * time.Hour)
	if err := os.Chtimes(filepath.Join(repo, "node_modules"), old, old); err != nil {
		t.Fatal(err)
	}

	// The scan root is a SYMLINK pointing at `real`.
	link := filepath.Join(t.TempDir(), "github-link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}

	st := openTestStore(t)
	if err := scanRepos(context.Background(), []string{link}, st); err != nil {
		t.Fatalf("scanRepos: %v", err)
	}
	rows, err := st.ListIdleRepos(30, 0, time.Now())
	if err != nil {
		t.Fatalf("ListIdleRepos: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("symlinked root should surface the planted repo, got %d rows (D25 regression)", len(rows))
	}
}

// TestScanReposMissingRootIsTolerated ensures a non-existent root doesn't
// abort the whole scan.
func TestScanReposMissingRootIsTolerated(t *testing.T) {
	st := openTestStore(t)
	err := scanRepos(context.Background(), []string{"/this/really/does/not/exist"}, st)
	if err != nil {
		t.Fatalf("missing root should be tolerated, got %v", err)
	}
}
