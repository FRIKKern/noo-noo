#!/bin/bash
#
# internal_test.sh — self-test for verify-task.sh
#
# Builds a sandbox git repo, exercises the happy path and each failure
# mode against task 2 (the simplest non-Makefile task), and asserts that
# verify-task.sh returns the expected exit code in each case.
#
# Exit 0  — all scenarios behaved as expected (verifier is sound).
# Exit 1  — at least one scenario failed.
#
# Run from the repo root: bash scripts/verify/internal_test.sh

set -u

HERE="$(cd "$(dirname "$0")" && pwd)"
VERIFIER="$HERE/verify-task.sh"

if [[ ! -x "$VERIFIER" ]]; then
  echo "verify-task.sh not executable at $VERIFIER" >&2
  exit 2
fi
if ! command -v go >/dev/null 2>&1; then
  echo "go is required" >&2
  exit 2
fi
if ! command -v git >/dev/null 2>&1; then
  echo "git is required" >&2
  exit 2
fi

ok=0
fail=0
failed_scenarios=()

# ---------- helpers --------------------------------------------------------

# init_sandbox: creates a fresh temp repo with the verifier copied in,
# go.mod pre-committed (mirroring Phase 0.1 Task 1's state), so the next
# commit can be a clean Task-2-style change.
init_sandbox() {
  local sb v
  sb="$(mktemp -d)"
  cd "$sb" || exit 2
  git init -q -b main
  git config user.email "test@test"
  git config user.name "test"

  mkdir -p scripts/verify
  cp "$VERIFIER" scripts/verify/verify-task.sh
  chmod +x scripts/verify/verify-task.sh

  v="$(go env GOVERSION 2>/dev/null | sed -E 's/^go//; s/\.[0-9]+$//')"
  [[ -z "$v" ]] && v="1.21"
  cat > go.mod <<EOF
module testmod

go $v
EOF

  # Minimal Makefile so `make lint` works in the sandbox. Uses gofmt + go vet
  # only (skips golangci-lint) — that's enough to validate verifier behaviour.
  printf '%s\n' \
    '.PHONY: lint' \
    'lint:' \
    "$(printf '\tgofmt -l . | tee /dev/stderr | (! read)')" \
    "$(printf '\tgo vet ./...')" \
    > Makefile

  git add .
  git commit -q -m "chore: initial scaffold"
  printf '%s' "$sb"
}

# write_size_files: a minimal internal/core/size.go + matching passing test.
write_size_files() {
  mkdir -p internal/core
  cat > internal/core/size.go <<'GO'
package core

type Bytes int64

func (b Bytes) String() string { return "x" }

func ParseBytes(s string) (Bytes, error) { return 0, nil }
GO
  cat > internal/core/size_test.go <<'GO'
package core

import "testing"

func TestBytesString(t *testing.T) {
	if Bytes(0).String() != "x" {
		t.Fatal("unexpected")
	}
}

func TestBytesParse(t *testing.T) {
	if _, err := ParseBytes("0"); err != nil {
		t.Fatal(err)
	}
}
GO
}

# run_scenario NAME EXPECTED_EXIT SETUP_FN
# SETUP_FN runs inside the sandbox and is responsible for creating files
# and committing. The verifier is then invoked for task 2 against HEAD.
run_scenario() {
  local name="$1" expected="$2" setup="$3"
  local sb
  sb="$(init_sandbox)"
  ( cd "$sb" && $setup ) || { echo "setup failed for: $name" >&2; fail=$((fail+1)); failed_scenarios+=("$name (setup)"); return; }

  local out actual
  out="$(cd "$sb" && bash scripts/verify/verify-task.sh 2 2>&1)"
  actual=$?

  if [[ $actual -eq $expected ]]; then
    printf '  ok    %s (exit %d)\n' "$name" "$actual"
    ok=$((ok+1))
  else
    printf '  FAIL  %s (expected exit %d, got %d)\n' "$name" "$expected" "$actual"
    printf '        %s\n' "$out" | head -5
    fail=$((fail+1))
    failed_scenarios+=("$name")
  fi

  rm -rf "$sb"
}

# ---------- scenarios -------------------------------------------------------

scenario_happy() {
  write_size_files
  git add -A
  git commit -q -m "feat(core): add Bytes type"
}

scenario_wrong_prefix() {
  write_size_files
  git add -A
  git commit -q -m "fix: this is the wrong prefix"
}

scenario_missing_test_file() {
  mkdir -p internal/core
  cat > internal/core/size.go <<'GO'
package core
type Bytes int64
GO
  git add -A
  git commit -q -m "feat(core): only the source file, no test"
}

scenario_scope_creep() {
  write_size_files
  echo "junk" > unrelated.txt
  git add -A
  git commit -q -m "feat(core): includes an unrelated file"
}

scenario_failing_test() {
  mkdir -p internal/core
  cat > internal/core/size.go <<'GO'
package core
type Bytes int64
GO
  cat > internal/core/size_test.go <<'GO'
package core
import "testing"
func TestBytesFails(t *testing.T) { t.Fatal("forced failure") }
GO
  git add -A
  git commit -q -m "feat(core): test that fails"
}

scenario_no_test_passes() {
  # Test file present but contains no Test* functions matching `TestBytes`.
  mkdir -p internal/core
  cat > internal/core/size.go <<'GO'
package core
type Bytes int64
GO
  cat > internal/core/size_test.go <<'GO'
package core
GO
  git add -A
  git commit -q -m "feat(core): empty test file"
}

# ---------- run ------------------------------------------------------------

echo "running verifier self-test scenarios..."
echo

run_scenario "happy path"           0 scenario_happy
run_scenario "wrong commit prefix"  1 scenario_wrong_prefix
run_scenario "missing test file"    1 scenario_missing_test_file
run_scenario "scope creep"          1 scenario_scope_creep
run_scenario "failing test"         1 scenario_failing_test
run_scenario "no PASS lines"        1 scenario_no_test_passes

echo
printf 'Scenarios: %d ok, %d failed\n' "$ok" "$fail"
if (( fail > 0 )); then
  printf 'Failed: %s\n' "${failed_scenarios[*]}"
  exit 1
fi
echo "Verifier self-test: PASS"
exit 0
