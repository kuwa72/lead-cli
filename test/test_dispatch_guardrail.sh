#!/usr/bin/env bash
# issue #230: ヘッドレス dispatch の完了定義が `lead finish` 経由になり、
# 規約ファイル保護のガードレールを迂回できないことを振る舞いで検証する。
# 一時 HOME・一時リポジトリ・ダミー gh・ダミーエージェントを使い、
#   - プロンプトが `lead finish <issue> --merge` を指示し `gh pr merge` を禁止する
#   - 規約ファイル（AGENTS.md 等）を含む PR は finish が停止し、gh pr merge /
#     gh issue close が呼ばれず Issue は open のまま dispatch が retry する
#   - 規約ファイルを含まない PR は finish がマージ・クローズし completed になる
# をアサートする（grep検査なし・実HOME/実リポジトリに触れない）。
set -euo pipefail

# This test exercises dispatch/finish gates, not the post-merge smoke (#275).
export LEAD_TEST_SKIP_POSTMERGE=1

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
mkdir -p "$HOME"

repo="$tmp/repo"
git init -q -b main "$repo" || fail "git init failed"
git -C "$repo" config user.email "t@t"
git -C "$repo" config user.name "t"
echo x > "$repo/f.txt"
git -C "$repo" add . && git -C "$repo" commit -qm init
git -C "$repo" remote add origin https://github.com/o/r.git

export GH_LOG="$tmp/gh.log" PROMPT_LOG="$tmp/prompt.log" FINISH_LOG="$tmp/finish.log"
export MERGED_MARK="$tmp/merged" CLOSED_MARK="$tmp/closed"
: > "$GH_LOG"; : > "$PROMPT_LOG"; : > "$FINISH_LOG"

# ISSUE selects the ready issue; FILES is the PR's changed-file list.
# LABELS is the issue's risk class JSON (risk:a keeps these fixtures merging;
# the risk gate itself is covered by test_risk_gate.sh, issue #245).
export ISSUE=7 FILES="internal/x.go" LABELS='[{"name":"risk:a"}]'

mkdir -p "$tmp/bin"
cat > "$tmp/bin/gh" <<'EOF'
#!/bin/sh
echo "GH $*" >> "$GH_LOG"
case "$1 $2" in
  "issue list")
    case "$*" in *"--label ready"*) printf '[{"number":%s,"title":"dispatch me"}]' "$ISSUE" ;; *) printf '[]' ;; esac ;;
  "issue view")
    if [ -f "$MERGED_MARK" ] || [ -f "$CLOSED_MARK" ]; then st=CLOSED; else st=OPEN; fi
    printf '{"number":%s,"title":"dispatch me","body":"do the thing","state":"%s","labels":%s}' "$ISSUE" "$st" "${LABELS:-[]}" ;;
  "issue edit"|"issue comment"|"auth status") : ;;
  "issue close") touch "$CLOSED_MARK" ;;
  "api user") printf 'lead\n' ;;
  "api repos/o/r")
    case "$*" in
      *default_branch*) printf 'main\n' ;;
      *allow_auto_merge*) printf 'true\n' ;;
      *) echo "unexpected gh api jq: $*" >&2; exit 3 ;;
    esac ;;
  "api repos/o/r/branches/main/protection")
    printf '{"required_status_checks":{"contexts":["test"]},"required_pull_request_reviews":{}}' ;;
  "api repos/o/r/rules/branches/main") printf '[]' ;;
  "pr list") printf '9\n' ;;
  "pr view")
    case "$*" in
      *"--json labels"*) printf 'review:pass\n' ;;
      *"--json headRefOid"*) printf '{"headRefOid":"0123456789abcdef0123456789abcdef01234567"}' ;;
      *"--json comments"*) printf '{"comments":[{"author":{"login":"lead"},"body":"<!-- lead-review sha=0123456789abcdef0123456789abcdef01234567 verdict=pass -->","createdAt":"2026-10-25T00:00:00Z"}]}' ;;
      *"--json files"*) printf '%s\n' $FILES ;;
      *) printf '{"number":9,"state":"OPEN","mergeable":"MERGEABLE","mergeStateStatus":"CLEAN"}' ;;
    esac ;;
  "pr checks") printf '[{"name":"test","bucket":"pass","state":"SUCCESS"}]' ;;
  "pr merge") touch "$MERGED_MARK" ;;
  *) echo "unexpected gh call: $*" >&2; exit 3 ;;
esac
EOF
cat > "$tmp/bin/claude" <<'EOF'
#!/bin/sh
# Headless agent stub: record the launch prompt, then follow the completion
# definition's merge step through `lead finish`.
for a in "$@"; do printf '%s\n' "$a" >> "$PROMPT_LOG"; done
"$LEAD_BIN" finish "$ISSUE" --merge >> "$FINISH_LOG" 2>&1
EOF
chmod +x "$tmp/bin/gh" "$tmp/bin/claude"
export PATH="$tmp/bin:$PATH"

CGO_ENABLED=0 go build -o "$tmp/lead" ./cmd/lead || fail "go build failed"
ln -sf "$tmp/lead" "$tmp/bin/lead"
export LEAD_BIN="$tmp/lead"

# --- 1. 規約ファイルを含む PR は自動マージされない --------------------------
export ISSUE=7 FILES="AGENTS.md .github/workflows/ci.yml"
rm -f "$MERGED_MARK" "$CLOSED_MARK"
: > "$GH_LOG"; : > "$PROMPT_LOG"; : > "$FINISH_LOG"
out="$(cd "$repo" && "$tmp/lead" dispatch --once --parallel 1 --agent claude 2>&1)" || fail "dispatch exited non-zero: $out"

grep -q 'lead finish 7 --merge' "$PROMPT_LOG" || fail "prompt does not route the merge through lead finish: $(cat "$PROMPT_LOG")"
grep -q 'gh pr merge --squash' "$PROMPT_LOG" && fail "prompt still instructs a direct gh pr merge"
grep -q 'gh issue close' "$PROMPT_LOG" && fail "prompt still instructs a direct gh issue close"

for want in "AGENTS.md" ".github/workflows/ci.yml" "protected files" "confirm" "--override-guardrail"; do
  grep -qF -- "$want" "$FINISH_LOG" || fail "finish did not stop with the guardrail message (missing '$want'): $(cat "$FINISH_LOG")"
done
grep -q '^GH pr merge ' "$GH_LOG" && fail "guarded PR was merged directly: $(cat "$GH_LOG")"
grep -q '^GH issue close ' "$GH_LOG" && fail "guarded PR's issue was closed: $(cat "$GH_LOG")"
[ -f "$CLOSED_MARK" ] && fail "guarded issue was closed"
case "$out" in *"#7 retry"*) ;; *) fail "guarded issue should be retried, got: $out";; esac
grep -q '"attempts": 1' "$LEAD_STATE_FILE" || fail "guardrail stop was not counted as a failed attempt: $(cat "$LEAD_STATE_FILE")"
grep -q '"merge_policy": "confirm"' "$LEAD_STATE_FILE" || fail "guardrail did not lower the recorded policy to confirm: $(cat "$LEAD_STATE_FILE")"

# --- 2. 規約ファイルを含まない PR は finish 経由でマージ・クローズされる -----
export ISSUE=8 FILES="internal/x.go"
rm -f "$MERGED_MARK" "$CLOSED_MARK"
: > "$GH_LOG"; : > "$PROMPT_LOG"; : > "$FINISH_LOG"
out="$(cd "$repo" && "$tmp/lead" dispatch --once --parallel 1 --agent claude 2>&1)" || fail "dispatch exited non-zero: $out"

grep -qxF 'GH pr merge 9 --squash --delete-branch --match-head-commit 0123456789abcdef0123456789abcdef01234567' "$GH_LOG" || fail "clean PR was not merged through finish: $(cat "$GH_LOG")"
grep -qxF 'GH issue close 8' "$GH_LOG" || fail "issue was not closed through finish: $(cat "$GH_LOG")"
[ -f "$CLOSED_MARK" ] || fail "clean PR's issue was not closed"
case "$out" in *"#8 completed"*) ;; *) fail "clean issue should be completed, got: $out";; esac
[ -d "$repo/.worktrees/issue-8" ] && fail "worktree not removed after completion"
git -C "$repo" worktree list | grep -q "issue-8" && fail "worktree still registered after completion"

echo "dispatch guardrail tests passed"
