package leaks

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// OpenPathsLister returns every path currently held open by any process
// visible to the caller, in ONE call. The family scan needs a liveness
// answer for tens of thousands of siblings; one `lsof +D` per member would
// take half an hour, one global listing takes a fraction of a second.
//
// ok=false means the listing could not be trusted and callers must classify
// LIVE with failProof. KNOWN LIMIT: names come from the vnode name cache, so
// a file opened and then renamed may list under its old name — which is why
// the family deleter re-probes each member with the inode-accurate
// `lsof +D` at delete time (memberRefusal); this listing only ever decides
// what is NOT offered.
type OpenPathsLister func(ctx context.Context) (paths []string, failProof string, ok bool)

// LsofOpenPaths is the default OpenPathsLister: `lsof -n -P -F n`, keeping
// the n (name) field rows.
func LsofOpenPaths(ctx context.Context) ([]string, string, bool) {
	if _, err := exec.LookPath("lsof"); err != nil {
		return nil, "lsof unavailable — treating as LIVE (fail-safe): " + err.Error(), false
	}
	ctx, cancel := context.WithTimeout(ctx, lsofTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "lsof", "-n", "-P", "-F", "n")
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	runErr := cmd.Run()
	paths := parseOpenPaths(stdout.String())
	if len(paths) > 0 {
		return paths, "", true
	}
	if ctx.Err() != nil {
		return nil, fmt.Sprintf("lsof -F n timed out after %s — treating as LIVE (fail-safe)", lsofTimeout), false
	}
	if runErr != nil {
		var ee *exec.ExitError
		if errors.As(runErr, &ee) && ee.ExitCode() == 1 && stderr.Len() == 0 {
			return nil, "", true
		}
		return nil, fmt.Sprintf("lsof -F n failed (%v; stderr %q) — treating as LIVE (fail-safe)",
			runErr, strings.TrimSpace(stderr.String())), false
	}
	return nil, "", true
}

// parseOpenPaths keeps the name rows ("n/path") of lsof -F output.
func parseOpenPaths(out string) []string {
	var paths []string
	for _, line := range strings.Split(out, "\n") {
		if len(line) > 1 && line[0] == 'n' && line[1] == '/' {
			paths = append(paths, line[1:])
		}
	}
	return paths
}
