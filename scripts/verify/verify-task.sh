#!/bin/bash
#
# verify-task.sh TASK_ID [COMMIT]
#
# Verifies that one Phase 0.1 implementation task was correctly completed.
# Exits 0 on PASS, non-zero on FAIL. Always appends one JSONL record to
# verification-results/phase-0.1.jsonl.
#
# This script is the deterministic core of the OpenClaw verification layer:
# the pass/fail verdict is decided here, not by an LLM. OpenClaw's job is
# only to invoke this script and route notifications.
#
# Per-task rules are encoded as case arms below. To add a new task: add a
# case arm. To change a rule: edit the relevant case arm.

set -u

TASK="${1:?usage: $0 TASK_ID [COMMIT]}"
COMMIT="${2:-HEAD}"
RESULTS_DIR="${RESULTS_DIR:-verification-results}"
RESULTS_FILE="$RESULTS_DIR/phase-0.1.jsonl"

mkdir -p "$RESULTS_DIR"

# ---------- helpers ---------------------------------------------------------

short_sha() { git rev-parse --short "$COMMIT" 2>/dev/null || echo "unknown"; }

# json_escape: minimal escaper for embedding in a JSON string. Handles
# backslashes, double-quotes, and newlines. Falls back to jq when present.
json_escape() {
  if command -v jq >/dev/null 2>&1; then
    printf '%s' "$1" | jq -Rs .
  else
    local s="$1"
    s="${s//\\/\\\\}"
    s="${s//\"/\\\"}"
    s="${s//$'\n'/\\n}"
    printf '"%s"' "$s"
  fi
}

record() {
  local outcome="$1" reason="$2"
  local ts sha esc
  ts="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
  sha="$(short_sha)"
  esc="$(json_escape "$reason")"
  printf '{"ts":"%s","task":%d,"commit":"%s","outcome":"%s","reason":%s}\n' \
    "$ts" "$TASK" "$sha" "$outcome" "$esc" >> "$RESULTS_FILE"
}

pass() {
  echo "PASS (task $TASK)"
  record "ok" ""
  rm -f .verify-blocked
  exit 0
}

fail() {
  local msg="$*"
  echo "FAIL (task $TASK): $msg" >&2
  record "fail" "$msg"
  printf 'task %d: %s\n' "$TASK" "$msg" > .verify-blocked
  exit 1
}

# require_commit_prefix PREFIX
require_commit_prefix() {
  local prefix="$1"
  local msg
  msg="$(git log -1 --format=%s "$COMMIT")"
  case "$msg" in
    "$prefix"*) ;;
    *) fail "commit message '$msg' does not start with '$prefix'" ;;
  esac
}

# require_files_changed FILE1 FILE2 ...
# Asserts the diff includes every file (no missing) and excludes any file
# not in the union of expected paths (no scope creep).
require_files_changed() {
  local expected=("$@")
  local actual
  actual="$(git diff --name-only "$COMMIT~1..$COMMIT" 2>/dev/null | sort -u)"

  # Missing-file check.
  local f
  for f in "${expected[@]}"; do
    if ! grep -Fxq "$f" <<<"$actual"; then
      fail "expected file not in diff: $f (got: $(echo "$actual" | tr '\n' ' '))"
    fi
  done

  # Scope-creep check: every file in actual must be in expected (allowing
  # glob expansion via shell pattern matching).
  while IFS= read -r f; do
    [[ -z "$f" ]] && continue
    local matched=0
    local e
    for e in "${expected[@]}"; do
      # shellcheck disable=SC2053
      if [[ "$f" == $e ]]; then matched=1; break; fi
    done
    if [[ $matched -eq 0 ]]; then
      fail "scope creep: $f changed but not in expected file list"
    fi
  done <<<"$actual"
}

# require_test PKG TESTNAME
# Runs `go test PKG -run TESTNAME -v` and asserts at least one PASS line.
require_test() {
  local pkg="$1" name="$2"
  local out
  if ! out="$(go test "$pkg" -run "$name" -v 2>&1)"; then
    fail "go test $pkg -run $name returned non-zero (output: $(echo "$out" | tail -10 | tr '\n' '|'))"
  fi
  if ! grep -q '^--- PASS:' <<<"$out"; then
    fail "go test $pkg -run $name had no PASS lines (output: $(echo "$out" | tail -10 | tr '\n' '|'))"
  fi
}

# require_test_pkg PKG — runs `go test PKG -v`, asserts overall pass.
require_test_pkg() {
  local pkg="$1"
  local out
  if ! out="$(go test "$pkg" -v 2>&1)"; then
    fail "go test $pkg returned non-zero (output: $(echo "$out" | tail -10 | tr '\n' '|'))"
  fi
}

# require_build PATH — runs `go build PATH` and fails on non-zero.
require_build() {
  local path="$1"
  local out
  if ! out="$(go build "$path" 2>&1)"; then
    fail "go build $path failed: $out"
  fi
}

# require_cmd CMD — runs CMD as bash one-liner; fails on non-zero exit.
require_cmd() {
  local cmd="$1" desc="${2:-$cmd}"
  if ! eval "$cmd" >/dev/null 2>&1; then
    fail "$desc failed"
  fi
}

# ---------- per-task rules --------------------------------------------------

case "$TASK" in
  1)
    # go.mod is already at 'go 1.23' from the scaffold commit, so it's
    # asserted by content rather than by appearing in the diff.
    require_commit_prefix "chore:"
    require_files_changed "Makefile" ".golangci.yml"
    require_cmd "grep -q 'go 1.23' go.mod" "go.mod has 'go 1.23'"
    require_cmd "make lint" "make lint"
    pass
    ;;

  2)
    require_commit_prefix "feat(core):"
    require_files_changed "internal/core/size.go" "internal/core/size_test.go"
    require_test "./internal/core/" "TestBytes"
    require_cmd "make lint" "make lint"
    pass
    ;;

  3)
    require_commit_prefix "feat(core):"
    require_files_changed "internal/core/walk.go" "internal/core/walk_test.go"
    require_test "./internal/core/" "TestDirSize"
    require_cmd "make lint" "make lint"
    pass
    ;;

  4)
    require_commit_prefix "feat(core):"
    require_files_changed "internal/core/safety.go" "internal/core/safety_test.go"
    require_test "./internal/core/" "TestSafety"
    require_cmd "make lint" "make lint"
    pass
    ;;

  5)
    require_commit_prefix "feat(audit):"
    require_files_changed "internal/audit/audit.go" "internal/audit/audit_test.go"
    require_test_pkg "./internal/audit/"
    require_cmd "make lint" "make lint"
    pass
    ;;

  6)
    require_commit_prefix "feat(modules):"
    require_files_changed "internal/modules/module.go"
    require_build "./internal/modules/"
    require_cmd "make lint" "make lint"
    pass
    ;;

  7)
    require_commit_prefix "feat(dev):"
    # Two source files + test file allowed.
    require_files_changed "internal/modules/dev/dev.go" "internal/modules/dev/dev_test.go" "internal/modules/dev/syscalls.go"
    require_test "./internal/modules/dev/" "TestScan"
    require_cmd "make lint" "make lint"
    pass
    ;;

  8)
    require_commit_prefix "test(dev):"
    require_files_changed "internal/modules/dev/dev_test.go"
    require_test "./internal/modules/dev/" "TestApply"
    require_cmd "make lint" "make lint"
    pass
    ;;

  9)
    require_commit_prefix "feat(caches):"
    require_files_changed "internal/modules/caches/caches.go" "internal/modules/caches/caches_test.go"
    require_test_pkg "./internal/modules/caches/"
    require_cmd "make lint" "make lint"
    pass
    ;;

  10)
    require_commit_prefix "feat(startup):"
    require_files_changed "internal/modules/startup/runner.go" "internal/modules/startup/runner_test.go"
    require_test "./internal/modules/startup/" "TestFakeRunner"
    require_cmd "make lint" "make lint"
    pass
    ;;

  11)
    require_commit_prefix "feat(startup):"
    require_files_changed "internal/modules/startup/startup.go" "internal/modules/startup/startup_test.go"
    require_test "./internal/modules/startup/" "TestScan|TestApply|TestSystem"
    require_cmd "make lint" "make lint"
    pass
    ;;

  12)
    require_commit_prefix "feat(cli):"
    require_files_changed "internal/cli/cli.go" "internal/cli/cli_test.go"
    require_test "./internal/cli/" "TestDispatch"
    require_cmd "make lint" "make lint"
    pass
    ;;

  13)
    require_commit_prefix "feat(cli):"
    require_files_changed "internal/cli/confirm.go" "internal/cli/output.go"
    require_build "./internal/cli/"
    require_cmd "make lint" "make lint"
    pass
    ;;

  14)
    require_commit_prefix "feat(cli):"
    require_files_changed "internal/cli/dev_cmd.go"
    require_build "./..."
    require_cmd "make lint" "make lint"
    pass
    ;;

  15)
    require_commit_prefix "feat(cli):"
    require_files_changed "internal/cli/caches_cmd.go"
    require_build "./..."
    require_cmd "make lint" "make lint"
    pass
    ;;

  16)
    require_commit_prefix "feat(cli):"
    require_files_changed "internal/cli/startup_cmd.go"
    require_build "./..."
    require_cmd "make lint" "make lint"
    pass
    ;;

  17)
    require_commit_prefix "feat(cli):"
    require_files_changed "internal/cli/report_cmd.go"
    require_build "./..."
    require_cmd "make lint" "make lint"
    pass
    ;;

  18)
    require_commit_prefix "feat(cmd):"
    require_files_changed "cmd/noo-noo/main.go"
    require_cmd "make build" "make build"
    require_cmd "./bin/noo-noo 2>&1 | grep -q '^Usage:'" "binary prints Usage banner"
    require_cmd "make lint" "make lint"
    pass
    ;;

  19)
    require_commit_prefix "test(cli):"
    require_files_changed "internal/cli/e2e_test.go"
    require_cmd "make test" "make test (full suite)"
    require_cmd "make lint" "make lint"
    pass
    ;;

  20)
    require_commit_prefix "docs:"
    require_files_changed "README.md"
    require_cmd "git tag --list | grep -q '^v0.1.0$'" "v0.1.0 tag exists"
    require_cmd "make test" "make test"
    require_cmd "make lint" "make lint"
    pass
    ;;

  *)
    fail "unknown task id $TASK (valid: 1-20)"
    ;;
esac
