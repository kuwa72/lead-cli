#!/usr/bin/env bash
# issue #245: 承認待ち（awaiting-human）は失敗ではない。
# ダミー gh・ダミーエージェントで dispatch を 1 パス動かし、CI 成功後に
# finish が awaiting-human を付けて exit 0 したとき、
#   - blocked ラベルが付かず、attempts が増えない
#   - status が awaiting_review になり worktree が片付く
#   - 次のパスで再 dispatch されない
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
mkdir -p "$HOME"

repo="$tmp/repo"
git init -q -b main "$repo" || fail "git init failed"
git -C "$repo" config user.email "t@t"
git -C "$repo" config user.name "t"
echo x > "$repo/f.txt"
git -C "$repo" add . && git -C "$repo" commit -qm init
git -C "$repo" remote add origin https://github.com/o/r.git

export GH_LOG="$tmp/gh.log" PROMPT_LOG="$tmp/prompt.log" FINISH_LOG="$tmp/finish.log"
export LABELS_FILE="$tmp/labels.txt"
export MERGED_MARK="$tmp/merged" CLOSED_MARK="$tmp/closed"
: > "$GH_LOG"; : > "$PROMPT_LOG"; : > "$FINISH_LOG"; : > "$LABELS_FILE"

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
case "$1 $2" in
  "issue list")
    case "$*" in *"--label ready"*) printf '[{"number":7,"title":"dispatch me"}]' ;; *) printf '[]' ;; esac ;;
  "issue view")
    case "$*" in
      *"--json comments"*) printf '{"comments":[]}' ;;
      *)
        if [ -f "$CLOSED_MARK" ]; then st=CLOSED; else st=OPEN; fi
        printf '{"number":7,"title":"dispatch me","body":"do the thing","state":"%s","labels":%s}' "$st" "$(labels_json)" ;;
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
  "api user") printf 'octocat\n' ;;
  "api repos/o/r")
    case "$*" in
      *default_branch*) printf 'main\n' ;;
      *allow_auto_merge*) printf 'true\n' ;;
      *) echo "unexpected gh api: $*" >&2; exit 3 ;;
    esac ;;
  "api repos/o/r/branches/main/protection")
    printf '{"required_status_checks":{"contexts":["test"]},"required_pull_request_reviews":{}}' ;;
  "api repos/o/r/rules/branches/main") printf '[]' ;;
  "pr list") printf '9\n' ;;
  "pr view")
    case "$*" in
      *"--json files"*) printf 'internal/x.go\n' ;;
      *) printf '{"number":9,"state":"OPEN","mergeable":"MERGEABLE","mergeStateStatus":"CLEAN"}' ;;
    esac ;;
  "pr checks") printf '[{"name":"test","bucket":"pass","state":"SUCCESS"}]' ;;
  "pr merge") touch "$MERGED_MARK" ;;
  "auth status") : ;;
  *) echo "unexpected gh call: $*" >&2; exit 3 ;;
esac
EOF
cat > "$tmp/bin/claude" <<'EOF'
#!/bin/sh
# Headless agent stub: follow the completion definition's merge step.
for a in "$@"; do printf '%s\n' "$a" >> "$PROMPT_LOG"; done
"$LEAD_BIN" finish 7 --merge >> "$FINISH_LOG" 2>&1
EOF
chmod +x "$tmp/bin/gh" "$tmp/bin/claude"
export PATH="$tmp/bin:$PATH"

CGO_ENABLED=0 go build -o "$tmp/lead" ./cmd/lead || fail "go build failed"
ln -sf "$tmp/lead" "$tmp/bin/lead"
export LEAD_BIN="$tmp/lead"

# --- 1. CI 成功 → awaiting-human、blocked にしない・attempts を増やさない ---
out="$(cd "$repo" && "$tmp/lead" dispatch --once --parallel 1 --agent claude 2>&1)" \
  || fail "dispatch exited non-zero: $out"

case "$out" in *"#7 awaiting_human"*) ;; *) fail "dispatch did not report awaiting_human: $out";; esac
case "$out" in *"#7 blocked"*) fail "awaiting-human was reported as blocked: $out";; esac
grep -qx 'awaiting-human' "$LABELS_FILE" || fail "awaiting-human label was not added: $(cat "$LABELS_FILE")"
grep -q -- '--add-label blocked' "$GH_LOG" && fail "blocked label was added: $(cat "$GH_LOG")"
[ -f "$MERGED_MARK" ] && fail "risk:b PR was merged"
[ -f "$CLOSED_MARK" ] && fail "issue was closed"
python3 -c "import json;d=json.load(open('$LEAD_STATE_FILE'));w=[x for x in d['workflows'] if x['issue']==7][0];assert w.get('attempts',0)==0,w;assert w['status']=='awaiting_review',w" \
  || fail "state must keep attempts 0 and status awaiting_review: $(cat "$LEAD_STATE_FILE")"
[ -d "$repo/.worktrees/issue-7" ] && fail "worktree not removed after awaiting-human"
git -C "$repo" worktree list | grep -q "issue-7" && fail "worktree still registered after awaiting-human"

# --- 2. 次のパスでは再 dispatch しない（attempts も増えない） ---
: > "$PROMPT_LOG"; : > "$GH_LOG"
out2="$(cd "$repo" && "$tmp/lead" dispatch --once --parallel 1 --agent claude 2>&1)" \
  || fail "second dispatch exited non-zero: $out2"
[ -s "$PROMPT_LOG" ] && fail "awaiting-human issue was dispatched again: $(cat "$PROMPT_LOG")"
grep -q -- '--add-label blocked' "$GH_LOG" && fail "second pass blocked the awaiting-human issue: $(cat "$GH_LOG")"
python3 -c "import json;d=json.load(open('$LEAD_STATE_FILE'));w=[x for x in d['workflows'] if x['issue']==7][0];assert w.get('attempts',0)==0,w;assert w['status']=='awaiting_review',w" \
  || fail "second pass changed the awaiting record: $(cat "$LEAD_STATE_FILE")"

[ "$HOME" = "$tmp/home" ] || fail "HOME isolation broken"

echo "dispatch awaiting-human checks passed"
