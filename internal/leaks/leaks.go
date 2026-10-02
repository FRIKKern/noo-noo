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
	// StaleWhenFamilyAgedAndLsofEmpty is the sibling-family rule (see
	// FamilyRule): a hit is one FAMILY of mktemp-style siblings under a
	// root, stale iff the family has at least MinMembers members, its
	// newest mtime across every member is older than MinAge, and no
	// member has a file held open.
	StaleWhenFamilyAgedAndLsofEmpty
	// StaleWhenSessionDead is the agent-session rule (see SessionRule): a
	// session dir named by a UUID is stale iff the session is provably
	// dead (no process carries its id, transcript quiet), its newest mtime
	// is older than DeadAfter, and nothing under it is held open. Paths
	// matched by the same signature whose basename is NOT a UUID fall back
	// to StaleWhenAgedAndLsofEmpty with the signature's MinAge.
	StaleWhenSessionDead
)

func (s Staleness) String() string {
	switch s {
	case StaleWhenLsofEmpty:
		return "lsof-empty"
	case StaleWhenAgedAndLsofEmpty:
		return "aged+lsof-empty"
	case StaleWhenFamilyAgedAndLsofEmpty:
		return "family-aged+lsof-empty"
	case StaleWhenSessionDead:
		return "session-liveness"
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
	// Alias picks the hardlink-alias handling when the lsof probe reports
	// LIVE (see AliasRule in causality.go). Zero value AliasNone keeps the
	// probe's verdict untouched.
	Alias AliasRule
	// Risk is the risk level attached to delete actions for stale hits.
	Risk modules.RiskLevel
	// Workaround is evidence-cited advice that prevents the leak class at
	// its source; it is surfaced verbatim in every hit's Evidence.
	Workaround string
	// Family, set with StaleWhenFamilyAgedAndLsofEmpty, switches discovery
	// from Globs to sibling-family grouping under Family.Roots. Globs then
	// only name the deletable LEAF shape (root/*) for the safety predicate.
	Family *FamilyRule
	// Session, set with StaleWhenSessionDead, names how a glob-matched
	// session dir is liveness-checked and which subdir carries per-entry
	// scratch that can go stale inside a live session.
	Session *SessionRule
}

// FamilyRule describes mktemp-style sibling families: direct children of
// a root whose names share a prefix once a trailing random suffix
// ([-_.]?[A-Za-z0-9]{6,}, or Go's MkdirTemp decimal run) is stripped. A
// single such dir is nobody's business; a FAMILY of them (bd-two-installs-
// eBU8d7 x 130, days old, nothing open) is a harness that forgot to clean
// up — 16,603 of them held 43 GB on the founding machine.
type FamilyRule struct {
	// Roots are filepath.Glob patterns naming the directories whose direct
	// children are grouped, e.g. "/private/var/folders/*/*/T" ($TMPDIR).
	Roots []string
	// MinMembers is the smallest sibling count that counts as a family.
	MinMembers int
	// ExcludePrefixes drops children whose NAME starts with one of these
	// before grouping — com.apple.launchd.* socket dirs look exactly like a
	// family and must never be touched.
	ExcludePrefixes []string
}

// SessionRule describes an agent session dir whose basename is the session
// UUID, so liveness is CHECKABLE instead of guessed from mtime: a session is
// alive when a process carries `--session-id <uuid>` or its transcript
// (~/.claude/projects/*/<uuid>.jsonl) was written within TranscriptWindow.
type SessionRule struct {
	// ScratchDir is the subdir (relative to the session dir) whose direct
	// children are offered per-entry while the session is alive.
	ScratchDir string
	// DeadAfter is the age gate for a DEAD session: its newest mtime must
	// be older than this before the whole dir is stale.
	DeadAfter time.Duration
	// EntryIdle is the per-entry age gate inside a LIVE session: a direct
	// child of ScratchDir whose newest mtime is older than this (and that
	// nothing holds open) is stale on its own.
	EntryIdle time.Duration
	// TranscriptWindow is how recently the transcript must have been
	// written to count as proof of life.
	TranscriptWindow time.Duration
}

// scratchGlobs derives the per-entry leaf globs for a session signature:
// <session glob>/<ScratchDir>/*. Entries are discovered by enumeration (only
// inside live sessions), never by glob expansion; these globs exist so the
// safety predicate and signatureFor recognise the entry leaf shape.
func (s Signature) scratchGlobs() []string {
	if s.Session == nil || s.Session.ScratchDir == "" {
		return nil
	}
	out := make([]string, 0, len(s.Globs))
	for _, g := range s.Globs {
		out = append(out, g+"/"+s.Session.ScratchDir+"/*")
	}
	return out
}

// allGlobs is every leaf shape this signature may delete: the discovery
// globs plus the derived scratch-entry globs.
func (s Signature) allGlobs() []string {
	return append(append([]string{}, s.Globs...), s.scratchGlobs()...)
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
			// mtime AGE alone is PROVEN unsafe here: a 6-day-old clone was
			// held open by 7 live Chrome processes as txt segments. lsof-empty
			// is the primary staleness signal — but lsof matches by inode,
			// and Chrome hardlinks ONE launcher binary into every clone, so
			// one live Chrome made all 48 leaked clones probe LIVE (73 GB
			// undeletable, disk to zero — the second founding incident).
			// Launch causality arbitrates: txt-only matches by processes
			// younger than the whole tree are inode aliases via the newer
			// active clone, and the tree is provably stale.
			Staleness: StaleWhenLsofEmpty,
			Alias:     AliasLaunchCausality,
			Risk:      modules.RiskLow,
			Workaround: "Chrome leaks one code-sign clone per crash/force-kill. " +
				"Quit and relaunch Chrome cleanly, or launch with " +
				"--disable-features=MacAppCodeSignClone to stop the leak class at its source.",
		},
		{
			ID:    "private-tmp-agent-scratch",
			Title: "Stale agent scratch under /private/tmp (never OS-cleaned)",
			Globs: []string{
				// Per-session depth, not the claude-* root: the root stays
				// warm for months while individual session dirs inside go
				// cold — cleaning at the root would either never fire (the
				// age gate sees the newest session) or nuke live sessions.
				"/private/tmp/claude-*/*/*",
				"/private/tmp/*-gocache*",
				"/private/tmp/*gocache.*",
				// The redirected-TMPDIR convention: machines that point dev
				// churn at a big external disk use <volume>/dev-caches/tmp
				// (TMPDIR/GOCACHE/npm redirects). Space is cheap there, but
				// unbounded is unbounded — the same age+lsof gate applies.
				"/Volumes/*/dev-caches/tmp/*",
			},
			// Session dirs (basename = session UUID) are liveness-checked:
			// the 7-day age gate alone protected a 23 GB scratchpad for a
			// session that was idle but not dead, and would have waited a
			// week on a dead one. Dead session: whole dir stale after 1h.
			// Live session: direct scratchpad entries idle 24h are offered
			// on their own (22 GB freed exactly that way by hand). Paths
			// without a UUID basename (gocache dirs, redirected tmp) keep
			// the age gate: scratch files are rarely held open even while
			// in use, so lsof alone is insufficient there.
			Staleness: StaleWhenSessionDead,
			Session: &SessionRule{
				ScratchDir:       "scratchpad",
				DeadAfter:        time.Hour,
				EntryIdle:        24 * time.Hour,
				TranscriptWindow: 30 * time.Minute,
			},
			MinAge: 7 * 24 * time.Hour,
			Risk:   modules.RiskLow,
			Workaround: "Agent sessions recreate scratch dirs on demand; nothing regenerable " +
				"is lost. macOS never cleans /private/tmp on this machine — without noo-noo " +
				"this class only grows.",
		},
		{
			ID:    "darwin-t-agent-trial",
			Title: "Abandoned agent trial workdirs under $TMPDIR (T/)",
			Globs: []string{
				"/private/var/folders/*/*/T/grip-trial-*",
				"/private/var/folders/*/*/T/emit-fence-*",
				"/Volumes/*/dev-caches/tmp/grip-trial-*",
				"/Volumes/*/dev-caches/tmp/emit-fence-*",
			},
			// Trial workdirs are disposable by contract (mktemp naming) but
			// a run can legitimately span hours; twelve idle hours plus
			// nothing held open proves the run is over. The age gate reads
			// the NEWEST mtime in the tree, so a long-running trial stays
			// protected while it writes. Sizing the gate: 30 grip-trial
			// dirs held 16 GB over days, but one evening minted 3,746
			// emit-fence dirs (37 GB) — at that burst rate a 48h gate lets
			// two bursts stack, so half a day is the ceiling this class
			// can afford.
			Staleness: StaleWhenAgedAndLsofEmpty,
			MinAge:    12 * time.Hour,
			Risk:      modules.RiskLow,
			Workaround: "Trial harnesses should remove their workdir on exit (trap cleanup), " +
				"or create it under a session scratchpad that already has an owner.",
		},
		{
			ID:    "temp-family",
			Title: "Abandoned mktemp families under $TMPDIR and /private/tmp",
			// Globs name the deletable LEAF shape only (a direct child of a
			// root); discovery is family grouping, never glob expansion.
			Globs: []string{
				"/private/var/folders/*/*/T/*",
				"/private/tmp/*",
			},
			Staleness: StaleWhenFamilyAgedAndLsofEmpty,
			Family: &FamilyRule{
				Roots:           []string{"/private/var/folders/*/*/T", "/private/tmp"},
				MinMembers:      5,
				ExcludePrefixes: []string{"com.apple."},
			},
			// Same gate as the trial class, for the same reason: a harness
			// run spans hours, not days, and the family's NEWEST mtime
			// protects a harness that is still minting siblings.
			MinAge: 12 * time.Hour,
			Risk:   modules.RiskLow,
			Workaround: "os.MkdirTemp/mktemp callers must remove their dir on exit (defer os.RemoveAll / trap). " +
				"macOS only sweeps $TMPDIR entries untouched for 3 days, and never sweeps /private/tmp.",
		},
	}
}
