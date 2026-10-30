#!/usr/bin/env bash
# Scenario runner for issue #295.
#   1. ./test/test_new_ready.sh passes and `lead ready 42` freezes the issue
#      scenario at <dir of $LEAD_STATE_FILE>/scenarios/o_r/42.md.
#   2. go test ./internal/{scenario,finish,dispatch} passes and finish refuses
#      to merge when the issue body has ## シナリオ but no stored scenario.
#   3. ./test/run-tests.sh passes (existing CLI behaviour is intact).
set -uo pipefail

here="$(cd "$(dirname "$0")" && pwd)"
ROOT="$(pwd)"
logs="$(mktemp -d)"
check_tmp=""
cleanup() {
  [ -n "$check_tmp" ] && rm -rf "$check_tmp"
  [ -n "${LEAD_SCENARIO_KEEP_LOGS:-}" ] && { echo "logs kept in $logs" >&2; return; }
  rm -rf "$logs"
}
trap cleanup EXIT

results=()
verdict() { # verdict <n> <0|1>
  if [ "$2" -eq 0 ]; then
    echo "SCENARIO $1: pass"
    results+=("$1:pass")
  else
    echo "SCENARIO $1: fail"
    results+=("$1:fail")
  fi
}

show() { # show <log> <lines>
  [ -f "$1" ] || return 0
  echo "--- $1 (tail) ---" >&2
  tail -n "${2:-25}" "$1" >&2
}

# TestLoop_CancelDoesNotKillAgent is timing-flaky in this repository: it kills
# the fake agent and then reads the argv log the agent may not have written
# yet. A failure of that test alone is retried instead of failing the step;
# any other failure (or a second flake) is a real fail.
flake_test="TestLoop_CancelDoesNotKillAgent"
go_test_retrying_flake() { # <log> <packages...>
  local log="$1"; shift
  if go test "$@" >"$log" 2>&1; then
    return 0
  fi
  if [ -n "$(grep -E '^--- FAIL:' "$log" | grep -v "$flake_test" || true)" ]; then
    return 1
  fi
  grep -q "^--- FAIL: $flake_test" "$log" || return 1
  echo "note: retrying the timing-flaky $flake_test" >&2
  local i
  for i in 1 2 3; do
    go test "$@" -run "^$flake_test\$" -count=1 >>"$log" 2>&1 && return 0
  done
  return 1
}

# --- Step 1 -----------------------------------------------------------------
step1() {
  if ! bash ./test/test_new_ready.sh >"$logs/step1_new_ready.log" 2>&1; then
    echo "step1: test/test_new_ready.sh failed" >&2
    show "$logs/step1_new_ready.log" 25
    return 1
  fi
  if ! bash "$here/ready_freeze_check.sh" "$ROOT" >"$logs/step1_freeze.log" 2>&1; then
    echo "step1: lead ready 42 did not freeze the scenario where expected" >&2
    show "$logs/step1_freeze.log" 25
    return 1
  fi
  return 0
}

# --- Step 2 -----------------------------------------------------------------
step2() {
  if ! go_test_retrying_flake "$logs/step2_go.log" ./internal/scenario ./internal/finish ./internal/dispatch; then
    echo "step2: go test ./internal/scenario ./internal/finish ./internal/dispatch failed" >&2
    show "$logs/step2_go.log" 40
    return 1
  fi
  check_tmp="$ROOT/scenario-check-tmp"
  rm -rf "$check_tmp"
  mkdir -p "$check_tmp"
  cp "$here/finish_gate_test.go" "$check_tmp/finish_gate_test.go"
  if ! go test ./scenario-check-tmp >"$logs/step2_gate.log" 2>&1; then
    echo "step2: finish merged a body-scenario issue with no stored scenario (or the control failed)" >&2
    show "$logs/step2_gate.log" 40
    rm -rf "$check_tmp"; check_tmp=""
    return 1
  fi
  rm -rf "$check_tmp"; check_tmp=""
  return 0
}

# --- Step 3 -----------------------------------------------------------------
step3() {
  local log="$logs/step3_suite.log"
  if bash ./test/run-tests.sh >"$log" 2>&1; then
    return 0
  fi
  # run-tests.sh only reaches go test when every test/test_*.sh passed, so a
  # missing go test banner means a shell test failed and is never a flake.
  if ! grep -qxF '=== go test ./... ===' "$log"; then
    echo "step3: a test/test_*.sh script failed" >&2
    grep -E '^(FAIL|Testing .*\.\.\. FAIL)' "$log" | tail -n 20 >&2
    return 1
  fi
  if ! go_test_retrying_flake "$logs/step3_go.log" ./...; then
    echo "step3: go test ./... failed" >&2
    grep -E '^(--- FAIL|FAIL)' "$log" | tail -n 20 >&2
    show "$logs/step3_go.log" 40
    return 1
  fi
  echo "note: the suite passed after retrying the timing-flaky $flake_test" >&2
  return 0
}

step1; verdict 1 $?
step2; verdict 2 $?
step3; verdict 3 $?

exit 0
