#!/usr/bin/env bash
# issue #304: land の実行中に lead status が landing と stage を出し、終了後に消える。
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
export LEAD_STATE_FILE="$tmp/state/workflows.json"
export LEAD_TEST_SKIP_LAND_CI=1
mkdir -p "$HOME"

repo="$tmp/repo"
git init -q -b main "$repo"
git -C "$repo" config user.email "t@t"
git -C "$repo" config user.name "t"
echo x > "$repo/f.txt"
git -C "$repo" add . && git -C "$repo" commit -qm init

mkdir -p "$tmp/bin"
cat > "$tmp/bin/fake-lead" <<'EOF'
#!/bin/sh
if [ "$1" = "review" ]; then
  sleep 2
fi
exit 0
EOF
cat > "$tmp/bin/gh" <<'EOF'
#!/bin/sh
if [ "$1 $2" = "issue view" ]; then
  printf '{"number":42,"title":"land","body":"","state":"CLOSED","labels":[]}'
else
  exit 3
fi
EOF
chmod +x "$tmp/bin/fake-lead" "$tmp/bin/gh"
export PATH="$tmp/bin:$PATH"
export LEAD_LAND_BIN="$tmp/bin/fake-lead"

CGO_ENABLED=0 go build -o "$tmp/lead" ./cmd/lead || fail "go build failed"
cd "$repo"

"$tmp/lead" land 42 --pr 7 >/dev/null 2>&1 &
land_pid=$!
sleep 1
out="$("$tmp/lead" status 2>&1)" || fail "status failed: $out"
case "$out" in *"landing"*"review"*) ;; *) fail "status does not show landing review: $out";; esac
json="$("$tmp/lead" status --json 2>&1)" || fail "status --json failed: $json"
case "$json" in *'"stage": "review"'*) ;; *) fail "status JSON lacks the review stage: $json";; esac
wait "$land_pid" || fail "lead land failed"

out="$("$tmp/lead" status 2>&1)"
case "$out" in *"landing"*) fail "landing state remained after exit: $out";; esac

echo "landing status behavioral checks passed"
