#!/usr/bin/env bash
# issue #246: 独立レビューゲート `lead review <pr>` の振る舞いテスト。
# ダミー gh・ダミーエージェントで
#   - VERDICT: pass → exit 0、review:pass ラベル、PR コメント投稿
#   - VERDICT: needs-work → exit 1、review:needs-work、コメント投稿
#   - verdict 行なし/解釈不能 → pass にしない（exit 1、review:pass を付けない）
# を検証する（grep によるソース検査なし・一時 HOME/リポジトリ）。
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

export GH_LOG="$tmp/gh.log" PROMPT_LOG="$tmp/prompt.log"
export COMMENT_FILE="$tmp/comment.md" LABELS_FILE="$tmp/pr-labels.txt"
: > "$GH_LOG"; : > "$PROMPT_LOG"; : > "$COMMENT_FILE"; : > "$LABELS_FILE"
export AGENT_REPLY_FILE="$tmp/reply.md"
: > "$AGENT_REPLY_FILE"

mkdir -p "$tmp/bin"
cat > "$tmp/bin/gh" <<'EOF'
#!/bin/sh
echo "GH $*" >> "$GH_LOG"
case "$1 $2" in
  "pr view")
    case "$*" in
      *"--json labels"*) cat "$LABELS_FILE" ;;
      *"--json body"*) printf '%s' '{"body":"Closes #246"}' ;;
      *"--json headRefName"*) printf '%s' '{"headRefName":"issue/246-feat-review"}' ;;
      *) printf '%s' '{"number":7,"state":"OPEN","mergeable":"MERGEABLE","mergeStateStatus":"CLEAN"}' ;;
    esac ;;
  "pr diff") printf '%s' 'diff --git a/internal/review/review.go b/internal/review/review.go
+package review' ;;
  "pr checks") printf '%s' '[{"name":"test","bucket":"pass","state":"SUCCESS"}]' ;;
  "pr comment") cat > "$COMMENT_FILE" ;;
  "issue view") printf '%s' '{"number":246,"title":"feat(review): gate","body":"## 受入条件\n- review:pass","state":"OPEN","labels":[]}' ;;
  "issue edit")
    case "$*" in
      *--add-label*) printf '%s\n' "$5" >> "$LABELS_FILE" ;;
      *--remove-label*)
        grep -vxF "$5" "$LABELS_FILE" > "$LABELS_FILE.tmp" || true
        mv "$LABELS_FILE.tmp" "$LABELS_FILE" ;;
      *) : ;;
    esac ;;
  "auth status") : ;;
  "api user") printf 'octocat\n' ;;
  *) echo "unexpected gh call: $*" >&2; exit 3 ;;
esac
EOF
cat > "$tmp/bin/claude" <<'EOF'
#!/bin/sh
for a in "$@"; do printf '%s\n' "$a" >> "$PROMPT_LOG"; done
cat "$AGENT_REPLY_FILE"
EOF
chmod +x "$tmp/bin/gh" "$tmp/bin/claude"
export PATH="$tmp/bin:$PATH"

CGO_ENABLED=0 go build -o "$tmp/lead" ./cmd/lead || fail "go build failed"

reset_run() {
  : > "$GH_LOG"; : > "$PROMPT_LOG"; : > "$COMMENT_FILE"
}
label_present() { grep -qx "$1" "$LABELS_FILE"; }

# --- 1. VERDICT: pass → exit 0、review:pass、コメント投稿 ---
cat > "$AGENT_REPLY_FILE" <<'EOF'
## 所見
- 受入条件を実行ログで確認した
- スコープ外の差分なし

VERDICT: pass
EOF
reset_run
out="$(cd "$repo" && "$tmp/lead" review 7 --agent claude 2>&1)" \
  || fail "pass verdict exited non-zero: $out"
case "$out" in *review:pass*) ;; *) fail "pass output missing review:pass: $out";; esac
label_present 'review:pass' || fail "review:pass label was not added: $(cat "$LABELS_FILE")"
grep -q 'VERDICT: pass' "$COMMENT_FILE" || fail "PR comment missing the review body: $(cat "$COMMENT_FILE")"
grep -q '受入条件を実行ログで確認した' "$COMMENT_FILE" || fail "PR comment missing the findings"
grep -qx 'GH issue edit 7 --add-label review:pass' "$GH_LOG" \
  || fail "review:pass was not applied through gh issue edit: $(cat "$GH_LOG")"
for want in '受入条件を実際に検証したか' 'スコープ外の差分' 'テストが実装の写経になっていないか' \
            'エラーハンドリング' '秘密情報' 'マイグレーションの後方互換' '新規依存の妥当性' 'ついで' \
            '読み取り専用' 'VERDICT: pass'; do
  grep -qF -- "$want" "$PROMPT_LOG" || fail "agent prompt missing checklist item '$want'"
done
grep -qF -- '## 受入条件' "$PROMPT_LOG" || fail "agent prompt missing the issue acceptance criteria"
grep -qF -- '+package review' "$PROMPT_LOG" || fail "agent prompt missing the PR diff"
grep -qF -- 'test: pass' "$PROMPT_LOG" || fail "agent prompt missing the CI result"

# --- 2. VERDICT: needs-work → exit 1、needs-work、pass を外す、コメント投稿 ---
cat > "$AGENT_REPLY_FILE" <<'EOF'
## 所見
- テストが実装の写経になっている

VERDICT: needs-work
EOF
reset_run
if out="$(cd "$repo" && "$tmp/lead" review 7 --agent claude 2>&1)"; then
  fail "needs-work verdict exited 0: $out"
fi
label_present 'review:needs-work' || fail "review:needs-work label was not added: $(cat "$LABELS_FILE")"
label_present 'review:pass' && fail "stale review:pass label survived a needs-work verdict"
grep -q 'テストが実装の写経になっている' "$COMMENT_FILE" || fail "needs-work comment missing the findings"
grep -qx 'GH issue edit 7 --remove-label review:pass' "$GH_LOG" \
  || fail "stale review:pass was not removed: $(cat "$GH_LOG")"

# --- 3. verdict 行なし/解釈不能 → pass にしない ---
for reply in 'looks good to me' 'VERDICT: LGTM' ''; do
  printf '%s\n' "$reply" > "$AGENT_REPLY_FILE"
  reset_run
  if out="$(cd "$repo" && "$tmp/lead" review 7 --agent claude 2>&1)"; then
    fail "invalid verdict '$reply' exited 0: $out"
  fi
  label_present 'review:pass' && fail "invalid verdict '$reply' got review:pass"
done

[ "$HOME" = "$tmp/home" ] || fail "HOME isolation broken"

echo "review gate behavioral checks passed"
