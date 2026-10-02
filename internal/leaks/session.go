package leaks

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

// SessionProber reports whether the agent session named by uuid is alive.
// Fail-safe: when liveness cannot be established, alive=true (protect).
type SessionProber func(ctx context.Context, uuid string, transcriptWindow time.Duration) (alive bool, proof string)

var uuidRe = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// isSessionUUID reports whether s has the canonical 8-4-4-4-12 UUID shape.
func isSessionUUID(s string) bool { return uuidRe.MatchString(s) }

// SessionProbeFor builds the default SessionProber: a session is alive when
// any process's args carry `--session-id <uuid>` (ps -axo args=) OR its
// transcript home/.claude/projects/*/<uuid>.jsonl was modified within
// transcriptWindow. A ps failure means ALIVE (fail-safe); a missing
// transcript is simply no proof of life.
func SessionProbeFor(home string, now func() time.Time) SessionProber {
	return func(ctx context.Context, uuid string, window time.Duration) (bool, string) {
		ctx, cancel := context.WithTimeout(ctx, lsofTimeout)
		defer cancel()
		out, err := exec.CommandContext(ctx, "ps", "-axo", "args=").Output()
		if err != nil {
			return true, "ps -axo args= failed (" + err.Error() + ") — treating session as ALIVE (fail-safe)"
		}
		if argsCarrySession(string(out), uuid) {
			return true, "a running process carries --session-id " + uuid
		}
		mt, found := transcriptMtime(home, uuid)
		if found {
			if age := now().Sub(mt); age < window {
				return true, fmt.Sprintf("transcript %s.jsonl written %s ago < %s", uuid, age.Round(time.Second), window)
			}
			return false, fmt.Sprintf("no process carries --session-id %s; transcript last written %s ago ≥ %s",
				uuid, now().Sub(mt).Round(time.Second), window)
		}
		return false, fmt.Sprintf("no process carries --session-id %s; no transcript under %s", uuid, filepath.Join(home, ".claude", "projects"))
	}
}

// argsCarrySession scans ps args output for `--session-id <uuid>` or
// `--session-id=<uuid>` (case-insensitive on the uuid).
func argsCarrySession(psOut, uuid string) bool {
	if !strings.Contains(strings.ToLower(psOut), "--session-id") {
		return false
	}
	re := regexp.MustCompile(`(?i)--session-id[ =]+` + regexp.QuoteMeta(uuid) + `(?:[^0-9a-fA-F-]|$)`)
	return re.MatchString(psOut)
}

// transcriptMtime returns the newest mtime among home/.claude/projects/*/
// <uuid>.jsonl transcripts (a session can appear under more than one
// project dir after a cwd change).
func transcriptMtime(home, uuid string) (time.Time, bool) {
	matches, err := filepath.Glob(filepath.Join(home, ".claude", "projects", "*", uuid+".jsonl"))
	if err != nil {
		return time.Time{}, false
	}
	var newest time.Time
	var found bool
	for _, p := range matches {
		fi, err := os.Stat(p)
		if err != nil {
			continue
		}
		found = true
		if mt := fi.ModTime(); mt.After(newest) {
			newest = mt
		}
	}
	return newest, found
}
