#!/usr/bin/env bash
# issue #255: 朝の承認を lead に閉じる。
# ダミー gh の下で
#   - awaiting-human の Issue を `lead approve` すると CI 待機 → squash merge →
#     close が呼ばれ、awaiting-human が外れる
#   - awaiting-human でない Issue は exit 1 で拒否され、merge が呼ばれない
#   - --dry-run は merge せず PR 番号とレビュー verdict を表示する
#   - inbox の Awaiting approval 区画で A → y が lead approve 相当の gh 呼び出しになる
#   - 0 件でも区画が残り "none" と出る
# をアサートする（ソース grep なし・一時 HOME/リポジトリ）。
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
unset XDG_STATE_HOME LEAD_TEST_INBOX_KEYS || true
export LEAD_NOTIFY=0
mkdir -p "$HOME"

export GH_LOG="$tmp/gh.log"
export LABELS_FILE="$tmp/labels.txt"
export MERGED_MARK="$tmp/merged" CLOSED_MARK="$tmp/closed"
: > "$GH_LOG"; : > "$LABELS_FILE"

mkdir -p "$tmp/bin"
cat > "$tmp/bin/gh" <<'EOF'
#!/bin/sh
echo "GH $*" >> "$GH_LOG"
labels_json() {
  first=1
  printf '['
  while IFS= read -r l; do
    [ -n "$l" ] || continue
    [ "$first" = 1 ] || printf ','
    first=0
    printf '{"name":"%s"}' "$l"
  done < "$LABELS_FILE"
  printf ']'
}
has_label() {
  grep -qxF "$1" "$LABELS_FILE" 2>/dev/null
}
case "$1 $2" in
  "issue list")
    case "$*" in
      *"--label awaiting-human"*)
        if has_label awaiting-human; then
          printf '[{"number":36,"title":"demo","updatedAt":"2026-10-25T00:00:00Z"}]'
        else
          printf '[]'
        fi ;;
      *"--state closed"*) printf '[]' ;;
      *) printf '[]' ;;
    esac ;;
  "issue view")
    case "$*" in
      *"--json comments"*) printf '{"comments":[]}' ;;
      *)
        if [ -f "$CLOSED_MARK" ]; then st=CLOSED; else st=OPEN; fi
        printf '{"number":36,"title":"demo","body":"b","state":"%s","labels":%s}' "$st" "$(labels_json)" ;;
    esac ;;
  "issue edit")
    case "$*" in
      *--add-label*) printf '%s\n' "$5" >> "$LABELS_FILE" ;;
      *--remove-label*)
        grep -vxF "$5" "$LABELS_FILE" > "$LABELS_FILE.tmp" || true
        mv "$LABELS_FILE.tmp" "$LABELS_FILE" ;;
      *) : ;;
    esac ;;
  "issue close") touch "$CLOSED_MARK" ;;
  "pr list") printf '7\n' ;;
  "pr view")
    case "$*" in
      *"--json labels"*) printf '%s\n' "${REVIEW_VERDICT:-review:pass}" ;;
      *"--json headRefOid"*) printf '{"headRefOid":"0123456789abcdef0123456789abcdef01234567"}' ;;
      *"--json comments"*) printf '{"comments":[]}' ;;
      *"--json files"*) printf '%s\n' "${FILES:-internal/x.go}" ;;
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
  "auth status") : ;;
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

# seed <case> <label>... : fresh repo, state file and label set.
seed() {
  local name="$1"; shift
  local repo="$tmp/repo-$name"
  mk_repo "$repo"
  export LEAD_STATE_FILE="$tmp/state-$name.json"
  : > "$LABELS_FILE"
  for l in "$@"; do printf '%s\n' "$l" >> "$LABELS_FILE"; done
  rm -f "$MERGED_MARK" "$CLOSED_MARK"
  : > "$GH_LOG"
  (cd "$repo" && "$tmp/lead" run 36 >/dev/null) || fail "$name: lead run failed"
  mkdir -p "$(dirname "$LEAD_STATE_FILE")"
  cat > "$(dirname "$LEAD_STATE_FILE")/inbox-seen.json" <<'JSON'
{"help_shown":true}
JSON
}

# --- 1. awaiting-human: approve merges, closes, clears the label -----------
seed approve risk:b awaiting-human
out="$(cd "$tmp/repo-approve" && "$tmp/lead" approve 36 --timeout 60s --poll-interval 1s)" \
  || fail "approve exited non-zero: $out"
[ -f "$MERGED_MARK" ] || fail "approve did not merge: $out"
[ -f "$CLOSED_MARK" ] || fail "approve did not close the issue: $out"
grep -qx 'GH issue edit 36 --remove-label awaiting-human' "$GH_LOG" \
  || fail "awaiting-human was not removed: $(cat "$GH_LOG")"
grep -q -- '^GH pr merge 7 --squash --delete-branch' "$GH_LOG" \
  || fail "squash merge not invoked: $(cat "$GH_LOG")"
has_awaiting=0
grep -qx 'awaiting-human' "$LABELS_FILE" && has_awaiting=1
[ "$has_awaiting" = 0 ] || fail "awaiting-human label still present: $(cat "$LABELS_FILE")"
case "$out" in *merged*closed*) ;; *) fail "approve report missing merge/close: $out";; esac

# --- 2. not awaiting-human: exit 1, no merge -------------------------------
seed reject risk:b
out="$(cd "$tmp/repo-reject" && "$tmp/lead" approve 36 --timeout 60s --poll-interval 1s 2>&1)" \
  && fail "approve on a non-awaiting issue exited 0: $out"
case "$out" in *awaiting-human*) ;; *) fail "rejection does not name awaiting-human: $out";; esac
[ -f "$MERGED_MARK" ] && fail "non-awaiting issue was merged"
[ -f "$CLOSED_MARK" ] && fail "non-awaiting issue was closed"
grep -q '^GH pr merge ' "$GH_LOG" && fail "gh pr merge called for a non-awaiting issue: $(cat "$GH_LOG")"

# --- 3. --dry-run: report PR + verdict, never merge ------------------------
seed dryrun risk:b awaiting-human
out="$(cd "$tmp/repo-dryrun" && "$tmp/lead" approve 36 --dry-run)" \
  || fail "approve --dry-run exited non-zero: $out"
[ -f "$MERGED_MARK" ] && fail "dry-run merged the PR"
[ -f "$CLOSED_MARK" ] && fail "dry-run closed the issue"
grep -q '^GH pr merge ' "$GH_LOG" && fail "dry-run called gh pr merge: $(cat "$GH_LOG")"
case "$out" in *"#7"*) ;; *) fail "dry-run output lacks the PR number: $out";; esac
case "$out" in *review:pass*) ;; *) fail "dry-run output lacks the review verdict: $out";; esac

# --- 4. inbox: A -> y in the Awaiting approval section approves ------------
# The issue keeps the ready label, as the risk gate leaves it in place; the
# inbox must still show it under Awaiting approval (not Ready).
seed inbox risk:b awaiting-human ready
out="$(cd "$tmp/repo-inbox" && LEAD_TEST_INBOX_KEYS='A,y,enter,q' "$tmp/lead" 2>&1)" \
  || fail "headless inbox approve exited non-zero: $out"
[ -f "$MERGED_MARK" ] || fail "inbox A did not merge: $out"
[ -f "$CLOSED_MARK" ] || fail "inbox A did not close the issue: $out"
grep -qx 'GH issue edit 36 --remove-label awaiting-human' "$GH_LOG" \
  || fail "inbox A did not clear awaiting-human: $(cat "$GH_LOG")"
case "$out" in *"Awaiting approval (1)"*) ;; *) fail "awaiting section did not list #36: $out";; esac

# --- 5. the empty Awaiting approval section stays visible ------------------
seed empty risk:b
out="$(cd "$tmp/repo-empty" && LEAD_TEST_INBOX_KEYS='q' "$tmp/lead" 2>&1)" \
  || fail "headless inbox on an empty queue exited non-zero: $out"
case "$out" in *"Awaiting approval (0)"*) ;; *) fail "empty awaiting section missing: $out";; esac
printf '%s\n' "$out" | awk '/Awaiting approval \(0\)/ && /none/ {ok=1} END{exit ok?0:1}' \
  || fail "empty awaiting section does not say none: $out"

# --- 6. help documents the approval flow ----------------------------------
help_out="$("$tmp/lead" approve --help 2>&1)" || fail "approve --help failed: $help_out"
case "$help_out" in *awaiting-human*) ;; *) fail "approve help lacks the awaiting-human gate: $help_out";; esac
case "$help_out" in *--dry-run*) ;; *) fail "approve help lacks --dry-run: $help_out";; esac

[ "$HOME" = "$tmp/home" ] || fail "HOME isolation broken"

echo "approve behavioral checks passed"
