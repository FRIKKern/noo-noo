---
name: verify-noo-noo-task
description: |
  Independently verify each Phase 0.1 implementation task of the noo-noo
  project as commits land. Invokes the deterministic shell verifier and
  routes pass/fail notifications. Halts the implementer on FAIL.
tags: [noo-noo, verification, ci]
---

# Skill: verify-noo-noo-task

You are the independent verifier for the noo-noo Phase 0.1 implementation
plan. Your job is to detect new commits on the `phase-0.1` branch, invoke
the deterministic verifier, and report results — not to judge code yourself.

## Configuration

```yaml
repo_path: ~/Documents/GitHub/noo-noo
branch: phase-0.1
poll_interval_seconds: 30
notify_channel: desktop      # one of: desktop | telegram | slack | none
notify_target: ""            # chat id / channel id; empty for desktop
halt_on_fail: true           # write .verify-blocked sentinel
```

## Loop

1. **Watch.** Every `poll_interval_seconds`, run inside `repo_path`:

   ```sh
   git fetch --quiet
   git log -1 --format='%H %s' refs/heads/$branch
   ```

   If the HEAD SHA on `$branch` has not changed since your last run, sleep
   and repeat.

2. **Identify the task.** Parse `[task N]` from the commit message body:

   ```sh
   git log -1 --format=%B $sha | grep -oE '\[task [0-9]+\]' | grep -oE '[0-9]+'
   ```

   If `[task N]` is absent, fall back: read
   `verification-results/phase-0.1.jsonl`, find the highest task with
   `outcome == "ok"`, and assume this commit is task N+1.

3. **Verify.** Run the deterministic verifier:

   ```sh
   bash scripts/verify/verify-task.sh $task_id $sha
   ```

   Capture the exit code. **Do not interpret the verdict yourself.** The
   exit code is the truth: 0 = PASS, non-zero = FAIL.

4. **Read the verdict.** The last line of
   `verification-results/phase-0.1.jsonl` contains the structured record
   for this run.

5. **Notify.** Format and send a message according to `notify_channel`:

   - **PASS:** `"✅ noo-noo task {N} PASS — {commit_subject} ({short_sha})"`
   - **FAIL:** `"❌ noo-noo task {N} FAIL — {reason} ({short_sha})\n\nSee verification-results/phase-0.1.jsonl line {line_number}."`

   For `desktop`, use `osascript -e 'display notification "..."'` on macOS.
   For `telegram` / `slack`, use the corresponding OpenClaw integration.

6. **Halt or release.** On FAIL, the verifier already wrote
   `.verify-blocked` at the repo root. Do nothing further — leave it.
   On PASS, the verifier already removed the sentinel.

7. **Record skill state.** Persist the last-seen SHA so you don't
   re-verify the same commit on the next poll.

## Manual one-shot mode

If a user asks you to "verify task N" without entering the watch loop,
run only steps 3-5 once for the given N against `HEAD`, then exit.

## What you must NOT do

- **Do not modify code** to make the verifier pass.
- **Do not amend or rewrite commits.**
- **Do not skip a failing task** to "see if the next one works."
- **Do not interpret test output yourself** — the verifier's exit code
  is the only truth source.

If the verifier appears to be wrong (e.g., a test passes locally but the
verifier says FAIL), do not silence it. Surface the discrepancy in the
notification: `"⚠️ verifier reports FAIL but go test passes locally —
investigate scripts/verify/verify-task.sh case for task N"`.

## Failure modes you should report

- `scripts/verify/verify-task.sh` not executable → `chmod +x`, retry once,
  then report.
- Working tree dirty (uncommitted changes) → tell the user; do not auto-stash.
- Branch `phase-0.1` does not exist → tell the user; exit watch loop.
- `verification-results/phase-0.1.jsonl` missing after the verifier ran →
  the verifier crashed; surface the verifier's stderr verbatim.
