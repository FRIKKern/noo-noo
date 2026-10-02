package orphanapps

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// App is one installed application bundle. BundleID may be empty when the
// app was found by directory listing only (Spotlight knew nothing about it).
type App struct {
	Path     string
	BundleID string
}

// Index answers "which installed app owns a data dir named X?" for X a
// bundle id (com.docker.docker), an app name (Local), or a loose name
// (Docker Desktop, Google). It is built ONCE per scan from one batched
// mdfind call plus directory listings of the application folders, so a run
// over a few hundred candidate dirs costs no further execs.
type Index struct {
	byID    map[string]string // lower bundle id -> app path
	byToken map[string]string // lower name/token -> app path
	// Degraded is non-empty when the bundle-id source failed and the index
	// rests on name matching alone; surfaced in evidence so a verdict never
	// hides that it was made half-blind.
	Degraded string
}

// IndexBuilder produces the Index a scan judges against. Injectable so tests
// never touch Spotlight or /Applications.
type IndexBuilder func(ctx context.Context) *Index

// NewIndex builds an Index from a known app list (tests, or any caller with
// its own inventory).
func NewIndex(apps []App) *Index {
	idx := &Index{byID: map[string]string{}, byToken: map[string]string{}}
	for _, a := range apps {
		idx.add(a)
	}
	return idx
}

var tokenSplit = regexp.MustCompile(`[ \-_.]+`)

// genericTokens never identify an app on their own.
var genericTokens = map[string]bool{
	"the": true, "app": true, "for": true, "and": true, "mac": true, "macos": true, "pro": true,
	"com": true, "org": true, "net": true, "io": true, "co": true, "dev": true, "desktop": true,
	"helper": true, "inc": true, "ltd": true, "llc": true, "beta": true, "lite": true,
}

func (idx *Index) add(a App) {
	if a.BundleID != "" {
		idx.byID[strings.ToLower(a.BundleID)] = a.Path
		for _, tok := range tokenSplit.Split(strings.ToLower(a.BundleID), -1) {
			idx.addToken(tok, a.Path)
		}
	}
	name := strings.TrimSuffix(filepath.Base(a.Path), ".app")
	lower := strings.ToLower(name)
	idx.byToken[lower] = a.Path
	for _, tok := range tokenSplit.Split(lower, -1) {
		idx.addToken(tok, a.Path)
	}
}

func (idx *Index) addToken(tok, path string) {
	if len(tok) < 3 || genericTokens[tok] {
		return
	}
	if _, dup := idx.byToken[tok]; !dup {
		idx.byToken[tok] = path
	}
}

// Lookup resolves a data-dir name to an installed app. Order: exact bundle
// id; exact name; the dir's first non-generic token against every app's
// name tokens and bundle-id components ("Docker Desktop" -> docker ->
// Docker.app / com.docker.docker; "Code" -> Visual Studio Code.app).
func (idx *Index) Lookup(name string) (string, bool) {
	lower := strings.ToLower(strings.TrimSpace(name))
	if lower == "" {
		return "", false
	}
	if p, ok := idx.byID[lower]; ok {
		return p, true
	}
	if p, ok := idx.byToken[lower]; ok {
		return p, true
	}
	for _, tok := range tokenSplit.Split(lower, -1) {
		if len(tok) < 3 || genericTokens[tok] {
			continue
		}
		if p, ok := idx.byToken[tok]; ok {
			return p, true
		}
		break // only the FIRST meaningful token identifies the vendor/app
	}
	return "", false
}

// appFolders are listed for name matching regardless of Spotlight health.
var appFolders = []string{
	"/Applications",
	"/Applications/Utilities",
	"/System/Applications",
	"/System/Applications/Utilities",
}

const mdfindTimeout = 20 * time.Second

// BuildIndex is the default IndexBuilder: one `mdfind -attr
// kMDItemCFBundleIdentifier` over every application bundle Spotlight knows,
// unioned with a listing of the application folders (and ~/Applications).
func BuildIndex(ctx context.Context) *Index {
	idx := NewIndex(nil)
	apps, err := mdfindApps(ctx)
	if err != nil {
		idx.Degraded = "mdfind failed (" + err.Error() + ") — bundle ids unknown, name matching only"
	}
	for _, a := range apps {
		idx.add(a)
	}
	folders := append([]string{}, appFolders...)
	if home, err := os.UserHomeDir(); err == nil {
		folders = append(folders, filepath.Join(home, "Applications"))
	}
	for _, dir := range folders {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if strings.HasSuffix(e.Name(), ".app") {
				idx.add(App{Path: filepath.Join(dir, e.Name())})
			}
		}
	}
	return idx
}

// mdfindApps runs the batched Spotlight query and parses
// "<path>   kMDItemCFBundleIdentifier = <id>" rows.
func mdfindApps(ctx context.Context) ([]App, error) {
	if _, err := exec.LookPath("mdfind"); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, mdfindTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, "mdfind", "-attr", "kMDItemCFBundleIdentifier",
		`kMDItemContentType == "com.apple.application-bundle"`).Output()
	if err != nil {
		return nil, fmt.Errorf("mdfind: %w", err)
	}
	apps := parseMdfind(string(out))
	if len(apps) == 0 {
		return nil, fmt.Errorf("mdfind returned no application bundles (Spotlight indexing off?)")
	}
	return apps, nil
}

const mdfindSep = "kMDItemCFBundleIdentifier = "

func parseMdfind(out string) []App {
	var apps []App
	for _, line := range strings.Split(out, "\n") {
		i := strings.Index(line, mdfindSep)
		if i < 0 {
			continue
		}
		path := strings.TrimSpace(line[:i])
		id := strings.TrimSpace(line[i+len(mdfindSep):])
		if id == "(null)" {
			id = ""
		}
		if path == "" {
			continue
		}
		apps = append(apps, App{Path: path, BundleID: id})
	}
	return apps
}
