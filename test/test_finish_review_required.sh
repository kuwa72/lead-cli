#!/usr/bin/env bash
# issue #246: `lead finish` は自動マージ（risk:a）に独立レビューの
# review:pass を要求する。ダミー gh で
#   - review:pass なし → マージせず exit 0（レビュー手順を案内）
#   - review:needs-work のみ → マージしない
#   - review:pass あり → マージして close
#   - --no-review（人間専用）→ ラベルなしでもマージ
#   - ラベル取得失敗 → マージしない（fail closed）
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
mkdir -p "$HOME"

export GH_LOG="$tmp/gh.log"
export PR_LABELS_FILE="$tmp/pr-labels.txt"
export MERGED_MARK="$tmp/merged" CLOSED_MARK="$tmp/closed"
: > "$GH_LOG"; : > "$PR_LABELS_FILE"

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
      *"--json labels"*)
        if [ "${PR_LABELS_FAIL:-0}" = "1" ]; then
          echo "gh: labels unavailable" >&2
          exit 1
        fi
        cat "$PR_LABELS_FILE" ;;
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
  "api user") printf 'octocat\n' ;;
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

# seed <case> <label>... : fresh repo/state with the given PR labels.
seed() {
  local name="$1"; shift
  local repo="$tmp/repo-$name"
  mk_repo "$repo"
  export LEAD_STATE_FILE="$tmp/state-$name.json"
  : > "$PR_LABELS_FILE"
  for l in "$@"; do printf '%s\n' "$l" >> "$PR_LABELS_FILE"; done
  rm -f "$MERGED_MARK" "$CLOSED_MARK"
  : > "$GH_LOG"
  (cd "$repo" && "$tmp/lead" run 36 >/dev/null) || fail "$name: lead run failed"
}

# --- 1. review:pass なし → マージせず、レビュー手順を案内して exit 0 ---
seed no-review
out="$(cd "$tmp/repo-no-review" && "$tmp/lead" finish 36 --pr 7 --merge --timeout 60s --poll-interval 1s)" \
  || fail "no-review: finish exited non-zero: $out"
[ -f "$MERGED_MARK" ] && fail "no-review: PR merged without review:pass"
grep -q '^GH pr merge ' "$GH_LOG" && fail "no-review: gh pr merge was called: $(cat "$GH_LOG")"
case "$out" in *review:pass*) ;; *) fail "no-review: message missing review:pass: $out";; esac
case "$out" in *"lead review 7"*) ;; *) fail "no-review: message missing the lead review next step: $out";; esac

# --- 2. review:needs-work のみ → マージしない ---
seed needs-work review:needs-work
out="$(cd "$tmp/repo-needs-work" && "$tmp/lead" finish 36 --pr 7 --merge --timeout 60s --poll-interval 1s)" \
  || fail "needs-work: finish exited non-zero: $out"
[ -f "$MERGED_MARK" ] && fail "needs-work: PR merged with a needs-work verdict"
grep -q '^GH pr merge ' "$GH_LOG" && fail "needs-work: gh pr merge was called: $(cat "$GH_LOG")"

# --- 3. review:pass あり → マージして close ---
seed pass review:pass
out="$(cd "$tmp/repo-pass" && "$tmp/lead" finish 36 --pr 7 --merge --close --timeout 60s --poll-interval 1s)" \
  || fail "pass: finish failed: $out"
[ -f "$MERGED_MARK" ] || fail "pass: PR was not merged"
[ -f "$CLOSED_MARK" ] || fail "pass: issue was not closed"
grep -qx 'GH pr merge 7 --squash --delete-branch' "$GH_LOG" || fail "pass: squash merge not invoked: $(cat "$GH_LOG")"

# --- 4. --no-review（人間専用）→ ラベルなしでもマージ ---
seed no-review-flag
out="$(cd "$tmp/repo-no-review-flag" && "$tmp/lead" finish 36 --pr 7 --merge --no-review --close --timeout 60s --poll-interval 1s)" \
  || fail "no-review flag: finish failed: $out"
[ -f "$MERGED_MARK" ] || fail "no-review flag: PR was not merged"
[ -f "$CLOSED_MARK" ] || fail "no-review flag: issue was not closed"

# --- 5. ラベル取得失敗 → マージしない（fail closed） ---
seed label-fail review:pass
export PR_LABELS_FAIL=1
if out="$(cd "$tmp/repo-label-fail" && "$tmp/lead" finish 36 --pr 7 --merge --timeout 60s --poll-interval 1s 2>&1)"; then
  fail "label failure: finish exited 0: $out"
fi
unset PR_LABELS_FAIL
[ -f "$MERGED_MARK" ] && fail "label failure: PR merged despite an unknown review verdict"
grep -q '^GH pr merge ' "$GH_LOG" && fail "label failure: gh pr merge was called: $(cat "$GH_LOG")"

[ "$HOME" = "$tmp/home" ] || fail "HOME isolation broken"

echo "finish review-required behavioral checks passed"
