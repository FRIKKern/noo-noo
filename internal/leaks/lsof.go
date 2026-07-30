package leaks

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// Prober reports whether any process currently holds a file open under dir.
// proof is the human-readable evidence string surfaced in reports and audit
// refusals. Probers must FAIL SAFE: when liveness cannot be established, the
// answer is live=true (never deletable), not a guess.
//
// KNOWN LIMIT: lsof matches by device+inode, so a file HARD-LINKED into dir
// but only held open via a path elsewhere still probes LIVE here. Worse, the
// printed NAME cannot arbitrate — macOS caches ONE name per vnode and
// resets it on lookup, so both `+D` walks and `-p` output report whichever
// alias was touched last. Signatures whose lifecycle supports it can opt
// into the launch-causality downgrade (AliasLaunchCausality in
// causality.go) instead; the probe itself never guesses.
type Prober func(ctx context.Context, dir string) (live bool, proof string)

// lsofTimeout bounds one probe. lsof +D walks the tree and every process's
// fd table; on a huge tree that can stall — and a stalled probe must degrade
// to LIVE (fail-safe), not hang the scan.
const lsofTimeout = 30 * time.Second

// LsofProbe is the default Prober: exec `lsof +D dir` and count non-header
// rows. Zero rows = stale; any rows, or any failure to prove emptiness
// (missing binary, timeout, stderr noise, unexpected exit) = LIVE.
func LsofProbe(ctx context.Context, dir string) (live bool, proof string) {
	if _, err := exec.LookPath("lsof"); err != nil {
		return true, "lsof unavailable — treating as LIVE (fail-safe): " + err.Error()
	}
	rows, failProof, ok := lsofTreeRows(ctx, dir)
	if !ok {
		return true, failProof
	}
	if len(rows) == 0 {
		return false, "lsof +D: no process holds any file open under " + dir
	}
	return true, fmt.Sprintf("lsof +D: %d open file(s) under %s, e.g. %q", len(rows), dir, rows[0])
}

// lsofTreeRows runs `lsof +D dir` and returns the non-header data rows.
// ok=false means emptiness could not be proven and the caller must classify
// LIVE with the returned proof.
func lsofTreeRows(ctx context.Context, dir string) (rows []string, failProof string, ok bool) {
	ctx, cancel := context.WithTimeout(ctx, lsofTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "lsof", "+D", dir)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	runErr := cmd.Run()

	rows = nonHeaderRows(stdout.String())
	if len(rows) > 0 {
		return rows, "", true
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return nil, fmt.Sprintf("lsof +D timed out after %s — treating as LIVE (fail-safe)", lsofTimeout), false
	}
	if runErr != nil {
		var ee *exec.ExitError
		// lsof exits 1 when it simply finds nothing open — with clean
		// stderr and zero rows that IS the stale signal, not a failure.
		if errors.As(runErr, &ee) && ee.ExitCode() == 1 && stderr.Len() == 0 {
			return nil, "", true
		}
		return nil, fmt.Sprintf("lsof failed (%v; stderr %q) — treating as LIVE (fail-safe)",
			runErr, strings.TrimSpace(stderr.String())), false
	}
	return nil, "", true
}

// nonHeaderRows splits lsof stdout into data rows, dropping the COMMAND
// header line and blanks.
func nonHeaderRows(out string) []string {
	var rows []string
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "COMMAND") {
			continue
		}
		rows = append(rows, line)
	}
	return rows
}
