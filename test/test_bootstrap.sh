#!/usr/bin/env bash
# issue #327: 0→1 bootstrap (`lead bootstrap` + uninitialized interactive `lead` prompt).
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

fail() { echo "FAIL: $*" >&2; exit 1; }

REAL_GOPATH="$(go env GOPATH)"
REAL_GOCACHE="$(go env GOCACHE)"
REAL_PATH="$PATH"
export HOME="$tmp/home"
export GOPATH="$REAL_GOPATH" GOCACHE="$REAL_GOCACHE"
unset XDG_STATE_HOME || true
export LEAD_STATE_FILE="$tmp/wf.json"
unset LEAD_TEST_INBOX_KEYS || true
unset LEAD_TEST_TTY || true
mkdir -p "$HOME"

mkdir -p "$tmp/bin"
export AGY_ARGS_LOG="$tmp/agy-args.log"
cat > "$tmp/bin/agy" <<'EOF'
#!/bin/sh
printf 'AGYLOG %s\n' "$(printf '%s' "$*" | tr '\n' ' ')" >> "$AGY_ARGS_LOG"
input=$(cat)
printf 'AGYSTDIN %s\n' "$input" >> "$AGY_ARGS_LOG"
EOF
chmod +x "$tmp/bin/agy"

CGO_ENABLED=0 go build -o "$tmp/lead" ./cmd/lead || fail "go build failed"

# --- Scenario A: `lead bootstrap --dry-run` in empty directory prints command and goal ---
dir_a="$tmp/empty-a"; mkdir -p "$dir_a"
out_a="$( cd "$dir_a" && "$tmp/lead" bootstrap --dry-run "demo todo app" )" \
  || fail "scenario A: bootstrap --dry-run failed"
case "$out_a" in
  *"demo todo app"*) ;;
  *) fail "scenario A: dry-run missing goal: $out_a" ;;
esac
case "$out_a" in
  *"agy -i"*) ;;
  *) fail "scenario A: dry-run missing interactive command preview: $out_a" ;;
esac

# --- Scenario B: `lead bootstrap` launches interactive agent with prompt and stdin ---
dir_b="$tmp/empty-b"; mkdir -p "$dir_b"
: > "$AGY_ARGS_LOG"
( cd "$dir_b" && printf 'user-stdin-line\n' | PATH="$tmp/bin:$REAL_PATH" "$tmp/lead" bootstrap "hello bootstrap" >/dev/null ) \
  || fail "scenario B: lead bootstrap failed"
grep -q "AGYLOG -i " "$AGY_ARGS_LOG" || fail "scenario B: agy was not launched with -i: $(cat "$AGY_ARGS_LOG")"
grep -q "hello bootstrap" "$AGY_ARGS_LOG" || fail "scenario B: prompt missing goal"
grep -q "AGYSTDIN user-stdin-line" "$AGY_ARGS_LOG" || fail "scenario B: stdin not forwarded to interactive agent"

# --- Scenario C: bare `lead` in interactive TTY + uninitialized dir prompts [Y/n] and launches bootstrap ---
dir_c="$tmp/empty-c"; mkdir -p "$dir_c"
: > "$AGY_ARGS_LOG"
out_c="$( cd "$dir_c" && printf 'y\n' | LEAD_TEST_TTY=1 PATH="$tmp/bin:$REAL_PATH" "$tmp/lead" 2>&1 )" \
  || fail "scenario C: interactive bare lead in uninitialized dir failed: $out_c"
grep -q "AGYLOG -i " "$AGY_ARGS_LOG" || fail "scenario C: declining/missing bootstrap launch on 'y': $out_c"

# --- Scenario D: bare `lead` in git repo without GitHub origin, user answers 'n' -> does not launch ---
dir_d="$tmp/git-no-origin"; git init -q -b main "$dir_d"
: > "$AGY_ARGS_LOG"
out_d="$( cd "$dir_d" && printf 'n\n' | LEAD_TEST_TTY=1 PATH="$tmp/bin:$REAL_PATH" "$tmp/lead" 2>&1 )" \
  || fail "scenario D: declining bootstrap exited non-zero: $out_d"
[ ! -s "$AGY_ARGS_LOG" ] || fail "scenario D: agy launched despite 'n' answer"
case "$out_d" in
  *"Cancelled. Run \`lead bootstrap\`"*) ;;
  *) fail "scenario D: missing cancellation guidance: $out_d" ;;
esac

# --- Scenario E: credentialed GitHub origin is redacted and subdir launches at repo root ---
dir_e="$tmp/cred-repo"; git init -q -b main "$dir_e"
git -C "$dir_e" remote add origin "https://x-access-token:ghp_topsecret123@github.com/kuwa72/demo-app.git"
mkdir -p "$dir_e/sub/pkg"
out_e="$( cd "$dir_e/sub/pkg" && "$tmp/lead" bootstrap --dry-run "verify redaction" )" \
  || fail "scenario E: bootstrap --dry-run in subdir failed"
case "$out_e" in
  *"kuwa72/demo-app"*) ;;
  *) fail "scenario E: missing redacted repo slug: $out_e" ;;
esac
case "$out_e" in
  *"ghp_topsecret123"*|*"x-access-token"*)
    fail "scenario E: credential leaked in bootstrap output: $out_e" ;;
esac
case "$out_e" in
  *"from $dir_e "*) ;;
  *"from $(cd "$dir_e" && pwd -P) "*) ;;
  *) fail "scenario E: expected resolved repo root in prompt, got: $out_e" ;;
esac

# --- Scenario F: bare `lead` without TTY in empty dir still fails with help hint ---
if ( cd "$dir_a" && "$tmp/lead" </dev/null >/dev/null 2>&1 ); then
  fail "scenario E: bare lead without TTY unexpectedly succeeded"
fi

[ "$HOME" = "$tmp/home" ] || fail "HOME isolation broken"

echo "bootstrap behavioral checks passed"
