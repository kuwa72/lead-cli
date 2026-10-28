#!/usr/bin/env bash
# issue #273: a skipped CI check must not count as a pass.
# Dummy gh returns bucket=skipping; finish must stop before merging.
# A pass-only run still merges (the happy path is unchanged).
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

fail() { echo "FAIL: $*" >&2; exit 1; }

REAL_GOPATH="$(go env GOPATH)"
REAL_GOCACHE="$(go env GOCACHE)"
export HOME="$tmp/home"
export GOPATH="$REAL_GOPATH" GOCACHE="$REAL_GOCACHE"
unset XDG_STATE_HOME || true
mkdir -p "$HOME"
command -v git >/dev/null || fail "git not available"

mkdir -p "$tmp/bin"
cat > "$tmp/bin/gh" <<'EOF'
#!/bin/sh
echo "GHLOG $@" >> "$GH_ARGS_LOG"
case "$1 $2" in
  "issue view")
    printf '{"number":36,"title":"demo","body":"b","state":"OPEN","labels":[{"name":"risk:a"}]}' ;;
  "pr view")
    case "$*" in
      *"--json labels"*) printf 'review:pass\n' ;;
      *"--json headRefOid"*) printf '{"headRefOid":"0123456789abcdef0123456789abcdef01234567"}' ;;
      *"--json comments"*) printf '{"comments":[{"author":{"login":"lead"},"body":"<!-- lead-review sha=0123456789abcdef0123456789abcdef01234567 verdict=pass -->","createdAt":"2026-10-25T00:00:00Z"}]}' ;;
      *"--json files"*) printf 'src/main.go\n' ;;
      *) printf '{"number":7,"state":"OPEN","mergeable":"MERGEABLE","mergeStateStatus":"CLEAN"}' ;;
    esac ;;
  "pr checks")
    case "${GH_MODE:-pass}" in
      skip) printf '[{"bucket":"skipping","name":"test","state":"SKIPPED"}]' ;;
      *)    printf '[{"bucket":"pass","name":"test","state":"SUCCESS"}]' ;;
    esac ;;
  "pr merge")
    printf '{"number":7,"state":"MERGED"}' ;;
  "api user") printf 'lead\n' ;;
  "issue close" | "issue comment")
    : ;;
  *)
    if [ "$1" = "api" ]; then printf 'true'; else echo "unexpected gh call: $@" >&2; exit 3; fi ;;
esac
EOF
chmod +x "$tmp/bin/gh"
export PATH="$tmp/bin:$PATH"
export GH_ARGS_LOG="$tmp/gh-args.log"

CGO_ENABLED=0 go build -o "$tmp/lead" ./cmd/lead || fail "go build failed"

mk_repo() {
  local repo="$1"
  git init -q -b main "$repo"
  git -C "$repo" config user.email "t@t"
  git -C "$repo" config user.name "t"
  echo x > "$repo/f.txt"
  git -C "$repo" add .
  git -C "$repo" commit -qm init
}

# --- Scenario A: skipped check stops before merge ---
repo_a="$tmp/repo-a"; mk_repo "$repo_a"
export LEAD_STATE_FILE="$tmp/state-a.json" GH_MODE=skip
: > "$GH_ARGS_LOG"
cd "$repo_a"
"$tmp/lead" run 36 >/dev/null || fail "scenario A: work failed"
out="$("$tmp/lead" finish 36 --pr 7 --merge --timeout 30s --poll-interval 1s 2>&1)" \
  && fail "scenario A: finish on a skipped check exited 0"
case "$out" in *skipped*) ;; *) fail "scenario A: message must name the skipped check: $out";; esac
grep -q "GHLOG pr merge" "$GH_ARGS_LOG" && fail "scenario A: merged despite a skipped check"
python3 -c "import json;d=json.load(open('$LEAD_STATE_FILE'));assert d['workflows'][0]['status']=='in_progress',d" \
  || fail "scenario A: state touched despite a skipped check"

# --- Scenario B: pass-only still merges ---
repo_b="$tmp/repo-b"; mk_repo "$repo_b"
export LEAD_STATE_FILE="$tmp/state-b.json" GH_MODE=pass
: > "$GH_ARGS_LOG"
cd "$repo_b"
"$tmp/lead" run 36 >/dev/null || fail "scenario B: work failed"
"$tmp/lead" finish 36 --pr 7 --merge --timeout 30s --poll-interval 1s >/dev/null \
  || fail "scenario B: finish on a passing check failed"
grep -q "GHLOG pr merge 7 --squash --delete-branch --match-head-commit 0123456789abcdef0123456789abcdef01234567" "$GH_ARGS_LOG" \
  || fail "scenario B: merge not invoked on a pass-only run"

[ "$HOME" = "$tmp/home" ] || fail "HOME isolation broken"

echo "finish checks behavioral checks passed"
