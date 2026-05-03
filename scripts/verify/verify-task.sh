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

  21)
    # Self-verifying: only syntax-check the verifier (running it on its own
    # commit would recurse oddly).
    require_commit_prefix "chore(verify):"
    require_files_changed "scripts/verify/verify-task.sh"
    require_cmd "bash -n scripts/verify/verify-task.sh" "bash -n scripts/verify/verify-task.sh"
    pass
    ;;

  22)
    # tools.go is the canonical Go pattern for keeping deps pinned in go.mod
    # before any production code imports them. It's removed once a real
    # importer lands (Task 23 for go-toml/v2, Task 24 for sqlite).
    require_commit_prefix "chore(deps):"
    require_files_changed "go.mod" "go.sum" "tools.go"
    require_cmd "go mod tidy && git diff --quiet -- go.mod go.sum" "go mod tidy clean (no diff)"
    require_cmd "go build ./..." "go build ./..."
    require_cmd "make lint" "make lint"
    pass
    ;;

  23)
    require_commit_prefix "feat(config):"
    require_files_changed "internal/config/config.go" "internal/config/config_test.go"
    require_test_pkg "./internal/config/"
    require_cmd "make lint" "make lint"
    pass
    ;;

  24)
    require_commit_prefix "feat(store):"
    require_files_changed "internal/store/store.go" "internal/store/store_test.go" "internal/store/schema.sql"
    require_test "./internal/store/" "TestOpen"
    require_cmd "make lint" "make lint"
    pass
    ;;

  25)
    require_commit_prefix "feat(store):"
    require_files_changed "internal/store/cache_history.go" "internal/store/cache_history_test.go"
    require_test "./internal/store/" "TestCacheHistory"
    require_cmd "make lint" "make lint"
    pass
    ;;

  26)
    require_commit_prefix "feat(store):"
    require_files_changed "internal/store/idleness.go" "internal/store/idleness_test.go"
    require_test "./internal/store/" "TestIdleness"
    require_cmd "make lint" "make lint"
    pass
    ;;

  27)
    require_commit_prefix "feat(store):"
    require_files_changed "internal/store/actions.go" "internal/store/actions_test.go" "internal/store/suggestions.go" "internal/store/suggestions_test.go"
    require_test "./internal/store/" "TestActions|TestSuggestions"
    require_cmd "make lint" "make lint"
    pass
    ;;

  28)
    require_commit_prefix "feat(metrics):"
    require_files_changed "internal/metrics/vmstat.go" "internal/metrics/vmstat_test.go" "internal/metrics/testdata/vm_stat_fixture.txt"
    require_test "./internal/metrics/" "TestVMStat"
    require_cmd "make lint" "make lint"
    pass
    ;;

  29)
    require_commit_prefix "feat(metrics):"
    require_files_changed "internal/metrics/sysctl.go" "internal/metrics/sysctl_test.go"
    require_test "./internal/metrics/" "TestSysctl|TestLoadAvg"
    require_cmd "make lint" "make lint"
    pass
    ;;

  30)
    require_commit_prefix "feat(heuristics):"
    require_files_changed "internal/heuristics/types.go" "internal/heuristics/idle_repos.go" "internal/heuristics/idle_repos_test.go"
    require_test "./internal/heuristics/" "TestIdleRepos"
    require_cmd "make lint" "make lint"
    pass
    ;;

  31)
    require_commit_prefix "feat(heuristics):"
    require_files_changed "internal/heuristics/cache_velocity.go" "internal/heuristics/cache_velocity_test.go"
    require_test "./internal/heuristics/" "TestCacheVelocity"
    require_cmd "make lint" "make lint"
    pass
    ;;

  32)
    require_commit_prefix "feat(notify):"
    require_files_changed "internal/notify/notify.go" "internal/notify/notify_test.go"
    require_test_pkg "./internal/notify/"
    require_cmd "make lint" "make lint"
    pass
    ;;

  33)
    require_commit_prefix "feat(launchd):"
    require_files_changed "internal/launchd/plist.go" "internal/launchd/plist_test.go" "internal/launchd/testdata/golden.plist"
    require_test "./internal/launchd/" "TestPlist"
    require_cmd "make lint" "make lint"
    pass
    ;;

  34)
    require_commit_prefix "feat(launchd):"
    require_files_changed "internal/launchd/install.go" "internal/launchd/install_test.go"
    require_test "./internal/launchd/" "TestInstall"
    require_cmd "make lint" "make lint"
    pass
    ;;

  35)
    require_commit_prefix "feat(ipc):"
    require_files_changed "internal/ipc/protocol.go" "internal/ipc/server.go" "internal/ipc/client.go" "internal/ipc/server_test.go" "internal/ipc/client_test.go"
    require_test "./internal/ipc/" "TestServer|TestClient"
    require_cmd "make lint" "make lint"
    pass
    ;;

  36)
    require_commit_prefix "feat(ipc):"
    require_files_changed "internal/ipc/report_method.go" "internal/ipc/report_method_test.go"
    require_test "./internal/ipc/" "TestReport"
    require_cmd "make lint" "make lint"
    pass
    ;;

  37)
    require_commit_prefix "feat(ipc):"
    require_files_changed "internal/ipc/suggestions_method.go" "internal/ipc/suggestions_method_test.go"
    require_test "./internal/ipc/" "TestSuggestions"
    require_cmd "make lint" "make lint"
    pass
    ;;

  38)
    require_commit_prefix "feat(ipc):"
    require_files_changed "internal/ipc/clean_method.go" "internal/ipc/clean_method_test.go"
    require_test "./internal/ipc/" "TestClean"
    require_cmd "make lint" "make lint"
    pass
    ;;

  39)
    # server.go is in the allowlist because Task 35 included a placeholder
    # Status to satisfy rpc.RegisterName; this task moves it out.
    require_commit_prefix "feat(ipc):"
    require_files_changed "internal/ipc/daemon_method.go" "internal/ipc/daemon_method_test.go" "internal/ipc/server.go"
    require_test "./internal/ipc/" "TestDaemonStatus"
    require_cmd "make lint" "make lint"
    pass
    ;;

  40)
    require_commit_prefix "feat(daemon):"
    require_files_changed "cmd/noo-nood/main.go" "cmd/noo-nood/main_test.go"
    require_build "./cmd/noo-nood/"
    require_test_pkg "./cmd/noo-nood/"
    require_cmd "make lint" "make lint"
    pass
    ;;

  41)
    require_commit_prefix "feat(cli):"
    require_files_changed "internal/cli/daemon_cmd.go" "internal/cli/daemon_cmd_test.go"
    require_test "./internal/cli/" "TestDaemonCmd"
    require_cmd "make lint" "make lint"
    pass
    ;;

  42)
    require_commit_prefix "feat(cli):"
    require_files_changed "internal/cli/suggestions_cmd.go" "internal/cli/suggestions_cmd_test.go"
    require_test "./internal/cli/" "TestSuggestionsCmd"
    require_cmd "make lint" "make lint"
    pass
    ;;

  43)
    require_commit_prefix "feat(cli):"
    require_files_changed "internal/cli/install_cmd.go" "internal/cli/install_cmd_test.go"
    require_test "./internal/cli/" "TestInstallCmd"
    require_cmd "make lint" "make lint"
    pass
    ;;

  44)
    require_commit_prefix "test(e2e):"
    require_files_changed "cmd/noo-nood/e2e_test.go"
    require_test "./cmd/noo-nood/" "TestEndToEnd"
    require_cmd "make test" "make test (full suite)"
    require_cmd "make lint" "make lint"
    pass
    ;;

  45)
    require_commit_prefix "docs:"
    require_files_changed "README.md"
    require_cmd "git tag --list | grep -q '^v0.2.0$'" "v0.2.0 tag exists"
    pass
    ;;

  *)
    fail "unknown task id $TASK (valid: 1-45)"
    ;;
esac
