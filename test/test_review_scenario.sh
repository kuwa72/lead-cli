#!/usr/bin/env bash
# issue #277: シナリオの合否を review の主条件にする振る舞いテスト。
# ダミー gh・ダミーエージェントで、レビュー出力の `SCENARIOS: n/m` が
# verdict を決めることを検証する（シナリオ未設定の Issue は 0/0 で diff のみ）。
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
export AGENT_REPLY_FILE="$tmp/reply.md"
: > "$GH_LOG"; : > "$COMMENT_FILE"; : > "$LABELS_FILE"; : > "$AGENT_REPLY_FILE"

mkdir -p "$tmp/bin"
cat > "$tmp/bin/gh" <<'EOF'
#!/bin/sh
echo "GH $*" >> "$GH_LOG"
case "$1 $2" in
  "pr view")
    case "$*" in
      *"--json labels"*) cat "$LABELS_FILE" ;;
      *"--json body"*) printf '%s' '{"body":"Closes #277"}' ;;
      *"--json headRefName"*) printf '%s' '{"headRefName":"issue/277-feat-review"}' ;;
      *"--json headRefOid"*) printf '{"headRefOid":"%s"}' "$PR_SHA" ;;
      *) printf '%s' '{"number":7,"state":"OPEN","mergeable":"MERGEABLE","mergeStateStatus":"CLEAN"}' ;;
    esac ;;
  "pr diff") printf '%s' 'diff --git a/x b/x
+package x' ;;
  "pr checks") printf '%s' '[{"name":"test","bucket":"pass","state":"SUCCESS"}]' ;;
  "pr comment") cat > "$COMMENT_FILE" ;;
  "issue view") printf '%s' '{"number":277,"title":"feat(review): scenario","body":"## 受入条件\n- review:pass","state":"OPEN","labels":[]}' ;;
  "issue edit")
    case "$*" in
      *--add-label*) printf '%s
' "$5" >> "$LABELS_FILE" ;;
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
cat "$AGENT_REPLY_FILE"
EOF
chmod +x "$tmp/bin/gh" "$tmp/bin/claude"
export PATH="$tmp/bin:$PATH"

CGO_ENABLED=0 go build -o "$tmp/lead" ./cmd/lead || fail "go build failed"

reset_run() { : > "$GH_LOG"; : > "$COMMENT_FILE"; : > "$LABELS_FILE"; }

# --- 1. SCENARIOS: 1/2 + VERDICT: pass → needs-work ---------------------------
cat > "$AGENT_REPLY_FILE" <<'EOF'
## 所見
- diff に重大な指摘なし

SCENARIOS: 1/2
VERDICT: pass
EOF
reset_run
if out="$(cd "$repo" && "$tmp/lead" review 7 --agent claude 2>&1)"; then
  fail "scenario 1/2 was accepted as pass: $out"
fi
grep -qx 'review:needs-work' "$LABELS_FILE" || fail "review:needs-work label missing: labels=$(cat "$LABELS_FILE") gh=$(cat "$GH_LOG")"
grep -q 'SCENARIOS: 1/2' "$COMMENT_FILE" || fail "comment missing the scenario score: $(cat "$COMMENT_FILE")"
grep -qF -- "verdict=needs-work scenarios=1/2" "$COMMENT_FILE" || fail "marker missing needs-work 1/2"

# --- 2. SCENARIOS: 2/2 + VERDICT: pass → pass --------------------------------
cat > "$AGENT_REPLY_FILE" <<'EOF'
## 所見
- 問題なし

SCENARIOS: 2/2
VERDICT: pass
EOF
reset_run
out="$(cd "$repo" && "$tmp/lead" review 7 --agent claude 2>&1)" || fail "scenario 2/2 did not pass: $out"
grep -qx 'review:pass' "$LABELS_FILE" || fail "review:pass label missing: $(cat "$LABELS_FILE")"
grep -q 'SCENARIOS: 2/2' "$COMMENT_FILE" || fail "comment missing the scenario score: $(cat "$COMMENT_FILE")"

# --- 3. no scenario → SCENARIOS: 0/0 and the diff verdict decides ------------
cat > "$AGENT_REPLY_FILE" <<'EOF'
## 所見
- 問題なし

VERDICT: pass
EOF
reset_run
out="$(cd "$repo" && "$tmp/lead" review 7 --agent claude 2>&1)" || fail "no-scenario review did not pass: $out"
grep -q 'SCENARIOS: 0/0' "$COMMENT_FILE" || fail "comment missing 0/0: $(cat "$COMMENT_FILE")"

[ "$HOME" = "$tmp/home" ] || fail "HOME isolation broken"

echo "review scenario behavioral checks passed"
