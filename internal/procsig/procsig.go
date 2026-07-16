// Package procsig is a data-driven registry of known orphaned-process
// classes and a module that finds and (with consent) terminates them.
//
// The founding incident: two headless Chromes from automation jobs finished
// days earlier were still running — pinning 305 open files, blocking asset
// relocation, eating RAM — and every hard-killed automation Chrome is a
// code_sign_clone leak SOURCE. Killing the orphan is the upstream half of
// the leak story: once its file handles drop, the leaked clone dirs go
// lsof-empty and the EXISTING leaks signatures sweep them on the next
// `noo-noo leaks clean` — composition by sequencing, not shared code.
//
// This is a SIBLING of internal/leaks, never an extension of it (charter
// D20): the leaks machinery is structurally path-shaped (globs, lstat,
// RemoveAll, CanDeleteLeakTarget) — all meaningless for a PID. Signatures
// here are DATA (ProcessSignature values); a new orphan class is a new
// registry entry, never new scanner code.
package procsig

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/FRIKKern/noo-noo/internal/modules"
)

// ProcessSignature is one named orphaned-process class. Pure data: the
// scanner interprets entries uniformly.
type ProcessSignature struct {
	// ID is the stable machine name, e.g. "headless-chrome-orphan".
	ID string
	// Title is the human one-liner shown in reports.
	Title string
	// CmdlineContains are substrings that must ALL appear in the process
	// command line for the signature to match.
	CmdlineContains []string
	// UserDataDirGlobs are absolute filepath.Match patterns. The process's
	// --user-data-dir value must lie AT or UNDER a path matching one of
	// them (the dir itself or any ancestor matches). This anchors the
	// signature to temp/agent-scratch profiles: a headless browser running
	// against a real user profile is somebody's session, never an orphan
	// candidate.
	UserDataDirGlobs []string
	// Risk is the risk level attached to terminate actions.
	Risk modules.RiskLevel
	// Workaround is evidence-cited advice that prevents the class at its
	// source; surfaced verbatim in every hit's Evidence.
	Workaround string
}

// Match reports whether proc matches the signature: every CmdlineContains
// substring present AND a --user-data-dir that lies under one of the globs.
// Returns the extracted user-data-dir on a match.
func (s ProcessSignature) Match(proc Process) (userDataDir string, ok bool) {
	for _, sub := range s.CmdlineContains {
		if !strings.Contains(proc.Command, sub) {
			return "", false
		}
	}
	dir, ok := UserDataDir(proc.Command)
	if !ok {
		return "", false
	}
	if !underAny(dir, s.UserDataDirGlobs) {
		return "", false
	}
	return dir, true
}

// DefaultSignatures is the shipped registry. Signature #1 is the proven
// incident shape: headless automation browsers whose profile lives in a
// temp/agent-scratch dir and whose parent job is gone (reparented to
// launchd, ppid 1).
func DefaultSignatures() []ProcessSignature {
	globs := []string{
		// Agent/automation scratch: the proven incident paths.
		"/private/tmp/*",
		"/tmp/*", // same tree, unresolved spelling as ps reports it
		"/private/var/folders/*/*/T/*",
		"/var/folders/*/*/T/*",
	}
	if home, err := os.UserHomeDir(); err == nil {
		globs = append(globs,
			filepath.Join(home, ".claude", "jobs", "*"),
			filepath.Join(home, ".claude", "tmp", "*"),
		)
	}
	return []ProcessSignature{
		{
			ID:    "headless-chrome-orphan",
			Title: "Orphaned headless browser (automation job gone, process still running)",
			CmdlineContains: []string{
				"--headless",
				"--user-data-dir=",
			},
			UserDataDirGlobs: globs,
			Risk:             modules.RiskLow,
			Workaround: "Automation harnesses that spawn headless browsers must kill them when " +
				"the job ends. Each hard-killed automation Chrome also leaks a code_sign_clone " +
				"dir — after terminating orphans, run 'noo-noo leaks clean' to sweep those.",
		},
	}
}

// Process is one row of the process table, as parsed from
// `ps -axo pid,ppid,uid,etime,command`.
type Process struct {
	PID     int
	PPID    int
	UID     int
	Etime   string // elapsed run time, ps ETIME format (e.g. "3-04:12:56")
	Command string // full command line, space-normalized
}

// ParsePS parses `ps -axo pid,ppid,uid,etime,command` output. Header and
// malformed rows are skipped, never fatal: one weird row must not blind the
// whole scan. Command is reassembled from whitespace-split fields, so runs
// of spaces inside arguments are normalized to one — fine for substring and
// token matching, which is all the scanner does.
func ParsePS(out string) []Process {
	var procs []Process
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) < 5 {
			continue
		}
		pid, err1 := strconv.Atoi(f[0])
		ppid, err2 := strconv.Atoi(f[1])
		uid, err3 := strconv.Atoi(f[2])
		if err1 != nil || err2 != nil || err3 != nil {
			continue // header or malformed row
		}
		procs = append(procs, Process{
			PID:     pid,
			PPID:    ppid,
			UID:     uid,
			Etime:   f[3],
			Command: strings.Join(f[4:], " "),
		})
	}
	return procs
}

// UserDataDir extracts the --user-data-dir= value from a command line.
// Token-based: a path containing spaces is not recoverable from ps output
// and simply fails to extract (no match — fail-safe).
func UserDataDir(cmdline string) (string, bool) {
	for _, tok := range strings.Fields(cmdline) {
		if v, ok := strings.CutPrefix(tok, "--user-data-dir="); ok && v != "" {
			return filepath.Clean(v), true
		}
	}
	return "", false
}

// underAny reports whether dir, or any ancestor of dir, matches one of the
// absolute globs — i.e. dir lies at or under a glob hit. The glob roots
// themselves (e.g. /private/tmp) never match their own pattern, so a
// browser profiled directly on the scratch ROOT's parent stays unmatched.
func underAny(dir string, globs []string) bool {
	p := filepath.Clean(dir)
	for {
		for _, g := range globs {
			if ok, err := filepath.Match(g, p); err == nil && ok {
				return true
			}
		}
		parent := filepath.Dir(p)
		if parent == p {
			return false
		}
		p = parent
	}
}
