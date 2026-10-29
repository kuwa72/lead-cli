#!/usr/bin/env bash
# issue #246 / #249: `lead finish` は自動マージ（risk:a）に独立レビューの
# pass を要求する。pass は PR コメントの
# `<!-- lead-review sha=<head> verdict=pass -->` で表され、現在の head SHA
# と一致する場合のみ有効。ダミー gh で
#   - レビューコメントなし → マージせず exit 0（レビュー手順を案内）
#   - needs-work のみ → マージしない
#   - 現在の head に対する pass あり → マージして close
#   - --no-review --approve（人間専用）→ コメントなしでもマージ
#   - コメント取得失敗 → マージしない（fail closed）
# を検証する（grep によるソース検査なし・一時 HOME/リポジトリ）。
set -euo pipefail

# These tests exercise finish gates, not the post-merge smoke (#275).
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
mkdir -p "$HOME"

export GH_LOG="$tmp/gh.log"
export PR_MARKER_FILE="$tmp/pr-marker.txt"
export HEAD_SHA_FILE="$tmp/head-sha.txt"
export MERGED_MARK="$tmp/merged" CLOSED_MARK="$tmp/closed"
: > "$GH_LOG"; : > "$PR_MARKER_FILE"; : > "$HEAD_SHA_FILE"

mkdir -p "$tmp/bin"
cat > "$tmp/bin/gh" <<'EOF'
#!/bin/sh
echo "GH $*" >> "$GH_LOG"
case "$1 $2" in
  "issue view")
    if [ -f "$CLOSED_MARK" ]; then st=CLOSED; else st=OPEN; fi
    printf '{"number":36,"title":"demo","body":"b","state":"%s","labels":[{"name":"risk:a"}]}' "$st" ;;
  "issue close") touch "$CLOSED_MARK" ;;
  "issue edit"|"issue comment"|"auth status") : ;;
  "pr view")
    case "$*" in
      *"--json headRefOid"*) printf '{"headRefOid":"%s"}' "$(cat "$HEAD_SHA_FILE")" ;;
      *"--json comments"*)
        if [ "${PR_COMMENTS_FAIL:-0}" = "1" ]; then
          echo "gh: comments unavailable" >&2
          exit 1
        fi
        if [ -s "$PR_MARKER_FILE" ]; then
          printf '{"comments":[{"author":{"login":"lead"},"body":"%s","createdAt":"2026-10-25T00:00:00Z"}]}' "$(cat "$PR_MARKER_FILE")"
        else
          printf '{"comments":[]}'
        fi ;;
      *"--json labels"*) printf '' ;;
      *"--json files"*) printf 'internal/x.go\n' ;;
      *) printf '{"number":7,"state":"OPEN","mergeable":"MERGEABLE","mergeStateStatus":"CLEAN"}' ;;
    esac ;;
  "pr checks") printf '[{"name":"test","bucket":"pass","state":"SUCCESS"}]' ;;
  "pr merge") touch "$MERGED_MARK" ;;
  "api repos/o/r")
    case "$*" in
      *default_branch*) printf 'main\n' ;;
      *allow_auto_merge*) printf 'true\n' ;;
      *) echo "unexpected gh api: $*" >&2; exit 3 ;;
    esac ;;
  "api user") printf 'lead\n' ;;
  *) echo "unexpected gh call: $*" >&2; exit 3 ;;
esac
EOF
chmod +x "$tmp/bin/gh"
export PATH="$tmp/bin:$PATH"

CGO_ENABLED=0 go build -o "$tmp/lead" ./cmd/lead || fail "go build failed"

mk_repo() {
  local repo="$1"
  git init -q -b main "$repo" || fail "git init failed"
  git -C "$repo" config user.email "t@t"
  git -C "$repo" config user.name "t"
  echo x > "$repo/f.txt"
  git -C "$repo" add . && git -C "$repo" commit -qm init
  git -C "$repo" remote add origin https://github.com/o/r.git
}

HEAD_A="0123456789abcdef0123456789abcdef01234567"

# seed <case> <marker-or-empty> : fresh repo/state with the given review marker.
seed() {
  local name="$1" marker="$2"
  local repo="$tmp/repo-$name"
  mk_repo "$repo"
  export LEAD_STATE_FILE="$tmp/state-$name.json"
  printf '%s' "$marker" > "$PR_MARKER_FILE"
  printf '%s' "$HEAD_A" > "$HEAD_SHA_FILE"
  rm -f "$MERGED_MARK" "$CLOSED_MARK"
  : > "$GH_LOG"
  (cd "$repo" && "$tmp/lead" run 36 >/dev/null) || fail "$name: lead run failed"
}

# --- 1. レビューコメントなし → マージせず、レビュー手順を案内して exit 0 ---
seed no-review ''
out="$(cd "$tmp/repo-no-review" && "$tmp/lead" finish 36 --pr 7 --merge --timeout 60s --poll-interval 1s)" \
  || fail "no-review: finish exited non-zero: $out"
[ -f "$MERGED_MARK" ] && fail "no-review: PR merged without a review pass"
grep -q '^GH pr merge ' "$GH_LOG" && fail "no-review: gh pr merge was called: $(cat "$GH_LOG")"
case "$out" in *"no review pass"*) ;; *) fail "no-review: message missing no review pass: $out";; esac
case "$out" in *"lead review 7"*) ;; *) fail "no-review: message missing the lead review next step: $out";; esac
grep -q '^GH pr view 7 --json headRefOid' "$GH_LOG" || fail "no-review: head SHA was not queried: $(cat "$GH_LOG")"
grep -q '^GH pr view 7 --json comments' "$GH_LOG" || fail "no-review: PR comments were not queried: $(cat "$GH_LOG")"

# --- 2. needs-work のみ → マージしない ---
seed needs-work "<!-- lead-review sha=$HEAD_A verdict=needs-work -->"
out="$(cd "$tmp/repo-needs-work" && "$tmp/lead" finish 36 --pr 7 --merge --timeout 60s --poll-interval 1s)" \
  || fail "needs-work: finish exited non-zero: $out"
[ -f "$MERGED_MARK" ] && fail "needs-work: PR merged with a needs-work verdict"
grep -q '^GH pr merge ' "$GH_LOG" && fail "needs-work: gh pr merge was called: $(cat "$GH_LOG")"
case "$out" in *"lead review 7"*) ;; *) fail "needs-work: message missing the re-review step: $out";; esac

# --- 3. 現在の head に対する pass あり → マージして close ---
seed pass "<!-- lead-review sha=$HEAD_A verdict=pass -->"
out="$(cd "$tmp/repo-pass" && "$tmp/lead" finish 36 --pr 7 --merge --close --timeout 60s --poll-interval 1s)" \
  || fail "pass: finish failed: $out"
[ -f "$MERGED_MARK" ] || fail "pass: PR was not merged"
[ -f "$CLOSED_MARK" ] || fail "pass: issue was not closed"
grep -qx "GH pr merge 7 --squash --delete-branch --match-head-commit $HEAD_A" "$GH_LOG" || fail "pass: squash merge not invoked: $(cat "$GH_LOG")"

# --- 4. --no-review --approve（人間専用）→ コメントなしでもマージ ---
seed no-review-flag ''
out="$(cd "$tmp/repo-no-review-flag" && "$tmp/lead" finish 36 --pr 7 --merge --no-review --approve --close --timeout 60s --poll-interval 1s)" \
  || fail "no-review flag: finish failed: $out"
[ -f "$MERGED_MARK" ] || fail "no-review flag: PR was not merged"
[ -f "$CLOSED_MARK" ] || fail "no-review flag: issue was not closed"

# --- 5. コメント取得失敗 → マージしない（fail closed） ---
seed comment-fail "<!-- lead-review sha=$HEAD_A verdict=pass -->"
export PR_COMMENTS_FAIL=1
if out="$(cd "$tmp/repo-comment-fail" && "$tmp/lead" finish 36 --pr 7 --merge --timeout 60s --poll-interval 1s 2>&1)"; then
  fail "comment failure: finish exited 0: $out"
fi
unset PR_COMMENTS_FAIL
[ -f "$MERGED_MARK" ] && fail "comment failure: PR merged despite an unknown review verdict"
grep -q '^GH pr merge ' "$GH_LOG" && fail "comment failure: gh pr merge was called: $(cat "$GH_LOG")"

[ "$HOME" = "$tmp/home" ] || fail "HOME isolation broken"

echo "finish review-required behavioral checks passed"
