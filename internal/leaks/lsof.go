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
	ctx, cancel := context.WithTimeout(ctx, lsofTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "lsof", "+D", dir)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	runErr := cmd.Run()

	rows := nonHeaderRows(stdout.String())
	if len(rows) > 0 {
		return true, fmt.Sprintf("lsof +D: %d open file(s) under %s, e.g. %q", len(rows), dir, rows[0])
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return true, fmt.Sprintf("lsof +D timed out after %s — treating as LIVE (fail-safe)", lsofTimeout)
	}
	if runErr != nil {
		var ee *exec.ExitError
		// lsof exits 1 when it simply finds nothing open — with clean
		// stderr and zero rows that IS the stale signal, not a failure.
		if errors.As(runErr, &ee) && ee.ExitCode() == 1 && stderr.Len() == 0 {
			return false, "lsof +D: no process holds any file open under " + dir
		}
		return true, fmt.Sprintf("lsof failed (%v; stderr %q) — treating as LIVE (fail-safe)",
			runErr, strings.TrimSpace(stderr.String()))
	}
	return false, "lsof +D: no process holds any file open under " + dir
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
