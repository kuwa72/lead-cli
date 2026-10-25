#!/usr/bin/env bash
# issue #250: `lead review <pr>` は PR head の一時 worktree でレビュアーを走らせる。
# main のチェックアウトから実行しても、ダミーエージェントが記録した
# `git rev-parse HEAD` が PR head と一致し、終了後に一時 worktree が
# 残らないことを検証する（grep によるソース検査なし・一時 HOME/リポジトリ）。
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
export LEAD_NOTIFY=0
export LEAD_STATE_FILE="$tmp/state/workflows.json"
mkdir -p "$HOME" "$(dirname "$LEAD_STATE_FILE")"

repo="$tmp/repo"
git init -q -b main "$repo" || fail "git init failed"
git -C "$repo" config user.email "t@t"
git -C "$repo" config user.name "t"
echo main > "$repo/f.txt"
git -C "$repo" add . && git -C "$repo" commit -qm main
MAIN_SHA="$(git -C "$repo" rev-parse HEAD)"
git -C "$repo" checkout -q -b issue/250-review-worktree
echo pr-head > "$repo/f.txt"
git -C "$repo" add . && git -C "$repo" commit -qm pr-head
PR_SHA="$(git -C "$repo" rev-parse HEAD)"
export PR_SHA
git -C "$repo" checkout -q main
[ "$MAIN_SHA" != "$PR_SHA" ] || fail "fixture: PR head equals main"
git -C "$repo" remote add origin https://github.com/o/r.git

export GH_LOG="$tmp/gh.log" AGENT_DIR="$tmp/agent-dir.txt" AGENT_HEAD="$tmp/agent-head.txt"
export PROMPT_COPY="$tmp/prompt.md" COMMENT_FILE="$tmp/comment.md" LABELS_FILE="$tmp/labels.txt"
: > "$GH_LOG"; : > "$COMMENT_FILE"; : > "$LABELS_FILE"
export AGENT_REPLY_FILE="$tmp/reply.md"
cat > "$AGENT_REPLY_FILE" <<'EOF'
## 所見
- PR head を確認した

VERDICT: pass
EOF

mkdir -p "$tmp/bin"
cat > "$tmp/bin/gh" <<'EOF'
#!/bin/sh
echo "GH $*" >> "$GH_LOG"
case "$1 $2" in
  "pr view")
    case "$*" in
      *"--json labels"*) cat "$LABELS_FILE" ;;
      *"--json body"*) printf '%s' '{"body":"Closes #250"}' ;;
      *"--json headRefName"*) printf '%s' '{"headRefName":"issue/250-review-worktree"}' ;;
      *"--json headRefOid"*) printf '{"headRefOid":"%s"}' "$PR_SHA" ;;
      *) printf '%s' '{"number":7,"state":"OPEN","mergeable":"MERGEABLE","mergeStateStatus":"CLEAN"}' ;;
    esac ;;
  "pr diff") printf '%s' 'diff --git a/f.txt b/f.txt
+pr-head' ;;
  "pr checks") printf '%s' '[{"name":"test","bucket":"pass","state":"SUCCESS"}]' ;;
  "pr comment") cat > "$COMMENT_FILE" ;;
  "issue view") printf '%s' '{"number":250,"title":"review worktree","body":"## 受入条件\n- PR head","state":"OPEN","labels":[]}' ;;
  "issue edit")
    case "$*" in
      *--add-label*) printf '%s\n' "$5" >> "$LABELS_FILE" ;;
      *--remove-label*)
        grep -vxF "$5" "$LABELS_FILE" > "$LABELS_FILE.tmp" || true
        mv "$LABELS_FILE.tmp" "$LABELS_FILE" ;;
      *) : ;;
    esac ;;
  "label list") printf '%s' '[{"name":"review:pass"},{"name":"review:needs-work"}]' ;;
  "label create") : ;;
  "auth status") : ;;
  "api user") printf 'octocat\n' ;;
  *) echo "unexpected gh call: $*" >&2; exit 3 ;;
esac
EOF
cat > "$tmp/bin/claude" <<'EOF'
#!/bin/sh
pwd > "$AGENT_DIR"
git rev-parse HEAD > "$AGENT_HEAD"
last=""
for a in "$@"; do last="$a"; done
case "$last" in
  *"file: "*) cp "${last##*file: }" "$PROMPT_COPY" ;;
esac
cat "$AGENT_REPLY_FILE"
EOF
chmod +x "$tmp/bin/gh" "$tmp/bin/claude"
export PATH="$tmp/bin:$PATH"

CGO_ENABLED=0 go build -o "$tmp/lead" ./cmd/lead || fail "go build failed"

review_from() {
  local dir="$1"
  : > "$GH_LOG"; : > "$COMMENT_FILE"; : > "$AGENT_DIR"; : > "$AGENT_HEAD"
  (cd "$dir" && "$tmp/lead" review 7 --agent claude >/dev/null 2>&1)
}

# --- 1. main のチェックアウトから実行 → PR head の一時 worktree で走る ---
review_from "$repo" || fail "review from main failed"
[ "$(cat "$AGENT_HEAD")" = "$PR_SHA" ] \
  || fail "agent HEAD = $(cat "$AGENT_HEAD"), want the PR head $PR_SHA"
want_wt="$repo/.worktrees/review-pr7"
[ "$(cat "$AGENT_DIR")" = "$want_wt" ] \
  || fail "agent dir = $(cat "$AGENT_DIR"), want the PR-head worktree $want_wt"
[ -e "$want_wt" ] && fail "temporary review worktree was not removed: $want_wt"
git -C "$repo" worktree list | grep -q 'review-pr7' \
  && fail "temporary review worktree is still registered: $(git -C "$repo" worktree list)"
grep -q 'pr-head' "$PROMPT_COPY" || fail "prompt file missing the PR diff"

# --- 2. PR head をチェックアウト済みなら cwd を再利用する ---
head_wt="$tmp/head-wt"
git -C "$repo" worktree add -q "$head_wt" issue/250-review-worktree || fail "worktree add failed"
review_from "$head_wt" || fail "review from the PR-head worktree failed"
[ "$(cat "$AGENT_HEAD")" = "$PR_SHA" ] || fail "reuse: agent HEAD = $(cat "$AGENT_HEAD")"
[ "$(cat "$AGENT_DIR")" = "$head_wt" ] \
  || fail "reuse: agent dir = $(cat "$AGENT_DIR"), want the existing worktree $head_wt"
[ -e "$want_wt" ] && fail "reuse: an unnecessary temporary worktree was created"

[ "$HOME" = "$tmp/home" ] || fail "HOME isolation broken"

echo "review worktree behavioral checks passed"
