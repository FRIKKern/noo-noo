# Verification scripts

Deterministic verifier for Phase 0.1 implementation tasks.

## Usage

```bash
# Verify the latest commit corresponds to a passing task N:
bash scripts/verify/verify-task.sh 2

# Or verify against a specific commit:
bash scripts/verify/verify-task.sh 2 a3f12c9
```

Exit code:
- `0` — PASS
- `1` — FAIL (reason printed to stderr)
- `2` — usage error (missing arg, etc.)

A JSONL record is appended to `verification-results/phase-0.1.jsonl` on every run, regardless of outcome. The directory is created if missing.

On FAIL, a sentinel file `.verify-blocked` is written at the repo root. The implementer (whether human or automated agent) should refuse to proceed while that file exists. PASS removes it.

## What it checks

For each of the 20 Phase 0.1 tasks, the script enforces:

1. **Commit message prefix** matches the task's expected type (`chore:`, `feat(core):`, `feat(dev):`, …).
2. **Files changed** match the task's declared file list — both must-include (no missing) and must-exclude (no scope creep beyond declared paths).
3. **Tests** for that task's package run and at least one `--- PASS:` line is present.
4. **Build** is clean for the affected package(s).
5. **Lint** passes for tasks 1, 19, 20 (full-repo checkpoints).

The exact rules per task live in the `case` arms of `verify-task.sh`. To change a rule, edit the script.

## Integration with `make`

Once Phase 0.1 Task 1 has created the Makefile, append this target:

```makefile
verify:
	@bash scripts/verify/verify-task.sh $(TASK)
```

Then: `make verify TASK=2`.

## Integration with OpenClaw

See `.openclaw/skills/verify-noo-noo-task.md` for the orchestration skill that watches commits, invokes this script, and posts notifications.

## Dependencies

- `bash` (4+; tested on macOS bash 3.2 with adjustments — see notes below)
- `git`
- `go` (1.23+)
- `make`
- `jq` (optional — fallback to `sed`-based JSON escaping if absent)

### Bash 3.2 (default macOS) compatibility

The script uses `[[ ]]`, `<<<`, and `(...)` arrays — all available in bash 3.2. No bash 4-only features (no associative arrays, no `mapfile`).

## Audit log

`verification-results/phase-0.1.jsonl` — one record per verification run:

```json
{"ts":"2026-05-03T08:14:22Z","task":2,"commit":"a3f12c","outcome":"ok","reason":""}
{"ts":"2026-05-03T08:18:47Z","task":3,"commit":"b81d09","outcome":"fail","reason":"expected file not in diff: internal/core/walk_test.go"}
```

The directory is gitignored by default (verdicts are local). To commit verdicts as part of project history, remove `verification-results/` from `.gitignore`.
