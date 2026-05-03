package scan

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/FRIKKern/noo-noo/internal/store"
)

func init() {
	// Override the no-op default installed by scan.go (T84) with the real
	// repo-idleness collector implemented in this file (T85).
	scanReposFn = scanRepos
}

// scanRepos walks each root looking for .git directories, computes the
// most-recent-activity timestamp for each repo, and upserts a
// repo_idleness row. Per-entry filesystem errors are tolerated; only
// ctx cancellation aborts a walk.
func scanRepos(ctx context.Context, roots []string, st *store.Store) error {
	for _, root := range roots {
		if err := scanOneRoot(ctx, root, st); err != nil {
			return err
		}
	}
	return nil
}

func scanOneRoot(ctx context.Context, root string, st *store.Store) error {
	now := time.Now()
	walkErr := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // tolerate per-entry errors
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		if !d.IsDir() {
			return nil
		}
		base := filepath.Base(p)
		// depth cap: don't recurse into node_modules / vendor; .git itself
		// signals a repo and we stop descending after recording it.
		switch base {
		case "node_modules", "vendor":
			return fs.SkipDir
		case ".git":
			repo := filepath.Dir(p)
			last := lastActive(repo)
			nmBytes := nodeModulesBytes(repo)
			_ = st.UpsertRepoIdleness(store.RepoIdleness{
				Path:             repo,
				LastCommitAt:     last,
				NodeModulesBytes: nmBytes,
				LastScanAt:       now,
			})
			return fs.SkipDir
		}
		return nil
	})
	// Missing root / unreadable root is fine; we only surface ctx errors.
	if walkErr != nil && ctx.Err() != nil {
		return ctx.Err()
	}
	return nil
}

// lastActive returns the more-recent of HEAD commit time and node_modules mtime.
// Returns the zero time if neither is available (e.g. empty .git).
func lastActive(repo string) time.Time {
	var t time.Time
	if commitTime, ok := readHeadCommitTime(repo); ok {
		t = commitTime
	}
	nmInfo, err := os.Stat(filepath.Join(repo, "node_modules"))
	if err == nil && nmInfo.ModTime().After(t) {
		t = nmInfo.ModTime()
	}
	return t
}

// nodeModulesBytes returns the directory entry size for the repo's
// node_modules directory, or 0 if it doesn't exist. The caches collector
// computes accurate per-tree sizes for dedicated cache roots; here we just
// want a proxy for "how much would purging this repo reclaim".
func nodeModulesBytes(repo string) int64 {
	info, err := os.Stat(filepath.Join(repo, "node_modules"))
	if err != nil {
		return 0
	}
	if !info.IsDir() {
		return 0
	}
	return info.Size()
}

// readHeadCommitTime is a minimal pure-Go HEAD -> commit-timestamp resolver.
// We avoid spawning git to keep the daemon dependency-free.
func readHeadCommitTime(repo string) (time.Time, bool) {
	headPath := filepath.Join(repo, ".git", "HEAD")
	raw, err := os.ReadFile(headPath)
	if err != nil {
		return time.Time{}, false
	}
	head := strings.TrimSpace(string(raw))
	if strings.HasPrefix(head, "ref: ") {
		ref := strings.TrimPrefix(head, "ref: ")
		commitFile := filepath.Join(repo, ".git", ref)
		sha, err := os.ReadFile(commitFile)
		if err != nil {
			// packed ref fallback: use refs/heads dir mtime
			if info, statErr := os.Stat(filepath.Join(repo, ".git", "refs", "heads")); statErr == nil {
				return info.ModTime(), true
			}
			return time.Time{}, false
		}
		return objectCommitTime(repo, strings.TrimSpace(string(sha)))
	}
	// detached HEAD; the file IS the commit sha.
	return objectCommitTime(repo, head)
}

// objectCommitTime locates the loose object .git/objects/<sha[0:2]>/<sha[2:]>
// and uses its file mtime as a coarse proxy for the commit timestamp. A full
// implementation would zlib-decode the object and parse the "committer" line;
// the mtime is good enough (within hours) for the idleness heuristic.
func objectCommitTime(repo, sha string) (time.Time, bool) {
	if len(sha) < 4 {
		return time.Time{}, false
	}
	obj := filepath.Join(repo, ".git", "objects", sha[:2], sha[2:])
	info, err := os.Stat(obj)
	if err != nil {
		// packed object: fall back to refs/heads mtime
		if rinfo, rerr := os.Stat(filepath.Join(repo, ".git", "refs", "heads")); rerr == nil {
			return rinfo.ModTime(), true
		}
		return time.Time{}, false
	}
	return info.ModTime(), true
}
