#!/usr/bin/env bash
# issue #262: リポジトリ単位の dispatch ロック。
# 1つの dispatch がエージェント実行中に、同じリポジトリで
#   - 2つ目の `lead dispatch --once` は exit 1 + 「既に実行中」で起動しない
#   - inbox は TUI を開くが dispatch を実行せず警告する
#   - `lead status` はロック保持者を表示する
# 1つ目が終了した後は dispatch でき、stale なロックファイルが残っていても
# （保持プロセスが死んでいる）起動できることをアサートする。
# grep によるソース検査はしない（振る舞いのみ）。
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
unset XDG_STATE_HOME LEAD_TEST_INBOX_KEYS || true
export LEAD_NOTIFY=0
export LEAD_STATE_FILE="$tmp/state/workflows.json"
mkdir -p "$HOME"

repo="$tmp/repo"
git init -q -b main "$repo" || fail "git init failed"
git -C "$repo" config user.email "t@t"
git -C "$repo" config user.name "t"
echo x > "$repo/f.txt"
git -C "$repo" add . && git -C "$repo" commit -qm init
git -C "$repo" remote add origin https://github.com/o/r.git

export GH_LOG="$tmp/gh.log" AGENT_LOG="$tmp/agent.log"
export CLOSED_MARK="$tmp/closed" RELEASE="$tmp/release"
: > "$GH_LOG"; : > "$AGENT_LOG"

mkdir -p "$tmp/bin"
cat > "$tmp/bin/gh" <<'EOF'
#!/bin/sh
echo "GH $*" >> "$GH_LOG"
case "$1 $2" in
  "issue list")
    case "$*" in *"--label ready"*) printf '[{"number":7,"title":"dispatch me"}]' ;; *) printf '[]' ;; esac ;;
  "issue view")
    if [ -f "$CLOSED_MARK" ]; then st=CLOSED; else st=OPEN; fi
    printf '{"number":7,"title":"dispatch me","body":"do the thing","state":"%s"}' "$st" ;;
  "issue edit"|"issue comment"|"auth status") : ;;
  "api user") printf 'octocat\n' ;;
  "api repos/o/r")
    case "$*" in
      *default_branch*) printf 'main\n' ;;
      *allow_auto_merge*) printf 'true\n' ;;
      *) echo "unexpected gh api jq: $*" >&2; exit 3 ;;
    esac ;;
  "api repos/o/r/branches/main/protection")
    printf '{"required_status_checks":{"contexts":["test"]},"required_pull_request_reviews":{}}' ;;
  "api repos/o/r/rules/branches/main") printf '[]' ;;
  *) echo "unexpected gh call: $*" >&2; exit 3 ;;
esac
EOF
# Long-running agent for the lock holder: it stays alive until $RELEASE
# appears (bounded so a Red run cannot hang the suite).
cat > "$tmp/bin/claude" <<'EOF'
#!/bin/sh
for a in "$@"; do printf '<%s>\n' "$a" >> "$AGENT_LOG"; done
printf 'CWD=%s\n' "$(pwd)" >> "$AGENT_LOG"
i=0
while [ ! -f "$RELEASE" ] && [ "$i" -lt 200 ]; do sleep 0.1; i=$((i+1)); done
touch "$CLOSED_MARK"
EOF
# Inbox default agent (agy): only used if the lock guard fails.
cat > "$tmp/bin/agy" <<'EOF'
#!/bin/sh
for a in "$@"; do printf '<%s>\n' "$a" >> "$AGENT_LOG"; done
printf 'CWD=%s\n' "$(pwd)" >> "$AGENT_LOG"
EOF
chmod +x "$tmp/bin/gh" "$tmp/bin/claude" "$tmp/bin/agy"
export PATH="$tmp/bin:$PATH"

CGO_ENABLED=0 go build -o "$tmp/lead" ./cmd/lead || fail "go build failed"
ln -sf "$tmp/lead" "$tmp/bin/lead"

launches() { grep -c '^CWD=' "$AGENT_LOG" || true; }

# --- 1. first dispatch holds the lock; a second is refused -------------------
rm -f "$CLOSED_MARK" "$RELEASE"; : > "$GH_LOG"; : > "$AGENT_LOG"
( cd "$repo" && exec "$tmp/lead" dispatch --once --parallel 1 --agent claude ) > "$tmp/first.out" 2>&1 &
first=$!
for _ in $(seq 1 100); do
  [ "$(launches)" -ge 1 ] && break
  sleep 0.1
done
[ "$(launches)" -ge 1 ] || fail "first dispatch did not launch the agent: $(cat "$tmp/first.out")"

set +e
second_out="$(cd "$repo" && timeout 15 "$tmp/lead" dispatch --once --parallel 1 --agent claude 2>&1)"
second_rc=$?
set -e
[ "$second_rc" -eq 1 ] || fail "second dispatch exit = $second_rc, want 1 (already running): $second_out"
case "$second_out" in
  *"already dispatching"*) ;;
  *) fail "second dispatch lacks 'already dispatching': $second_out" ;;
esac
[ "$(launches)" -eq 1 ] || fail "second dispatch launched an agent while the lock was held ($(launches) launches)"

# `lead status` reports the holder.
status_out="$(cd "$repo" && "$tmp/lead" status 2>&1)" || fail "status failed: $status_out"
case "$status_out" in
  *"Dispatch lock: held by pid $first"*) ;;
  *) fail "status does not report lock holder pid $first: $status_out" ;;
esac

# --- 2. inbox opens but disables dispatch while the lock is held -------------
before="$(launches)"
inbox_out="$(cd "$repo" && LEAD_TEST_INBOX_KEYS='R,R,R,q' timeout 20 "$tmp/lead" --parallel 1 2>&1)" \
  || fail "headless inbox exited non-zero while lock held: $inbox_out"
case "$inbox_out" in
  *"already dispatching"*) ;;
  *) fail "inbox lacks the dispatch-disabled warning: $inbox_out" ;;
esac
case "$inbox_out" in
  *"Log"*) ;;
  *) fail "inbox TUI did not render: $inbox_out" ;;
esac
[ "$(launches)" -eq "$before" ] || fail "inbox dispatched while the lock was held ($(launches) launches, want $before)"

# --- 3. the lock is released when the first dispatch exits -------------------
touch "$RELEASE"
wait "$first" || fail "first dispatch failed: $(cat "$tmp/first.out")"
[ -d "$repo/.worktrees/issue-7" ] && fail "first dispatch left its worktree behind"

rm -f "$CLOSED_MARK"; : > "$AGENT_LOG"
third_out="$(cd "$repo" && timeout 15 "$tmp/lead" dispatch --once --parallel 1 --agent claude 2>&1)" \
  || fail "dispatch after the lock was released failed: $third_out"
[ "$(launches)" -eq 1 ] || fail "dispatch after release launched $(launches) agents, want 1"
case "$third_out" in
  *"#7"*"completed"*) ;;
  *) fail "dispatch after release did not complete: $third_out" ;;
esac

# --- 4. a stale lock file (holder dead) does not block dispatch --------------
# Lock path contract: <state dir>/locks/<repo-key>.lock (o/r -> o-r.lock).
mkdir -p "$tmp/state/locks"
printf '{"pid":999999,"repo":"o/r","started_at":"2020-01-01T00:00:00Z"}\n' > "$tmp/state/locks/o-r.lock"
status_out="$(cd "$repo" && "$tmp/lead" status 2>&1)" || fail "status with stale lock failed: $status_out"
case "$status_out" in
  *"Dispatch lock: free"*) ;;
  *) fail "stale lock reported as held: $status_out" ;;
esac
rm -f "$CLOSED_MARK"; : > "$AGENT_LOG"
stale_out="$(cd "$repo" && timeout 15 "$tmp/lead" dispatch --once --parallel 1 --agent claude 2>&1)" \
  || fail "stale lock file blocked dispatch: $stale_out"
[ "$(launches)" -eq 1 ] || fail "stale-lock dispatch launched $(launches) agents, want 1"

echo "dispatch lock tests passed"
