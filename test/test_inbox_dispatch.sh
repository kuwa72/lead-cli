#!/usr/bin/env bash
# issue #105: while the inbox is open it runs a foreground dispatch loop.
# Tests that a bare `lead` run with --parallel dispatches a ready issue,
# creates a worktree, passes the expected argv to the agent, and records an
# in-progress workflow while the inbox stays open.
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
unset XDG_STATE_HOME HERDR_ENV LEAD_TEST_INBOX_KEYS || true
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
: > "$GH_LOG"; : > "$AGENT_LOG"

export PROTECTED=1

mkdir -p "$tmp/bin"
cat > "$tmp/bin/gh" <<'EOF'
#!/bin/sh
echo "GH $*" >> "$GH_LOG"
case "$1 $2" in
  "issue list")
    case "$*" in
      *"--label ready"*) sleep 0.05; printf '[{"number":7,"title":"dispatch me"}]' ;;
      *) sleep 0.05; printf '[]' ;;
    esac ;;
  "issue view")
    printf '{"number":7,"title":"dispatch me","body":"do the thing","state":"OPEN"}' ;;
  "issue edit"|"issue comment") : ;;
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
cat > "$tmp/bin/agy" <<'EOF'
#!/bin/sh
for a in "$@"; do printf '<%s>\n' "$a" >> "$AGENT_LOG"; done
printf 'CWD=%s\n' "$(pwd)" >> "$AGENT_LOG"
sleep 0.2
EOF
chmod +x "$tmp/bin/gh" "$tmp/bin/agy"
export PATH="$tmp/bin:$PATH"

CGO_ENABLED=0 go build -o "$tmp/lead" ./cmd/lead || fail "go build failed"

cd "$repo"
out="$(LEAD_TEST_INBOX_KEYS='R,R,R,q' "$tmp/lead" --parallel 3 2>&1)" || fail "headless inbox dispatch exited non-zero: $out"

# The dispatcher must have listed ready issues.
grep -q 'issue list --state open --label ready' "$GH_LOG" || fail "ready issue list missing: $(cat "$GH_LOG")"

# A worktree for #7 must exist after dispatch (and not be cleaned because the
# issue is still OPEN and the loop is cancelled before the agent settles).
wt="$repo/.worktrees/issue-7"
[ -d "$wt" ] || fail "worktree not created: $wt"

# The agent must have been launched with the expected argv in the worktree.
grep -qxF '<--dangerously-skip-permissions>' "$AGENT_LOG" || fail "agent argv missing --dangerously-skip-permissions: $(cat "$AGENT_LOG")"
grep -q 'Issue #7: dispatch me' "$AGENT_LOG" || fail "agent argv missing issue header: $(cat "$AGENT_LOG")"
grep -q 'do the thing' "$AGENT_LOG" || fail "agent argv missing issue body: $(cat "$AGENT_LOG")"
grep -qxF "CWD=$wt" "$AGENT_LOG" || fail "agent cwd was not the worktree: $(cat "$AGENT_LOG")"

# workflows.json must contain an in-progress record for #7.
python3 - "$LEAD_STATE_FILE" <<'PY' || fail "workflows.json missing in-progress #7: $(cat "$LEAD_STATE_FILE")"
import json, sys
with open(sys.argv[1]) as f:
    data = json.load(f)
for w in data.get('workflows', []):
    if w.get('issue') == 7 and w.get('status') == 'in_progress' and w.get('worktree'):
        raise SystemExit(0)
raise SystemExit(1)
PY

# The inbox header must reflect the dispatcher state and --parallel limit.
case "$out" in *"実行中 1 / 並列上限 3"*) ;; *) fail "inbox header missing running=1 / parallel=3: $out";; esac

echo "inbox dispatch tests passed"
