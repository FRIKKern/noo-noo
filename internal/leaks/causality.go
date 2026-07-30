package leaks

import (
	"context"
	"fmt"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"
)

// AliasRule selects how a signature handles the lsof hardlink-alias trap:
// lsof matches open files by device+inode, so one live inode hard-linked
// into N leaked trees makes ALL of them probe LIVE — and lsof's printed
// NAME cannot arbitrate, because macOS caches one name per vnode and
// resets it on every lookup (a `+D` walk of tree A renames the vnode to
// A's alias; the next probe sees it there). The second founding incident:
// Chrome hardlinks its launcher binary into every code-sign clone, five
// live processes made 48 leaked clones (73 GB) permanently undeletable,
// and the disk ran to zero with 26 open suggestions filed.
type AliasRule int

const (
	// AliasNone: any lsof +D match is LIVE, full stop. The safe default
	// for classes where a real fd into the tree is possible.
	AliasNone AliasRule = iota
	// AliasLaunchCausality: a +D match may be DOWNGRADED to stale when
	// launch identity proves no running process was BORN WITH this tree.
	// The class contract: such a tree is created by one launch, exec'd
	// through by that launch's process family, and never opened as data
	// afterwards — so the only legitimizing witness is a matched process
	// whose start time falls within minutes of the tree's birthtime (the
	// instance the tree was created FOR; its later-spawned helpers ride
	// on it). If every match is a txt segment (never a plain fd) and no
	// matched process was born with the tree, the matches are inode
	// aliases via a NEWER sibling's hardlink, and the tree is stale.
	// Timing subtleties that make weaker rules wrong: an 11-day Chrome
	// outlives clones born mid-flight (so "tree older than all holders"
	// under-collects), and its yesterday-spawned helpers postdate most
	// trees (so "holder existed before tree" over-collects).
	AliasLaunchCausality
)

func (a AliasRule) String() string {
	switch a {
	case AliasNone:
		return "none"
	case AliasLaunchCausality:
		return "launch-causality"
	default:
		return "unknown"
	}
}

// causalMargin is the birth-proximity window: a matched process legitimizes
// a tree when its start time is within this much of the tree's birthtime
// (clone creation and instance exec happen within seconds of each other; the
// margin absorbs slow launches, clock rounding, and `ps` etime granularity).
// It doubles as the youth gate: a tree younger than the margin is never
// judged — its creating launch may still be mid-flight.
const causalMargin = 10 * time.Minute

// lsofRow is the (pid, fd-type) pair parsed from one `lsof +D` data row.
type lsofRow struct {
	pid string
	fd  string // "txt", "cwd", "3r", "12u", …
}

// parseLsofRows extracts (pid, fd) from stage-1 rows. Rows that don't parse
// yield a zero pid, which causalVerdict treats as unprovable (LIVE).
func parseLsofRows(rows []string) []lsofRow {
	out := make([]lsofRow, 0, len(rows))
	for _, r := range rows {
		f := strings.Fields(r)
		if len(f) < 4 {
			out = append(out, lsofRow{})
			continue
		}
		if _, err := strconv.Atoi(f[1]); err != nil {
			out = append(out, lsofRow{})
			continue
		}
		out = append(out, lsofRow{pid: f[1], fd: f[3]})
	}
	return out
}

// causalVerdict decides stale-vs-live from parsed rows, a per-PID start-time
// oracle, the tree's birthtime, and the current time. Pure logic — fully
// unit-testable. stale=true requires EVERY row to be a txt mapping owned by
// a parseable PID, the tree to be older than causalMargin, and NO matched
// PID to have started within causalMargin of the tree's birth (no running
// instance was born with this tree). Anything unprovable is LIVE.
func causalVerdict(rows []lsofRow, start func(pid string) (time.Time, error), birth, now time.Time) (stale bool, proof string) {
	if len(rows) == 0 {
		return true, "causality: no rows to arbitrate (tree went quiet)"
	}
	if now.Sub(birth) < causalMargin {
		return false, fmt.Sprintf("causality: tree born %s is younger than %s — its launch may be mid-flight, LIVE",
			birth.Format(time.RFC3339), causalMargin)
	}
	starts := map[string]time.Time{}
	for _, r := range rows {
		if r.pid == "" {
			return false, "causality: unparseable lsof row — treating as LIVE (fail-safe)"
		}
		if r.fd != "txt" {
			return false, fmt.Sprintf("causality: PID %s holds a non-txt handle (%s) — a real fd, tree is LIVE", r.pid, r.fd)
		}
		if _, ok := starts[r.pid]; ok {
			continue
		}
		t, err := start(r.pid)
		if err != nil {
			return false, fmt.Sprintf("causality: start time of PID %s unprovable (%v) — treating as LIVE (fail-safe)", r.pid, err)
		}
		starts[r.pid] = t
	}
	for pid, t := range starts {
		if d := t.Sub(birth); d >= -causalMargin && d <= causalMargin {
			return false, fmt.Sprintf("causality: PID %s started %s, within %s of tree birth %s — born with this tree, LIVE",
				pid, t.Format(time.RFC3339), causalMargin, birth.Format(time.RFC3339))
		}
	}
	pids := make([]string, 0, len(starts))
	for p := range starts {
		pids = append(pids, p)
	}
	sort.Strings(pids)
	return true, fmt.Sprintf(
		"causality: all %d match(es) are txt mappings, and no matched PID (%s) started within %s of tree birth %s — creator is gone, matches are inode aliases via a newer sibling, tree is stale",
		len(rows), strings.Join(pids, ","), causalMargin, birth.Format(time.RFC3339))
}

// causalCheck is the exec-backed wrapper classify() calls for signatures
// with AliasLaunchCausality after the plain probe said LIVE. It re-collects
// the +D rows itself (the Prober interface hides them), reads the tree
// root's birthtime, and consults `ps` for process start times.
func causalCheck(ctx context.Context, path string) (stale bool, proof string) {
	rows, failProof, ok := lsofTreeRows(ctx, path)
	if !ok {
		return false, failProof
	}
	birth, err := birthTime(path)
	if err != nil {
		return false, "causality: tree birthtime unprovable (" + err.Error() + ") — treating as LIVE (fail-safe)"
	}
	now := time.Now() // taken before ps runs: biases start times EARLIER = conservative
	return causalVerdict(parseLsofRows(rows), func(pid string) (time.Time, error) {
		return psStartTime(ctx, pid, now)
	}, birth, now)
}

// psStartTime derives a process's start time from `ps -o etime=` (elapsed
// time), which is locale-independent, minus-side rounded (whole seconds),
// and available for any visible PID. now must be captured BEFORE ps runs so
// the derived start errs earlier, never later.
func psStartTime(ctx context.Context, pid string, now time.Time) (time.Time, error) {
	ctx, cancel := context.WithTimeout(ctx, lsofTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, "ps", "-o", "etime=", "-p", pid).Output()
	if err != nil {
		return time.Time{}, fmt.Errorf("ps -p %s: %w", pid, err)
	}
	d, err := parseEtime(strings.TrimSpace(string(out)))
	if err != nil {
		return time.Time{}, fmt.Errorf("ps -p %s etime: %w", pid, err)
	}
	return now.Add(-d), nil
}

// parseEtime parses ps's ETIME format: [[dd-]hh:]mm:ss.
func parseEtime(s string) (time.Duration, error) {
	if s == "" {
		return 0, fmt.Errorf("empty etime")
	}
	var days int64
	if i := strings.IndexByte(s, '-'); i >= 0 {
		n, err := strconv.ParseInt(s[:i], 10, 64)
		if err != nil {
			return 0, fmt.Errorf("bad days in etime %q", s)
		}
		days, s = n, s[i+1:]
	}
	parts := strings.Split(s, ":")
	if len(parts) < 2 || len(parts) > 3 {
		return 0, fmt.Errorf("bad etime %q", s)
	}
	var total int64
	for _, p := range parts {
		n, err := strconv.ParseInt(p, 10, 64)
		if err != nil {
			return 0, fmt.Errorf("bad etime %q", s)
		}
		total = total*60 + n
	}
	return time.Duration(total)*time.Second + time.Duration(days)*24*time.Hour, nil
}
