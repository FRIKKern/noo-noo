# OpenClaw Verification Plan — Phase 0.1

> **For agentic workers:** This plan defines the contract OpenClaw uses to independently verify each Phase 0.1 implementation task.

**Goal:** Every commit on the `phase-0.1` branch is verified by an independent process before the next task starts. Verification is deterministic (pass/fail decided by exit codes, not AI judgment); OpenClaw provides orchestration, notifications, and a structured audit trail.

**Status:** Plan + scaffolding ready. Activate when Phase 0.1 implementation begins.

---

## 1. Why two layers

| Layer | What it does | Who runs it |
|---|---|---|
| 1. `scripts/verify/verify-task.sh N` | Runs hard-coded checks for task N. Exit 0 = PASS, non-zero = FAIL. Appends one JSONL record. | Anyone — bash, Make, OpenClaw, CI. |
| 2. `.openclaw/skills/verify-noo-noo-task.md` | Watches for commits, invokes layer 1, posts summary to Telegram/Slack/desktop, halts implementer on FAIL. | OpenClaw. |

**Why split?** Determinism. The pass/fail decision is in shell with explicit exit codes — no AI in the loop for the verdict. OpenClaw's value is the orchestration around that verdict (when to run, where to post, who to alert). If OpenClaw goes down or you want CI to run the same verification, the deterministic core still works.

## 2. Design principles

1. **Deterministic verdict.** Pass/fail is decided by exit codes from `go test`, `make lint`, `git`. No LLM scores or judgment in the verdict path.
2. **One commit, one verification.** Each task ends in one commit. The verifier runs against that exact commit (`HEAD` by default; can be overridden).
3. **Scope-creep guard.** Each task declares the files it should touch. The verifier checks `git diff --name-only` against that list. A task that quietly modifies an unrelated file fails.
4. **Append-only audit trail.** Every verification run writes one JSONL record to `verification-results/phase-0.1.jsonl`. Never edited, never rotated by us.
5. **Halt on fail.** OpenClaw's job is to refuse to advance the implementer (whether human or another agent) until the verifier returns PASS.
6. **No fresh-checkout requirement (default).** Verification runs in the current working tree. If you want stronger isolation, do `git worktree add ../verify <commit>` and run verify-task.sh there. The script doesn't care.

## 3. Per-task verification rules

The 20 verification specs are encoded as `case` arms in `scripts/verify/verify-task.sh`. The high-level summary is below; the script is the source of truth.

| Task | Commit prefix | Files (must change) | Test / build / lint check |
|------|---|---|---|
| 1  | `chore:`        | `go.mod`, `Makefile`, `.golangci.yml` | `make lint`; `go.mod` contains `go 1.23` |
| 2  | `feat(core):`   | `internal/core/size.go`, `_test.go`   | `go test -run TestBytes` PASS |
| 3  | `feat(core):`   | `internal/core/walk.go`, `_test.go`   | `go test -run TestDirSize` PASS |
| 4  | `feat(core):`   | `internal/core/safety.go`, `_test.go` | `go test -run TestSafety` PASS |
| 5  | `feat(audit):`  | `internal/audit/audit.go`, `_test.go` | `go test ./internal/audit/` PASS |
| 6  | `feat(modules):`| `internal/modules/module.go`          | `go build ./internal/modules/` |
| 7  | `feat(dev):`    | `internal/modules/dev/*.go`           | `go test ./internal/modules/dev/ -run TestScan` PASS |
| 8  | `test(dev):`    | `internal/modules/dev/dev_test.go`    | `go test ./internal/modules/dev/ -run TestApply` PASS |
| 9  | `feat(caches):` | `internal/modules/caches/*.go`        | `go test ./internal/modules/caches/` PASS |
| 10 | `feat(startup):`| `internal/modules/startup/runner*.go` | `go test ./internal/modules/startup/ -run TestFakeRunner` PASS |
| 11 | `feat(startup):`| `internal/modules/startup/startup*.go`| `go test ./internal/modules/startup/ -run TestScan\\|TestApply\\|TestSystem` PASS |
| 12 | `feat(cli):`    | `internal/cli/cli.go`, `_test.go`     | `go test ./internal/cli/ -run TestDispatch` PASS |
| 13 | `feat(cli):`    | `internal/cli/confirm.go`, `output.go`| `go build ./internal/cli/` |
| 14 | `feat(cli):`    | `internal/cli/dev_cmd.go`             | `go build ./...` |
| 15 | `feat(cli):`    | `internal/cli/caches_cmd.go`          | `go build ./...` |
| 16 | `feat(cli):`    | `internal/cli/startup_cmd.go`         | `go build ./...` |
| 17 | `feat(cli):`    | `internal/cli/report_cmd.go`          | `go build ./...` |
| 18 | `feat(cmd):`    | `cmd/noo-noo/main.go`                 | `make build`; `./bin/noo-noo` prints `Usage:` |
| 19 | `test(cli):`    | `internal/cli/e2e_test.go`            | `make test` ALL PASS; `make lint` clean |
| 20 | `docs:`         | `README.md`                           | `git tag --list` contains `v0.1.0` |

For every task: `git diff --name-only HEAD~1..HEAD` must be a **subset** of the files declared above (no scope creep), and a **superset** (must include the declared files).

## 4. Audit log format

`verification-results/phase-0.1.jsonl`, append-only, one JSON object per run:

```json
{"ts":"2026-05-03T08:14:22Z","task":2,"commit":"a3f12c","outcome":"ok","reason":""}
{"ts":"2026-05-03T08:18:47Z","task":3,"commit":"b81d09","outcome":"fail","reason":"expected file not changed: internal/core/walk_test.go"}
```

Fields:
- `ts` — UTC timestamp (RFC3339, second precision)
- `task` — integer task ID
- `commit` — short SHA the verification ran against
- `outcome` — `"ok"` or `"fail"`
- `reason` — empty on PASS, human-readable failure message on FAIL

## 5. Execution flow

### Pattern A — Synchronous (recommended for in-session runs)

```
implementer commits task N
   │
   ▼
implementer runs:  make verify TASK=N
   │
   ▼
make verify shells out to scripts/verify/verify-task.sh N
   │
   ▼
exit 0  →  implementer proceeds to task N+1
exit !0 →  implementer halts, fixes, re-commits, re-verifies
```

OpenClaw is optional in this mode; the implementer can call `make verify` directly.

### Pattern B — Async (overnight runs, multi-machine, or notifications wanted)

```
implementer commits task N to phase-0.1 branch
   │
   ▼
OpenClaw skill `verify-noo-noo-task` polls the branch every N seconds
   │
   ▼
on new commit: skill invokes scripts/verify/verify-task.sh N (where N is parsed from commit message tag, see §7)
   │
   ▼
skill posts: Telegram / Slack / macOS notification with PASS/FAIL + link to the JSONL line
   │
   ▼
on FAIL: skill creates a sentinel file `.verify-blocked` so the implementer agent stops
on PASS: sentinel removed; implementer proceeds
```

## 6. Commit message convention (so the verifier knows which task)

Either:

- **Implicit (default):** verifier assumes `HEAD` corresponds to "the next un-verified task" by reading `verification-results/phase-0.1.jsonl` and finding the highest task with outcome `ok`, then verifying task N+1 against `HEAD`.
- **Explicit (recommended):** include `[task N]` in the commit message body. The verifier extracts it.

Example commit message:
```
feat(core): add Bytes type with human formatting and parsing

[task 2]
```

## 7. OpenClaw skill contract

The skill at `.openclaw/skills/verify-noo-noo-task.md` is a free-form instruction file (the exact OpenClaw skill format depends on your install — adapt the body to whatever your OpenClaw install accepts).

The skill must:

1. Watch the `phase-0.1` branch for new commits (poll `git log` every 30s, or hook into git via `post-commit` if running on the same machine).
2. On new commit, parse `[task N]` from the commit message. If absent, use the implicit rule from §6.
3. Run: `bash scripts/verify/verify-task.sh N`
4. Read the last line of `verification-results/phase-0.1.jsonl`.
5. Post a structured message:
    - **PASS:** `Task N PASS — <commit subject>`
    - **FAIL:** `Task N FAIL — <reason> — see verification-results/phase-0.1.jsonl`
6. On FAIL, write `.verify-blocked` containing the failing task ID and reason. On PASS, delete `.verify-blocked` if it exists.
7. Optional: post to a configured Telegram/Slack channel.

## 8. Failure recovery

When verification fails:

1. **Read the JSONL line** for the verdict + reason.
2. **Fix the underlying issue** in the working tree (don't try to game the verifier).
3. **Amend or add a fix-up commit.**
   - If the fix is small and atomic with the original task: `git commit --amend --no-edit`. The verifier re-runs against the new HEAD with the same task ID.
   - If the fix is substantive: `git commit -m "fix(...): <what>"` with `[task N]` in the body, then re-verify.
4. **Re-run verification:** `make verify TASK=N`.

The `.verify-blocked` sentinel only clears on PASS, so the implementer can't move on by mistake.

## 9. Setup steps (do once before Phase 0.1 implementation starts)

These tasks scaffold what's needed to make verification work. Each is small.

### 9.1 Install jq (required by verify-task.sh for safe JSON record writing)

```bash
brew install jq
```

(If jq is unavailable, the script falls back to `sed`-based escaping. jq is more robust for paths with special chars.)

### 9.2 Add `verify` target to Makefile

This integrates with **Phase 0.1 Task 1** (which creates the Makefile in the first place). When you reach Task 1, append this target to the Makefile:

```makefile
verify:
	@bash scripts/verify/verify-task.sh $(TASK)
```

Usage: `make verify TASK=2`

### 9.3 Configure OpenClaw notifications (optional)

Edit `.openclaw/skills/verify-noo-noo-task.md` and set the `notify_channel` field to your Telegram chat ID, Slack channel, or "desktop" for local notifications.

### 9.4 Bootstrap the audit trail directory

```bash
mkdir -p verification-results
echo 'phase-0.1.jsonl' >> verification-results/.gitignore   # don't commit verdicts
```

(Or commit them — verdicts as part of the project history is also defensible. Decide before Phase 0.1 begins.)

## 10. Open questions

- **Commit-as-task-boundary assumption.** The current spec assumes one commit per task. If a task needs multiple commits (e.g. a fix-up for a missed file), the verifier accepts the latest commit's HEAD. Is that strict enough? Alternative: require all task-N commits to be squashed before verification. **Decision deferred until execution.**
- **Verification in CI as well.** The same `verify-task.sh` could run in GitHub Actions on push. Probably overkill for solo dev, useful when contributors arrive. **Decision: add when contributor PRs start.**
- **Verification of the scaffold itself.** Should `scripts/verify/verify-task.sh` have its own unit tests? Probably yes — a buggy verifier silently passing bad code is the worst-case outcome. **Decision: add tests after first end-to-end use.**

## 11. Files in this scaffold

```
docs/plans/2026-05-02-openclaw-verification.md    # this document
scripts/verify/
├── verify-task.sh                                # the deterministic core
└── README.md                                     # usage docs
.openclaw/
└── skills/
    └── verify-noo-noo-task.md                    # OpenClaw orchestrator
```

The Makefile `verify` target is added during Phase 0.1 Task 1 (see §9.2).
