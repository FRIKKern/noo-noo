// Package leaks is a data-driven registry of known disk-leak classes and a
// cleanup module that finds, sizes, and (when provably stale) removes them.
//
// The founding incident is the doctrine: 65 leaked Chrome code_sign_clone
// directories du-reported 129G, but deleting 64 of them freed only ~3G,
// because they were APFS copy-on-write clones sharing blocks. Every hit is
// therefore sized twice — sizer.Blocks (the du-equivalent st_blocks sum) AND
// sizer.UniqueAllocated (physical-extent union, clone- and sparse-aware) —
// and the Evidence carries both so the du-fiction delta is visible.
//
// Signatures are DATA (Signature values), never scanner code: adding a new
// leak class is a new registry entry. Staleness is liveness-checked with
// lsof — mtime is PROVEN unsafe for the Chrome class (a 6-day-old clone was
// held open by 7 live Chrome processes) — and re-checked at Apply time so a
// hit that went live between Plan and Apply is refused (TOCTOU guard).
package leaks

import (
	"time"

	"github.com/FRIKKern/noo-noo/internal/modules"
)

// Staleness selects how a matched hit is classified stale (deletable) vs
// live (report-only, never deletable).
type Staleness int

const (
	// StaleWhenLsofEmpty marks a hit stale iff no process holds any file
	// under it open. Age plays no part: mtime is proven unsafe for classes
	// like Chrome code-sign clones, which stay held open for days.
	StaleWhenLsofEmpty Staleness = iota
	// StaleWhenAgedAndLsofEmpty marks a hit stale iff the NEWEST mtime
	// anywhere in its tree is older than the signature's MinAge AND no
	// process holds a file under it open. The age gate protects live agent
	// sessions whose scratch files exist but are not held open — lsof alone
	// is insufficient for this class.
	StaleWhenAgedAndLsofEmpty
)

func (s Staleness) String() string {
	switch s {
	case StaleWhenLsofEmpty:
		return "lsof-empty"
	case StaleWhenAgedAndLsofEmpty:
		return "aged+lsof-empty"
	default:
		return "unknown"
	}
}

// Signature is one named leak class. Signatures are pure data: the scanner
// interprets them uniformly, so a new leak class is a new entry here (or a
// caller-supplied one), never new scanner code.
type Signature struct {
	// ID is the stable machine name, e.g. "chrome-code-sign-clone".
	ID string
	// Title is the human one-liner shown in reports.
	Title string
	// Globs are absolute filepath.Match patterns. A matched path is a leak
	// HIT (a leaf the signature owns); parents of a pattern never match and
	// are never deletable.
	Globs []string
	// Staleness picks the deletability check.
	Staleness Staleness
	// MinAge is the age gate for StaleWhenAgedAndLsofEmpty (ignored
	// otherwise): the newest mtime in the tree must be older than this.
	MinAge time.Duration
	// Risk is the risk level attached to delete actions for stale hits.
	Risk modules.RiskLevel
	// Workaround is evidence-cited advice that prevents the leak class at
	// its source; it is surfaced verbatim in every hit's Evidence.
	Workaround string
}

// DefaultSignatures is the shipped registry: the two live refill classes
// proven on the founding machine (charter D3).
func DefaultSignatures() []Signature {
	return []Signature{
		{
			ID:    "chrome-code-sign-clone",
			Title: "Chrome leaked code-sign clone (APFS clone — du over-reports)",
			Globs: []string{
				"/private/var/folders/*/*/X/*.code_sign_clone/code_sign_clone.??????",
			},
			// mtime is PROVEN unsafe here: a 6-day-old clone was held open
			// by 7 live Chrome processes as txt segments. lsof-empty is the
			// ONLY staleness signal for this class.
			Staleness: StaleWhenLsofEmpty,
			Risk:      modules.RiskLow,
			Workaround: "Chrome leaks one code-sign clone per crash/force-kill. " +
				"Quit and relaunch Chrome cleanly, or launch with " +
				"--disable-features=MacAppCodeSignClone to stop the leak class at its source.",
		},
		{
			ID:    "private-tmp-agent-scratch",
			Title: "Stale agent scratch under /private/tmp (never OS-cleaned)",
			Globs: []string{
				"/private/tmp/claude-*",
				"/private/tmp/*-gocache*",
				"/private/tmp/*gocache.*",
			},
			// Scratch files are rarely held open even while a session is
			// live, so lsof alone is insufficient: the age gate (newest
			// mtime anywhere in the tree) protects live agent sessions.
			Staleness: StaleWhenAgedAndLsofEmpty,
			MinAge:    7 * 24 * time.Hour,
			Risk:      modules.RiskLow,
			Workaround: "Agent sessions recreate scratch dirs on demand; nothing regenerable " +
				"is lost. macOS never cleans /private/tmp on this machine — without noo-noo " +
				"this class only grows.",
		},
	}
}
