#!/usr/bin/env bash
# issue #250: verdict ラベルが無いリポジトリでも `lead review` が
# ラベルを作成してから付与する。作成に失敗した場合は PR コメントを
# 投稿せずにエラー終了する（grep によるソース検査なし・一時 HOME/リポジトリ）。
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
echo x > "$repo/f.txt"
git -C "$repo" add . && git -C "$repo" commit -qm init
git -C "$repo" remote add origin https://github.com/o/r.git
PR_SHA="$(git -C "$repo" rev-parse HEAD)"
export PR_SHA

export GH_LOG="$tmp/gh.log" COMMENT_FILE="$tmp/comment.md" LABELS_FILE="$tmp/pr-labels.txt"
export CREATE_FAIL="$tmp/create-fail" HAVE_LABELS="$tmp/have-labels"
: > "$GH_LOG"; : > "$COMMENT_FILE"; : > "$LABELS_FILE"
rm -f "$CREATE_FAIL" "$HAVE_LABELS"
export AGENT_REPLY_FILE="$tmp/reply.md"
cat > "$AGENT_REPLY_FILE" <<'EOF'
## 所見
- 受入条件を確認した

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
      *"--json headRefName"*) printf '%s' '{"headRefName":"issue/250-review-labels"}' ;;
      *"--json headRefOid"*) printf '{"headRefOid":"%s"}' "$PR_SHA" ;;
      *) printf '%s' '{"number":7,"state":"OPEN","mergeable":"MERGEABLE","mergeStateStatus":"CLEAN"}' ;;
    esac ;;
  "pr diff") printf '%s' 'diff --git a/f.txt b/f.txt
+x' ;;
  "pr checks") printf '%s' '[{"name":"test","bucket":"pass","state":"SUCCESS"}]' ;;
  "pr comment") cat > "$COMMENT_FILE" ;;
  "issue view") printf '%s' '{"number":250,"title":"review labels","body":"## 受入条件\n- labels","state":"OPEN","labels":[]}' ;;
  "issue edit")
    case "$*" in
      *--add-label*) printf '%s\n' "$5" >> "$LABELS_FILE" ;;
      *--remove-label*)
        grep -vxF "$5" "$LABELS_FILE" > "$LABELS_FILE.tmp" || true
        mv "$LABELS_FILE.tmp" "$LABELS_FILE" ;;
      *) : ;;
    esac ;;
  "label list")
    if [ -f "$HAVE_LABELS" ]; then
      printf '%s' '[{"name":"review:pass"},{"name":"review:needs-work"}]'
    else
      printf '%s' '[{"name":"needs-review"}]'
    fi ;;
  "label create")
    [ -f "$CREATE_FAIL" ] && { echo "label create failed" >&2; exit 1; }
    touch "$HAVE_LABELS"
    : ;;
  "auth status") : ;;
  "api user") printf 'octocat\n' ;;
  *) echo "unexpected gh call: $*" >&2; exit 3 ;;
esac
EOF
cat > "$tmp/bin/claude" <<'EOF'
#!/bin/sh
cat "$AGENT_REPLY_FILE"
EOF
chmod +x "$tmp/bin/gh" "$tmp/bin/claude"
export PATH="$tmp/bin:$PATH"

CGO_ENABLED=0 go build -o "$tmp/lead" ./cmd/lead || fail "go build failed"

# --- 1. verdict ラベルなし → review:pass を作成してから付与・コメント ---
out="$(cd "$repo" && "$tmp/lead" review 7 --agent claude 2>&1)" \
  || fail "review without verdict labels failed: $out"
grep -qx 'GH label create review:pass --color 0E8A16 --description Independent review passed --repo o/r' "$GH_LOG" \
  || fail "review:pass label was not created: $(cat "$GH_LOG")"
grep -qx 'review:pass' "$LABELS_FILE" || fail "review:pass was not applied"
create_line="$(grep -n '^GH label create review:pass ' "$GH_LOG" | cut -d: -f1)"
comment_line="$(grep -n '^GH pr comment 7' "$GH_LOG" | cut -d: -f1)"
[ -n "$create_line" ] && [ -n "$comment_line" ] && [ "$create_line" -lt "$comment_line" ] \
  || fail "label creation must happen before the PR comment: $(cat "$GH_LOG")"

# --- 2. needs-work も同様に作成して付与 ---
rm -f "$HAVE_LABELS"; : > "$GH_LOG"; : > "$COMMENT_FILE"; : > "$LABELS_FILE"
cat > "$AGENT_REPLY_FILE" <<'EOF'
## 所見
- 修正が必要

VERDICT: needs-work
EOF
if out="$(cd "$repo" && "$tmp/lead" review 7 --agent claude 2>&1)"; then
  fail "needs-work exited 0: $out"
fi
grep -qx 'GH label create review:needs-work --color D93F0B --description Independent review needs work --repo o/r' "$GH_LOG" \
  || fail "review:needs-work label was not created: $(cat "$GH_LOG")"
grep -qx 'review:needs-work' "$LABELS_FILE" || fail "review:needs-work was not applied"

# --- 3. ラベル作成失敗 → PR コメントを残さずエラー終了 ---
rm -f "$HAVE_LABELS"; touch "$CREATE_FAIL"
: > "$GH_LOG"; : > "$COMMENT_FILE"; : > "$LABELS_FILE"
cat > "$AGENT_REPLY_FILE" <<'EOF'
VERDICT: pass
EOF
if out="$(cd "$repo" && "$tmp/lead" review 7 --agent claude 2>&1)"; then
  fail "label creation failure exited 0: $out"
fi
[ -s "$COMMENT_FILE" ] && fail "a PR comment was posted despite label creation failure: $(cat "$COMMENT_FILE")"
grep -q '^GH pr comment ' "$GH_LOG" && fail "gh pr comment was called despite label creation failure: $(cat "$GH_LOG")"
grep -q '^GH issue edit 7 --add-label ' "$GH_LOG" && fail "a label was applied despite creation failure: $(cat "$GH_LOG")"

[ "$HOME" = "$tmp/home" ] || fail "HOME isolation broken"

echo "review label behavioral checks passed"
