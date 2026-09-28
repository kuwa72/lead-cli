#!/usr/bin/env bash
# test_worktree_agent_dir.sh: verifies agent launch directory and output details (issue #72).
# Asserts working directory, issue number, and agent name are displayed on launch,
# and that the printed `cd <dir> && <agent>` command targets the right directory.
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
if [ -d "/home/linuxbrew/.linuxbrew/bin" ]; then
  export PATH="/home/linuxbrew/.linuxbrew/bin:$PATH"
fi
unset XDG_STATE_HOME || true
unset LEAD_STATE_FILE || true
mkdir -p "$HOME"

repo="$tmp/repo"
git init -q -b main "$repo" || fail "git init failed"
git -C "$repo" config user.email "t@t"
git -C "$repo" config user.name "t"
echo "root agents instructions" > "$repo/AGENTS.md"
git -C "$repo" add .
git -C "$repo" commit -qm init || fail "fixture commit failed"

mkdir -p "$tmp/bin"
cat > "$tmp/bin/gh" <<'EOF'
#!/bin/sh
if [ "$1 $2" = "issue view" ]; then
  num="$3"
  printf '{"number":%s,"title":"issue-%s title","body":"issue body","state":"OPEN"}' "$num" "$num"
else
  echo "unexpected gh call: $@" >&2; exit 3
fi
EOF
chmod +x "$tmp/bin/gh"

export PATH="$tmp/bin:$PATH"

CGO_ENABLED=0 go build -o "$tmp/lead" ./cmd/lead || fail "go build failed"
cd "$repo"

# 1. lead run 72 --worktree --agent codex
out_codex=$("$tmp/lead" run 72 --worktree --agent codex)

# Verify displayed output includes issue number, worktree dir, and agent name
echo "$out_codex" | grep -F "Issue #72" > /dev/null || fail "output missing Issue #72: $out_codex"
echo "$out_codex" | grep -F "Worktree: $repo/.worktrees/issue-72" > /dev/null || fail "output missing Worktree dir: $out_codex"
echo "$out_codex" | grep -F "Agent: codex" > /dev/null || fail "output missing Agent: codex: $out_codex"

# Verify worktree directory exists on disk
[ -d "$repo/.worktrees/issue-72" ] || fail "worktree dir was not created"

# Verify the printed command changes directory to the worktree
echo "$out_codex" | grep -F "cd \"$repo/.worktrees/issue-72\" && codex" > /dev/null \
  || fail "output lacks cd to worktree for codex: $out_codex"

# 2. lead run 73 without --worktree --agent claude: runs in repo root
out_claude=$("$tmp/lead" run 73 --agent claude)

# Verify displayed output includes issue number, repo root as Dir, and agent name
echo "$out_claude" | grep -F "Issue #73" > /dev/null || fail "output missing Issue #73: $out_claude"
echo "$out_claude" | grep -F "Dir: $repo" > /dev/null || fail "output missing Dir: $repo: $out_claude"
echo "$out_claude" | grep -F "Agent: claude" > /dev/null || fail "output missing Agent: claude: $out_claude"

# Verify the printed command runs in repo root
echo "$out_claude" | grep -F "cd \"$repo\" && claude" > /dev/null \
  || fail "output lacks cd to repo root for claude: $out_claude"

# 3. Verify all other agents in worktree
for agent_name in agy devin opencode gemini; do
  issue_num=$((100 + RANDOM % 800))
  out_agent=$("$tmp/lead" run "$issue_num" --worktree --agent "$agent_name")
  echo "$out_agent" | grep -F "Issue #$issue_num" > /dev/null || fail "output missing Issue #$issue_num"
  echo "$out_agent" | grep -F "Worktree: $repo/.worktrees/issue-$issue_num" > /dev/null || fail "output missing worktree for $agent_name"
  echo "$out_agent" | grep -F "Agent: $agent_name" > /dev/null || fail "output missing Agent: $agent_name"
  echo "$out_agent" | grep -F "cd \"$repo/.worktrees/issue-$issue_num\"" > /dev/null \
    || fail "output lacks cd to worktree for $agent_name: $out_agent"
  echo "$out_agent" | grep -F "$agent_name" > /dev/null \
    || fail "output lacks $agent_name command: $out_agent"
done

echo "PASS: worktree agent dir behavioral checks passed"
