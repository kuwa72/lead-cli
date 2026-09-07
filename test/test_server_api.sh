#!/usr/bin/env bash
# issue #48: 自己外部操作機構の振る舞いテスト。
# 実serverバイナリを背景起動し、外部プロセスからの一覧→選択→
# ディスパッチ→状態取得の一連フローと、ソケット拒否を検証する (grep検査なし)。
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
unset XDG_RUNTIME_DIR || true
export LEAD_STATE_FILE="$tmp/wf.json"
mkdir -p "$HOME"
command -v git >/dev/null || fail "git not available"
[ "$(id -u)" != "0" ] || echo "(running as root: permission-denial check uses modes + refusal only)"

repo="$tmp/repo"
git init -q -b main "$repo"
git -C "$repo" config user.email "t@t"
git -C "$repo" config user.name "t"
echo x > "$repo/f.txt"
git -C "$repo" add .
git -C "$repo" commit -qm init

mkdir -p "$tmp/bin"
cat > "$tmp/bin/gh" <<'EOF'
#!/bin/sh
echo "GHLOG $@" >> "$GH_ARGS_LOG"
case "$1 $2" in
  "issue list")
    printf '[{"number":48,"title":"server api"}]' ;;
  "issue view")
    printf '{"number":48,"title":"server api","body":"b","state":"OPEN"}' ;;
  "pr checks")
    printf '[{"bucket":"pass","name":"test","state":"SUCCESS"}]' ;;
  *) echo "unexpected gh call: $@" >&2; exit 3 ;;
esac
EOF
chmod +x "$tmp/bin/gh"
export PATH="$tmp/bin:$PATH"
export GH_ARGS_LOG="$tmp/gh-args.log"
: > "$GH_ARGS_LOG"

CGO_ENABLED=0 go build -o "$tmp/lead" ./cmd/lead || fail "go build failed"
cd "$repo"

SOCK="$tmp/lead.sock"

# --- schema needs no server ---
"$tmp/lead" api schema | python3 -c \
  "import json,sys; d=json.load(sys.stdin); assert any(o['name']=='work.dispatch' for o in d['ops']), d" \
  || fail "api schema invalid"

# --- start the real server in the background ---
"$tmp/lead" server --socket "$SOCK" >"$tmp/server.log" 2>&1 &
SRV_PID=$!
trap 'kill $SRV_PID 2>/dev/null || true; rm -rf "$tmp"' EXIT
for _ in $(seq 1 50); do
  [ -S "$SOCK" ] && break
  sleep 0.1
done
[ -S "$SOCK" ] || fail "server did not create socket"

# --- socket locked down (owner-only modes) ---
[ "$(stat -c %a "$SOCK")" = "600" ] || fail "socket mode not 600"
[ "$(stat -c %a "$tmp")" = "700" ] || fail "socket dir mode not 700"

# --- external flow: list → dispatch → snapshot → stage → ci.status → notify ---
"$tmp/lead" api call issues.list --socket "$SOCK" | grep -q '"number": 48' \
  || fail "issues.list missing #48"
"$tmp/lead" api call work.dispatch --socket "$SOCK" --args '{"issue":48}' | grep -q "issue/48-server-api" \
  || fail "work.dispatch missing branch"
git -C "$repo" rev-parse --verify --quiet refs/heads/issue/48-server-api >/dev/null \
  || fail "dispatch did not create branch (CLI equivalence)"
"$tmp/lead" api snapshot --socket "$SOCK" | python3 -c \
  "import json,sys; d=json.load(sys.stdin); assert len(d['workflows'])==1 and d['workflows'][0]['issue']==48, d" \
  || fail "snapshot missing dispatched workflow"
"$tmp/lead" api call prompt.stage --socket "$SOCK" --args '{"issue":48,"prompt":"do it"}' | grep -q '"id": "st-1"' \
  || fail "prompt.stage missing staged id"
"$tmp/lead" api snapshot --socket "$SOCK" | python3 -c \
  "import json,sys; d=json.load(sys.stdin); assert len(d['staged'])==1, d" \
  || fail "staged prompt not visible (must wait for review, never auto-send)"
"$tmp/lead" api call ci.status --socket "$SOCK" --args '{"pr":7}' | grep -q '"all_pass": true' \
  || fail "ci.status not passing"
"$tmp/lead" api call notify --socket "$SOCK" --args '{"message":"hi"}' | grep -q '"ok": true' \
  || fail "notify failed"
"$tmp/lead" api call bogus.op --socket "$SOCK" >/dev/null 2>&1 \
  && fail "unknown op exited 0"

# --- unauthorized access refused: locked dir blocks new connections ---
chmod 000 "$tmp"
"$tmp/lead" api snapshot --socket "$SOCK" >/dev/null 2>&1 \
  && { chmod 700 "$tmp"; fail "snapshot through locked dir exited 0"; }
chmod 700 "$tmp"
"$tmp/lead" api snapshot --socket "$SOCK" >/dev/null \
  || fail "snapshot failed after restoring dir perms"

kill $SRV_PID 2>/dev/null || true
wait $SRV_PID 2>/dev/null || true
sleep 0.5

# --- no server: connection refused ---
"$tmp/lead" api snapshot --socket "$SOCK" >/dev/null 2>&1 \
  && fail "snapshot with dead server exited 0"

[ "$HOME" = "$tmp/home" ] || fail "HOME isolation broken"

echo "server api behavioral checks passed"
