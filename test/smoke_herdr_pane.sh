#!/usr/bin/env bash
# issue #96: 実 herdr での実機確認（opt-in・直列）。
# `test_*.sh` グロブに入れず、run-tests.sh からは実行されない。
# HERDR_ENV=1 かつ herdr がある端末で手動実行する:
#   ./test/smoke_herdr_pane.sh
# シナリオごとに「1 ペイン開く → 検証 → herdr pane close で閉じる」を直列に行い、
# 終了時に herdr pane list が実行前と一致することを確認する。
# gh とエージェントはダミー（PATH 先頭）。herdr だけ実物を使う。
set -euo pipefail

if [ "${HERDR_ENV:-}" != "1" ] || ! command -v herdr >/dev/null 2>&1; then
  echo "skip: needs HERDR_ENV=1 and herdr on PATH"
  exit 0
fi

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

tmp="$(mktemp -d)"
opened=""
cleanup() {
  for p in $opened; do herdr pane close "$p" >/dev/null 2>&1 || true; done
  rm -rf "$tmp"
}
trap cleanup EXIT
fail() { echo "FAIL: $*" >&2; exit 1; }

pane_ids() { herdr pane list 2>/dev/null | grep -oE 'w[A-Za-z0-9]+:p[A-Za-z0-9]+' | sort -u; }
panes_before="$(pane_ids)"

REAL_GOPATH="$(go env GOPATH)"
REAL_GOCACHE="$(go env GOCACHE)"
export HOME="$tmp/home"
export GOPATH="$REAL_GOPATH" GOCACHE="$REAL_GOCACHE"
unset XDG_STATE_HOME || true
export LEAD_STATE_FILE="$tmp/state/workflows.json"
mkdir -p "$HOME"

repo="$tmp/repo"
git init -q -b main "$repo"
git -C "$repo" config user.email "t@t"
git -C "$repo" config user.name "t"
echo x > "$repo/f.txt"
git -C "$repo" add . && git -C "$repo" commit -qm init

mkdir -p "$tmp/bin"
cat > "$tmp/bin/gh" <<'EOF'
#!/bin/sh
if [ "$1 $2" = "issue view" ]; then
  printf '{"number":36,"title":"smoke pane","body":"b","state":"OPEN"}'
else
  echo "unexpected gh call: $*" >&2; exit 3
fi
EOF
printf '#!/bin/sh\nexit 0\n' > "$tmp/bin/claude"
chmod +x "$tmp/bin/gh" "$tmp/bin/claude"
export PATH="$tmp/bin:$PATH"

CGO_ENABLED=0 go build -o "$tmp/lead" ./cmd/lead || fail "go build failed"

scenario() {
  local name="$1"; shift
  echo "== $name"
  rm -rf "$tmp/state"
  (cd "$repo" && "$tmp/lead" work 36 --agent claude "$@" >/dev/null) || fail "$name: lead work failed"
  local pane
  pane="$(grep -o '"pane": "[^"]*"' "$LEAD_STATE_FILE" | head -1 | cut -d'"' -f4)"
  [ -n "$pane" ] || fail "$name: no pane recorded in state"
  opened="$opened $pane"
  pane_ids | grep -qx "$pane" || fail "$name: pane $pane not listed by herdr"
  echo "   opened $pane"
  herdr pane close "$pane" >/dev/null || fail "$name: herdr pane close $pane failed"
  sleep 0.5
  pane_ids | grep -qx "$pane" && fail "$name: pane $pane still open after close"
  opened="${opened// $pane/}"
  echo "   closed $pane"
  # reset the fixture repo for the next scenario
  (cd "$repo" && "$tmp/lead" clean 36 >/dev/null 2>&1) || true
  git -C "$repo" switch -q main 2>/dev/null || true
  git -C "$repo" branch -D issue/36-smoke-pane >/dev/null 2>&1 || true
}

scenario "branch in place"
scenario "worktree" --worktree

panes_after="$(pane_ids)"
[ "$panes_before" = "$panes_after" ] || fail "pane list changed:
before: $panes_before
after:  $panes_after"

echo "herdr pane smoke passed (all panes closed)"
