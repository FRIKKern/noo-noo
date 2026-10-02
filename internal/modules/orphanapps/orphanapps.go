// Package orphanapps finds data left behind by UNINSTALLED applications:
// ~/Library/Application Support/<X>, ~/Library/Containers/<bundle-id>,
// ~/Library/Caches/<X>, and a short curated list of home-dir app folders
// (~/DevKinsta, ~/Local Sites, ~/.docker). A dir is orphaned when no
// installed app resolves for its key (bundle id, app name, or loose name —
// see Index) AND nothing in it was modified for 30 days. Apple bundle ids
// and known system/toolchain dirs are never flagged; neither is anything
// under 50 MB. Sizes are truth-sized: Docker's 64 GB Docker.raw is 5.5 GB of
// real extents on a sparse file, and that is what the item says.
package orphanapps

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/FRIKKern/noo-noo/internal/core"
	"github.com/FRIKKern/noo-noo/internal/modules"
	"github.com/FRIKKern/noo-noo/internal/sizer"
)

// Curated is one home-dir folder known to belong to a specific app.
type Curated struct {
	Path string // absolute
	App  string // the app name to resolve, e.g. "DevKinsta"
}

// DefaultCurated returns the shipped curated list for home.
func DefaultCurated(home string) []Curated {
	return []Curated{
		{Path: filepath.Join(home, "DevKinsta"), App: "DevKinsta"},
		{Path: filepath.Join(home, "Local Sites"), App: "Local"},
		{Path: filepath.Join(home, ".docker"), App: "Docker"},
	}
}

// DefaultLibraryRoots returns the per-app data roots whose direct children
// are candidates.
func DefaultLibraryRoots(home string) []string {
	return []string{
		filepath.Join(home, "Library", "Application Support"),
		filepath.Join(home, "Library", "Containers"),
		filepath.Join(home, "Library", "Caches"),
	}
}

const (
	// MinSize is the floor below which a dir is never flagged.
	MinSize int64 = 50 << 20
	// IdleFor is how long a dir must be untouched before it counts as
	// abandoned, not merely unused since the last launch.
	IdleFor = 30 * 24 * time.Hour
)

// neverFlag are dir names (case-insensitive) that are not app data even
// though they live in an app-data root: Apple system stores without an
// app of their own (iPhone backups!), and toolchain caches that belong to
// the caches module, not to an uninstalled app.
var neverFlag = map[string]bool{
	"addressbook": true, "animoji": true, "app store": true, "apple": true, "callhistorydb": true,
	"callhistorytransactions": true, "clouddocs": true, "cloudkit": true, "controlcenter": true,
	"crashreporter": true, "differentialprivacy": true, "diskimages": true, "dock": true,
	"facetime": true, "fileprovider": true, "icloud": true, "knowledge": true, "mobilesync": true,
	"syncservices": true, "siri": true, "spotlight": true, "developer": true, "xcode": true,
	"instruments": true, "caches": true, "homebrew": true, "pip": true, "go-build": true,
	"yarn": true, "pnpm": true, "npm": true, "node-gyp": true, "typescript": true, "composer": true,
	"electron": true, "cypress": true, "ms-playwright": true, "deno": true, "bun": true, "uv": true,
	"cargo": true, "rustup": true, "pypoetry": true, "huggingface": true, "torch": true,
	"temporaryitems": true, "keychain": true, "mail": true, "messages": true, "photos": true,
	"notes": true, "reminders": true, "calendar": true, "contacts": true, "music": true,
}

// Module finds and removes orphaned app data.
type Module struct {
	roots   []string
	curated []Curated
	index   IndexBuilder
	safety  *core.Safety
	minSize int64
	idle    time.Duration
	uid     int
	now     func() time.Time
}

// New constructs a Module over the given library roots and curated list.
// index is consulted once per Scan and once per Apply (fresh verdict).
func New(roots []string, curated []Curated, index IndexBuilder) *Module {
	return &Module{
		roots:   roots,
		curated: curated,
		index:   index,
		safety:  core.NewSafety(roots, nil),
		minSize: MinSize,
		idle:    IdleFor,
		uid:     os.Getuid(),
		now:     time.Now,
	}
}

func (*Module) Name() string { return "apps" }

// candidate is one dir under judgement.
type candidate struct {
	path    string
	keyedBy string // "bundle-id" | "name" | "curated"
	key     string // what was looked up
}

// candidates enumerates every dir that could be orphaned, before any app
// resolution or sizing.
func (m *Module) candidates() []candidate {
	var out []candidate
	for _, root := range m.roots {
		entries, err := os.ReadDir(root)
		if err != nil {
			continue
		}
		for _, e := range entries {
			name := e.Name()
			if !e.IsDir() || e.Type()&os.ModeSymlink != 0 || excludedName(name) {
				continue
			}
			keyed := "name"
			if strings.Contains(name, ".") {
				keyed = "bundle-id"
			}
			out = append(out, candidate{path: filepath.Join(root, name), keyedBy: keyed, key: name})
		}
	}
	for _, c := range m.curated {
		fi, err := os.Lstat(c.Path)
		if err != nil || !fi.IsDir() {
			continue
		}
		out = append(out, candidate{path: c.Path, keyedBy: "curated", key: c.App})
	}
	return out
}

func excludedName(name string) bool {
	lower := strings.ToLower(name)
	return strings.HasPrefix(lower, ".") || strings.HasPrefix(lower, "com.apple.") || neverFlag[lower]
}

// Scan judges every candidate against a fresh Index and reports the
// orphaned, idle, large ones — truth-sized.
func (m *Module) Scan(ctx context.Context) (modules.Report, error) {
	rep := modules.Report{Module: "apps"}
	idx := m.index(ctx)
	for _, c := range m.candidates() {
		if err := ctx.Err(); err != nil {
			return rep, err
		}
		item, ok := m.inspect(c, idx)
		if !ok {
			continue
		}
		rep.Items = append(rep.Items, item)
		rep.Total += item.Size
	}
	return rep, nil
}

// inspect returns the item for c if it is orphaned (no app), idle (newest
// mtime past IdleFor), and large (real bytes >= MinSize). Cheap checks
// first: the resolver is a map lookup, the stat walk is a few µs per file,
// and only survivors pay for the extent walk.
func (m *Module) inspect(c candidate, idx *Index) (modules.Item, bool) {
	if app, ok := idx.Lookup(c.key); ok {
		_ = app
		return modules.Item{}, false
	}
	fi, err := os.Lstat(c.path)
	if err != nil || !fi.IsDir() || fi.Mode()&os.ModeSymlink != 0 {
		return modules.Item{}, false
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); !ok || int(st.Uid) != m.uid {
		return modules.Item{}, false
	}
	blocks, newest, err := statWalk(c.path)
	if err != nil || blocks < m.minSize {
		return modules.Item{}, false
	}
	idle := m.now().Sub(newest)
	if idle < m.idle {
		return modules.Item{}, false
	}
	ts, err := sizer.UniqueAllocated(c.path)
	if err != nil || ts.UniqueAllocated < m.minSize {
		return modules.Item{}, false
	}
	ev := map[string]string{
		"keyed_by":               c.keyedBy,
		"expected_app":           c.key,
		"reason":                 fmt.Sprintf("no installed app resolves for %q (%s); untouched for %d days", c.key, c.keyedBy, int(idle.Hours()/24)),
		"newest_mtime":           newest.UTC().Format(time.RFC3339),
		"idle":                   idle.Round(time.Hour).String(),
		"unique_allocated_bytes": strconv.FormatInt(ts.UniqueAllocated, 10),
		"blocks_bytes":           strconv.FormatInt(ts.Blocks, 10),
		"logical_bytes":          strconv.FormatInt(ts.Logical, 10),
		"suggestion":             fmt.Sprintf("Delete — %s is not installed", c.key),
	}
	if fiction := ts.Blocks - ts.UniqueAllocated; fiction > 0 {
		ev["du_fiction_bytes"] = strconv.FormatInt(fiction, 10)
	}
	if sparse := ts.Logical - ts.UniqueAllocated; sparse > 0 {
		ev["sparse_fiction_bytes"] = strconv.FormatInt(sparse, 10)
	}
	if idx.Degraded != "" {
		ev["resolver"] = idx.Degraded
	}
	return modules.Item{Path: c.path, Size: core.Bytes(ts.UniqueAllocated), Evidence: ev}, true
}

// statWalk sums st_blocks*512 and finds the newest mtime in one pass.
func statWalk(path string) (int64, time.Time, error) {
	var blocks int64
	var newest time.Time
	err := filepath.WalkDir(path, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if p == path {
				return err
			}
			return nil
		}
		fi, err := d.Info()
		if err != nil {
			return nil
		}
		if mt := fi.ModTime(); mt.After(newest) {
			newest = mt
		}
		if st, ok := fi.Sys().(*syscall.Stat_t); ok && fi.Mode().IsRegular() {
			blocks += st.Blocks * 512
		}
		return nil
	})
	return blocks, newest, err
}

// Plan emits one delete per orphaned dir. Risk is MEDIUM: regenerable is
// not the word for an uninstalled app's data (DevKinsta sites, Docker
// volumes) — the human must want it gone.
func (m *Module) Plan(r modules.Report) []modules.Action {
	out := make([]modules.Action, 0, len(r.Items))
	for _, it := range r.Items {
		out = append(out, modules.Action{
			Module: "apps",
			Op:     "delete",
			Target: it.Path,
			Size:   it.Size,
			Risk:   modules.RiskMedium,
		})
	}
	return out
}

// Apply deletes one orphaned dir. The verdict is RE-ESTABLISHED at apply
// time against a fresh Index (an app reinstalled since Plan, or a dir
// written to since, is refused), the target must be exactly a candidate
// shape (a direct child of a library root, or a curated path), and freed
// bytes are statfs-measured.
func (m *Module) Apply(ctx context.Context, a modules.Action) (modules.Result, error) {
	res := modules.Result{Action: a}
	fail := func(err error) (modules.Result, error) {
		res.Err = err
		return res, err
	}
	if a.Op != "delete" {
		return fail(fmt.Errorf("apps: unsupported op %q", a.Op))
	}
	c, ok := m.candidateFor(a.Target)
	if !ok {
		return fail(fmt.Errorf("apps: %q is not a direct child of an app-data root nor a curated app folder — refusing", a.Target))
	}
	if c.keyedBy != "curated" {
		if err := m.safety.CanDelete(c.path); err != nil {
			return fail(err)
		}
	}
	if _, ok := m.inspect(c, m.index(ctx)); !ok {
		return fail(fmt.Errorf("apps: %q no longer orphaned/idle/large at apply time — refusing", a.Target))
	}
	freed, err := sizer.FreedByDelete(filepath.Dir(c.path), func() error {
		return os.RemoveAll(c.path)
	})
	if err != nil {
		return fail(err)
	}
	res.BytesFreed = core.Bytes(freed)
	return res, nil
}

// candidateFor maps a target back to its candidate shape, or refuses.
func (m *Module) candidateFor(target string) (candidate, bool) {
	abs, err := filepath.Abs(target)
	if err != nil {
		return candidate{}, false
	}
	clean := filepath.Clean(abs)
	for _, c := range m.curated {
		if filepath.Clean(c.Path) == clean {
			return candidate{path: clean, keyedBy: "curated", key: c.App}, true
		}
	}
	name := filepath.Base(clean)
	if excludedName(name) {
		return candidate{}, false
	}
	for _, root := range m.roots {
		if filepath.Dir(clean) == filepath.Clean(root) {
			keyed := "name"
			if strings.Contains(name, ".") {
				keyed = "bundle-id"
			}
			return candidate{path: clean, keyedBy: keyed, key: name}, true
		}
	}
	return candidate{}, false
}
