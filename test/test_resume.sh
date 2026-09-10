#!/usr/bin/env bash
# Issue #74: lead resume behavioral tests.
# Verifies resuming from Issue number, Branch name, and PR number,
# ensuring no new branch is created and --repair handles damaged state.
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
unset LEAD_STATE_FILE || true
unset HERDR_ENV || true
mkdir -p "$HOME"
command -v git >/dev/null || fail "git not available"

repo="$tmp/repo"
git init -q -b main "$repo" || fail "git init failed"
git -C "$repo" config user.email "t@t"
git -C "$repo" config user.name "t"
echo x > "$repo/f.txt"
git -C "$repo" add .
git -C "$repo" commit -qm init || fail "fixture commit failed"

# Create branch for issue 36
git -C "$repo" branch "issue/36-ports-adapter"

mkdir -p "$tmp/bin"
cat > "$tmp/bin/gh" <<'EOF'
#!/bin/sh
case "$1 $2" in
  "issue view")
    case "$3" in
      36) printf '{"number":36,"title":"ports adapter","body":"b","state":"OPEN"}' ;;
      *) echo "issue not found: $3" >&2; exit 1 ;;
    esac
    ;;
  "pr view")
    case "$3" in
      42) printf '{"number":42,"state":"OPEN","mergeable":"MERGEABLE","mergeStateStatus":"CLEAN","headRefName":"issue/36-ports-adapter"}' ;;
      *) echo "pr not found: $3" >&2; exit 1 ;;
    esac
    ;;
  *)
    echo "unexpected gh call: $@" >&2; exit 3
    ;;
esac
EOF
chmod +x "$tmp/bin/gh"

cat > "$tmp/bin/herdr" <<'EOF'
#!/bin/sh
case "$1 $2" in
  "pane split") printf '{"result":{"pane":{"pane_id":"test-pane"}}}' ;;
  "pane send-text") : ;;
  *) exit 0 ;;
esac
EOF
chmod +x "$tmp/bin/herdr"
export PATH="$tmp/bin:$PATH"

CGO_ENABLED=0 go build -o "$tmp/lead" ./cmd/lead || fail "go build failed"
cd "$repo"

STATE="$HOME/.local/state/lead/workflows.json"

# Record initial branches count
branches_before="$(git branch --format='%(refname:short)' | sort)"

# 1. Resume by Issue number
"$tmp/lead" resume 36 --worktree || fail "lead resume 36 failed"
[ -f "$repo/.worktrees/issue-36/f.txt" ] || fail "worktree not checked out on resume"
[ -f "$STATE" ] || fail "state file not created"
python3 -c "import json;d=json.load(open('$STATE'));assert len(d['workflows'])==1 and d['workflows'][0]['issue']==36 and d['workflows'][0]['branch']=='issue/36-ports-adapter', d" \
  || fail "state record invalid after resume by issue"

# Verify lead status shows resumption instruction
status_out="$("$tmp/lead" status)" || fail "lead status failed"
case "$status_out" in
  *"resume with lead resume 36"*) ;;
  *) fail "status missing resumption instruction: $status_out" ;;
esac

# 2. Resume by Branch name
"$tmp/lead" resume "issue/36-ports-adapter" || fail "lead resume by branch name failed"
python3 -c "import json;d=json.load(open('$STATE'));assert len(d['workflows'])==1 and d['workflows'][0]['issue']==36, d" \
  || fail "state record invalid after resume by branch"

# 3. Resume by PR number
"$tmp/lead" resume 42 || fail "lead resume by PR number failed"
python3 -c "import json;d=json.load(open('$STATE'));assert len(d['workflows'])==1 and d['workflows'][0]['branch']=='issue/36-ports-adapter', d" \
  || fail "state record invalid after resume by PR"

# 4. Verify no new branch was created
branches_after="$(git branch --format='%(refname:short)' | sort)"
[ "$branches_before" = "$branches_after" ] || fail "new branch was created: before=$branches_before after=$branches_after"

# Non-existent target must fail safely and not create branches
"$tmp/lead" resume 999 >/dev/null 2>&1 && fail "resume non-existent issue should fail"
branches_still="$(git branch --format='%(refname:short)' | sort)"
[ "$branches_before" = "$branches_still" ] || fail "branch created during failed resume"

# 5. Damaged state file test and --repair
echo '{broken' > "$STATE"
"$tmp/lead" resume 36 >/dev/null 2>&1 && fail "resume on corrupt file without --repair must fail"
[ "$(cat "$STATE")" = '{broken' ] || fail "corrupt state file was overwritten without --repair"

"$tmp/lead" resume 36 --repair || fail "lead resume 36 --repair failed on damaged state"
python3 -c "import json;d=json.load(open('$STATE'));assert len(d['workflows'])==1 and d['workflows'][0]['issue']==36, d" \
  || fail "state not repaired after --repair"

echo "PASS: resume behavioral tests passed"
