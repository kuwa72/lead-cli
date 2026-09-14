#!/usr/bin/env bash
# issue #185: `lead stop <n>` terminates the running agent (kill process
# group, remove worktree, comment, delete state). The ready label stays so
# the issue returns to the dispatch queue.
# Behavioral checks only: exit statuses, outputs, artifacts, gh argv.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"; kill "$agent_pid" 2>/dev/null || true' EXIT

fail() { echo "FAIL: $*" >&2; exit 1; }

CGO_ENABLED=0 go build -o "$tmp/lead" ./cmd/lead || fail "go build failed"

export HOME="$tmp/home"
mkdir -p "$HOME"
export LEAD_STATE_FILE="$tmp/state/workflows.json"
mkdir -p "$(dirname "$LEAD_STATE_FILE")"

# --- dummy gh: `issue comment` succeeds and logs argv ---
mkdir -p "$tmp/bin"
cat > "$tmp/bin/gh" <<'EOF'
#!/usr/bin/env bash
printf '<%s>\n' "$@" >> "$GH_ARG_LOG"
if [ "$1" = "issue" ] && [ "$2" = "comment" ]; then
  exit 0
fi
echo "unexpected gh call: $@" >&2
exit 3
EOF
chmod +x "$tmp/bin/gh"
export PATH="$tmp/bin:$PATH"
export GH_ARG_LOG="$tmp/gh-args.log"
: > "$GH_ARG_LOG"

proj="$tmp/project"
mkdir -p "$proj"
cd "$proj"
git init >/dev/null || fail "git init failed"
git config user.email "test@example.com"
git config user.name "Test"
git commit -q --allow-empty -m init || fail "fixture commit failed"

# --- 1. stop kills the agent, removes the worktree, comments, deletes state ---
wt="$proj/.worktrees/issue-7"
git worktree add "$wt" -b issue/7-t >/dev/null 2>&1 || fail "git worktree add failed"
sleep 300 &
agent_pid=$!
cat > "$LEAD_STATE_FILE" <<EOF
{"version":1,"workflows":[{"repository":"local","issue":7,"branch":"issue/7-t","worktree":"$wt","status":"in_progress","agent":"claude","pid":$agent_pid}]}
EOF

out="$("$tmp/lead" stop 7)" || fail "lead stop 7 failed: $out"
case "$out" in *"stopped #7"*) ;; *) fail "stop missing confirmation: $out";; esac
if kill -0 "$agent_pid" 2>/dev/null; then fail "agent process still alive after stop"; fi
[ ! -e "$wt" ] || fail "worktree remains after stop"
case "$(cat "$GH_ARG_LOG")" in *"<issue>"*"<comment>"*"<7>"*) ;; *) fail "gh issue comment #7 was not called";; esac
case "$(cat "$LEAD_STATE_FILE")" in *"\"issue\":7"*) fail "state record remains after stop";; esac

# --- 2. stop without a record fails ---
"$tmp/lead" stop 9 >/dev/null 2>&1 && fail "lead stop without a record succeeded"
"$tmp/lead" stop bogus >/dev/null 2>&1 && fail "lead stop bogus succeeded"

echo "stop agent checks passed"
