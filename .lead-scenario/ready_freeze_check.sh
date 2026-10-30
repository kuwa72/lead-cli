#!/usr/bin/env bash
# Behaviour check for step 1: `lead ready 42` freezes the issue body's
# scenario at <dir of $LEAD_STATE_FILE>/scenarios/o_r/42.md.
# Uses a throwaway HOME, repo and dummy gh; touches nothing in the worktree.
set -euo pipefail

ROOT="$1"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
fail() { echo "CHECK FAIL: $*" >&2; exit 1; }

REAL_GOPATH="$(go env GOPATH)"
REAL_GOCACHE="$(go env GOCACHE)"
export HOME="$tmp/home"
export GOPATH="$REAL_GOPATH" GOCACHE="$REAL_GOCACHE"
unset XDG_STATE_HOME LEAD_TEST_TTY || true
mkdir -p "$HOME"

export LEAD_STATE_FILE="$tmp/state/workflows.json"
mkdir -p "$tmp/state"

repo="$tmp/repo"
git init -q -b main "$repo" || fail "git init failed"
git -C "$repo" config user.email "t@t"
git -C "$repo" config user.name "t"
git -C "$repo" remote add origin git@github.com:o/r.git
echo x > "$repo/f.txt"
git -C "$repo" add . && git -C "$repo" commit -qm init

mkdir -p "$tmp/bin"
cat > "$tmp/bin/gh" <<'EOF'
#!/bin/sh
printf 'GH %s\n' "$*" >> "$GH_LOG"
case "$1 $2" in
  "issue view")
    cat <<'JSON'
{"number":42,"title":"valid","body":"# valid\n\n## 受入条件\n\n- [ ] `./test/run-tests.sh` が pass する\n\n## スコープ外\n\n- none\n\n## シナリオ\n\n1. `lead ready 42` → ready が付く\n2. `go test ./...` が通る","state":"OPEN","labels":[{"name":"needs-review"},{"name":"risk:b"}]}
JSON
    ;;
  "issue edit") ;;
  *) echo "unexpected gh call: $*" >&2; exit 3 ;;
esac
EOF
chmod +x "$tmp/bin/gh"
export GH_LOG="$tmp/gh.log"
: > "$GH_LOG"
export PATH="$tmp/bin:$PATH"

CGO_ENABLED=0 go build -o "$tmp/lead" "$ROOT/cmd/lead" >/dev/null 2>&1 \
  || fail "go build ./cmd/lead failed"

out="$(cd "$repo" && "$tmp/lead" ready 42 2>&1)" || fail "lead ready 42 exited non-zero: $out"

state_dir="$(dirname "$LEAD_STATE_FILE")"
frozen="$state_dir/scenarios/o_r/42.md"
[ -f "$frozen" ] || fail "lead ready 42 did not create $frozen (state dir $state_dir)"
grep -q '^## Steps$' "$frozen" || fail "frozen scenario lacks ## Steps: $(cat "$frozen")"
grep -q 'lead ready 42' "$frozen" || fail "frozen scenario does not carry the issue body steps: $(cat "$frozen")"
case "$(cat "$frozen")" in
  *"go test ./... が通る"*|*'go test ./...'*) ;;
  *) fail "frozen scenario dropped the second step: $(cat "$frozen")";;
esac
grep -qxF 'GH issue edit 42 --add-label ready' "$GH_LOG" || fail "ready label not added: $(cat "$GH_LOG")"
[ ! -e "$repo/scenarios" ] || fail "scenario was written inside the worktree"
echo "CHECK OK: frozen at $frozen"
