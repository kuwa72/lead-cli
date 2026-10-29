#!/usr/bin/env bash
# issue #249: --no-review は --approve との同時指定を必須にする。
# ダミー gh で
#   - `--no-review` 単独 → エラー（exit 非 0、マージしない、--approve を案内）
#   - `--no-review --approve` → レビューなしでもマージする
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
export MERGED_MARK="$tmp/merged" CLOSED_MARK="$tmp/closed"
: > "$GH_LOG"

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
      *"--json headRefOid"*) printf '{"headRefOid":"0123456789abcdef0123456789abcdef01234567"}' ;;
      *"--json comments"*) printf '{"comments":[]}' ;;
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

seed() {
  local name="$1"
  local repo="$tmp/repo-$name"
  mk_repo "$repo"
  export LEAD_STATE_FILE="$tmp/state-$name.json"
  rm -f "$MERGED_MARK" "$CLOSED_MARK"
  : > "$GH_LOG"
  (cd "$repo" && "$tmp/lead" run 36 >/dev/null) || fail "$name: lead run failed"
}

# --- 1. --no-review 単独 → エラー、マージしない ---
seed alone
if out="$(cd "$tmp/repo-alone" && "$tmp/lead" finish 36 --pr 7 --merge --no-review --close --timeout 60s --poll-interval 1s 2>&1)"; then
  fail "no-review alone: finish exited 0: $out"
fi
case "$out" in *"--approve"*) ;; *) fail "no-review alone: error missing --approve guidance: $out";; esac
[ -f "$MERGED_MARK" ] && fail "no-review alone: PR was merged"
[ -f "$CLOSED_MARK" ] && fail "no-review alone: issue was closed"
grep -q '^GH pr merge ' "$GH_LOG" && fail "no-review alone: gh pr merge was called: $(cat "$GH_LOG")"

# --- 2. --no-review --approve → レビューなしでもマージ ---
seed approved
out="$(cd "$tmp/repo-approved" && "$tmp/lead" finish 36 --pr 7 --merge --no-review --approve --close --timeout 60s --poll-interval 1s)" \
  || fail "no-review --approve: finish failed: $out"
[ -f "$MERGED_MARK" ] || fail "no-review --approve: PR was not merged"
[ -f "$CLOSED_MARK" ] || fail "no-review --approve: issue was not closed"
grep -qx 'GH pr merge 7 --squash --delete-branch --match-head-commit 0123456789abcdef0123456789abcdef01234567' "$GH_LOG" \
  || fail "no-review --approve: squash merge not invoked: $(cat "$GH_LOG")"

[ "$HOME" = "$tmp/home" ] || fail "HOME isolation broken"

echo "no-review requires approve behavioral checks passed"
