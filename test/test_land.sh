#!/usr/bin/env bash
# issue #298: `lead land` は review → finish の順に実行し、review が失敗したら
# finish に進まない。LEAD_LAND_BIN でダミー CLI を差し込み、引数と順序を検証する。
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
unset LEAD_STATE_FILE XDG_STATE_HOME || true
mkdir -p "$HOME"

repo="$tmp/repo"
git init -q -b main "$repo"
git -C "$repo" config user.email "t@t"
git -C "$repo" config user.name "t"
git -C "$repo" remote add origin git@github.com:o/r.git
echo x > "$repo/f.txt"
git -C "$repo" add . && git -C "$repo" commit -qm init

export LAND_LOG="$tmp/land.log"
export LEAD_LAND_SKIP_CI=1
: > "$LAND_LOG"
mkdir -p "$tmp/bin"
cat > "$tmp/bin/fake-lead" <<'EOF'
#!/bin/sh
printf '%s\n' "$*" >> "$LAND_LOG"
if [ "$1" = "review" ] && [ "${LAND_REVIEW_FAIL:-}" = "1" ]; then
  exit 1
fi
exit 0
EOF
chmod +x "$tmp/bin/fake-lead"

CGO_ENABLED=0 go build -o "$tmp/lead" ./cmd/lead || fail "go build failed"
cd "$repo"
export LEAD_LAND_BIN="$tmp/bin/fake-lead"

# --- 1. review passes: review then finish -----------------------------------
out="$("$tmp/lead" land 42 --pr 7 2>&1)" || fail "land failed: $out"
expected=$'review 7 --issue 42\nfinish 42 --merge --pr 7'
[ "$(cat "$LAND_LOG")" = "$expected" ] || fail "land order/args wrong: $(cat "$LAND_LOG")"

# --- 2. review fails: finish is not called ---------------------------------
: > "$LAND_LOG"
set +e
out="$(LAND_REVIEW_FAIL=1 "$tmp/lead" land 42 --pr 7 2>&1)"
status=$?
set -e
[ "$status" -eq 1 ] || fail "review failure exit = $status, want 1: $out"
[ "$(cat "$LAND_LOG")" = "review 7 --issue 42" ] || fail "finish ran after a failed review: $(cat "$LAND_LOG")"

# --- 3. negative --pr is rejected before any subprocess --------------------
: > "$LAND_LOG"
set +e
out="$("$tmp/lead" land 42 --pr -1 2>&1)"
status=$?
set -e
[ "$status" -eq 1 ] || fail "negative --pr exit = $status, want 1: $out"
[ -s "$LAND_LOG" ] && fail "negative --pr ran a subprocess: $(cat "$LAND_LOG")"

echo "land behavioral checks passed"
