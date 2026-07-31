package worktrees

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/FRIKKern/noo-noo/internal/modules"
)

// Dossier is the judgment-tier package handed to the AI judge: everything a
// ruling needs in one structured blob, no filesystem access required.
type Dossier struct {
	Path            string `json:"path"`
	Repo            string `json:"repo"`
	Branch          string `json:"branch"`
	Head            string `json:"head"`
	SizeBytes       int64  `json:"size_bytes"`
	Idle            string `json:"idle"`
	LastCommit      string `json:"last_commit"`
	Dirty           string `json:"dirty,omitempty"`
	RemoteContained bool   `json:"remote_contained"`
	AheadUpstream   string `json:"ahead_of_upstream,omitempty"`
}

// DossierFromItem lifts a judgment-tier report row into a Dossier.
func DossierFromItem(it modules.Item) Dossier {
	ev := it.Evidence
	return Dossier{
		Path:            it.Path,
		Repo:            ev["repo"],
		Branch:          ev["branch"],
		Head:            ev["head"],
		SizeBytes:       int64(it.Size),
		Idle:            ev["idle"],
		LastCommit:      ev["last_commit"],
		Dirty:           ev["dirty"],
		RemoteContained: ev["remote_contained"] == "true",
		AheadUpstream:   ev["ahead_of_upstream"],
	}
}

// Verdict is what the judge must answer, and all it may answer.
type Verdict struct {
	Verdict string `json:"verdict"` // "remove" | "keep"
	Reason  string `json:"reason"`
}

// judgeInstruction frames the ruling. The judge sees ONE dossier per
// invocation — batch judging invites cross-contamination of reasoning.
const judgeInstruction = `You are judging whether a git worktree is dead and safe to remove, or still carries value.

It already failed the mechanical safety proofs (it has dirty files, unpushed commits, or its HEAD is on no remote), so judgment is required. Before any removal the tool grave-bundles all commits and archives dirty files — a wrong "remove" loses convenience, not data. Still, rule "keep" whenever the dossier suggests work a human might come back for: meaningful dirty paths (source files, docs), commits whose subjects look like real work, a branch named like an open effort. Rule "remove" for abandoned residue: probe/experiment naming, dirty paths that are build artifacts or logs, ancient idle times, spike branches whose work clearly landed elsewhere.

Reply with STRICT JSON only, no prose around it: {"verdict":"remove"|"keep","reason":"<one sentence citing the dossier>"}

Dossier:
`

// Judge runs the configured judge command over one dossier and parses its
// ruling. The command gets instruction+dossier on stdin and must print the
// verdict JSON (anything around it is tolerated; the first JSON object
// wins). Any failure to produce a parseable ruling is KEEP — the fail-safe
// direction, exactly like the prober's LIVE.
func (m *Module) Judge(ctx context.Context, d Dossier) (Verdict, error) {
	if m.cfg.JudgeCmd == "" {
		return Verdict{}, fmt.Errorf("worktrees: no judge_cmd configured ([worktrees] in config.toml, e.g. \"claude -p\")")
	}
	blob, err := json.MarshalIndent(d, "", "  ")
	if err != nil {
		return Verdict{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", m.cfg.JudgeCmd)
	cmd.Stdin = strings.NewReader(judgeInstruction + string(blob) + "\n")
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return Verdict{}, fmt.Errorf("judge command: %w (stderr %q)", err, strings.TrimSpace(stderr.String()))
	}
	v, err := parseVerdict(stdout.String())
	if err != nil {
		return Verdict{}, fmt.Errorf("judge output unparseable (%v); raw: %q", err, truncate(stdout.String(), 300))
	}
	return v, nil
}

// parseVerdict extracts the first JSON object carrying a valid verdict from
// possibly-chatty judge output.
func parseVerdict(out string) (Verdict, error) {
	dec := json.NewDecoder(strings.NewReader(out[strings.IndexByte(out+"{", '{'):]))
	for {
		var v Verdict
		if err := dec.Decode(&v); err != nil {
			return Verdict{}, err
		}
		if v.Verdict == "remove" || v.Verdict == "keep" {
			return v, nil
		}
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// tarDirty archives the worktree's modified+untracked (non-ignored) files.
// NUL-separated listing throughout — dirty paths with spaces are data, not
// delimiters. wrote=false means the tree was clean and no tarball exists.
func tarDirty(ctx context.Context, wtPath, tarball string) (wrote bool, err error) {
	list := exec.CommandContext(ctx, "git", "-C", wtPath, "ls-files", "-z", "--others", "--modified", "--exclude-standard")
	out, err := list.Output()
	if err != nil {
		return false, err
	}
	var files []string
	for _, f := range strings.Split(string(out), "\x00") {
		if f != "" {
			files = append(files, f)
		}
	}
	if len(files) == 0 {
		return false, nil
	}
	args := append([]string{"-czf", tarball, "-C", wtPath, "--"}, files...)
	tar := exec.CommandContext(ctx, "tar", args...)
	var stderr bytes.Buffer
	tar.Stderr = &stderr
	if err := tar.Run(); err != nil {
		return false, fmt.Errorf("%w (%s)", err, strings.TrimSpace(stderr.String()))
	}
	return true, nil
}
