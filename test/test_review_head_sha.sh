#!/usr/bin/env bash
# issue #249: review:pass を head SHA に束縛する。
# ダミー gh・ダミーエージェントで実際に `lead review` を走らせ、
#   - レビュー後に head が進むと `lead finish --merge` は自動マージせず
#     「再レビューが必要」と表示する（exit 0）
#   - 新しい head で再レビューして pass が一致するとマージする
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

export LEAD_STATE_FILE="$tmp/state/workflows.json"
mkdir -p "$(dirname "$LEAD_STATE_FILE")"

repo="$tmp/repo"
git init -q -b main "$repo" || fail "git init failed"
git -C "$repo" config user.email "t@t"
git -C "$repo" config user.name "t"
echo x > "$repo/f.txt"
git -C "$repo" add . && git -C "$repo" commit -qm init
git -C "$repo" remote add origin https://github.com/o/r.git

export GH_LOG="$tmp/gh.log" COMMENT_FILE="$tmp/comments.txt" MARKER_FILE="$tmp/markers.txt"
export HEAD_SHA_FILE="$tmp/head-sha.txt" LABELS_FILE="$tmp/labels.txt"
export MERGED_MARK="$tmp/merged" CLOSED_MARK="$tmp/closed"
: > "$GH_LOG"; : > "$COMMENT_FILE"; : > "$MARKER_FILE"; : > "$LABELS_FILE"
rm -f "$MERGED_MARK" "$CLOSED_MARK"

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
  "issue view")
    if [ -f "$CLOSED_MARK" ]; then st=CLOSED; else st=OPEN; fi
    case "$3" in
      249) printf '%s' '{"number":249,"title":"bind review","body":"## 受入条件\n- sha","state":"OPEN","labels":[]}' ;;
      *) printf '{"number":36,"title":"demo","body":"b","state":"%s","labels":[{"name":"risk:a"}]}' "$st" ;;
    esac ;;
  "issue close") touch "$CLOSED_MARK" ;;
  "issue edit")
    case "$*" in
      *--add-label*) printf '%s\n' "$5" >> "$LABELS_FILE" ;;
      *--remove-label*)
        grep -vxF "$5" "$LABELS_FILE" > "$LABELS_FILE.tmp" || true
        mv "$LABELS_FILE.tmp" "$LABELS_FILE" ;;
      *) : ;;
    esac ;;
  "issue comment"|"auth status") : ;;
  "pr view")
    case "$*" in
      *"--json body"*) printf '%s' '{"body":"Closes #249"}' ;;
      *"--json headRefName"*) printf '%s' '{"headRefName":"issue/249-review-sha"}' ;;
      *"--json headRefOid"*) printf '{"headRefOid":"%s"}' "$(cat "$HEAD_SHA_FILE")" ;;
      *"--json labels"*) cat "$LABELS_FILE" ;;
      *"--json files"*) printf 'internal/x.go\n' ;;
      *"--json comments"*)
        if [ -s "$MARKER_FILE" ]; then
          printf '{"comments":['
          first=1
          while IFS= read -r m; do
            [ -n "$m" ] || continue
            [ "$first" = 1 ] || printf ','
            first=0
            printf '{"author":{"login":"lead"},"body":"%s","createdAt":"2026-10-25T00:00:00Z"}' "$m"
          done < "$MARKER_FILE"
          printf ']}'
        else
          printf '{"comments":[]}'
        fi ;;
      *) printf '{"number":7,"state":"OPEN","mergeable":"MERGEABLE","mergeStateStatus":"CLEAN"}' ;;
    esac ;;
  "pr diff") printf 'diff --git a/internal/x.go b/internal/x.go\n+new' ;;
  "pr checks") printf '[{"name":"test","bucket":"pass","state":"SUCCESS"}]' ;;
  "pr comment")
    body="$(cat)"
    printf '%s\n' "$body" >> "$COMMENT_FILE"
    printf '%s\n' "$body" | grep '^<!-- lead-review ' >> "$MARKER_FILE" || true ;;
  "pr merge") touch "$MERGED_MARK" ;;
  "label list") printf '%s' '[{"name":"review:pass"},{"name":"review:needs-work"}]' ;;
  "label create") : ;;
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
cat > "$tmp/bin/claude" <<'EOF'
#!/bin/sh
cat "$AGENT_REPLY_FILE"
EOF
chmod +x "$tmp/bin/gh" "$tmp/bin/claude"
export PATH="$tmp/bin:$PATH"

CGO_ENABLED=0 go build -o "$tmp/lead" ./cmd/lead || fail "go build failed"

HEAD_A=""
HEAD_B=""

(cd "$repo" && "$tmp/lead" run 36 >/dev/null) || fail "lead run failed"
HEAD_A="$(git -C "$repo" rev-parse HEAD)"
printf '%s' "$HEAD_A" > "$HEAD_SHA_FILE"

# --- 1. HEAD_A でレビュー → pass マーカーが HEAD_A に束縛される ---
out="$(cd "$repo" && "$tmp/lead" review 7 --agent claude 2>&1)" \
  || fail "review at HEAD_A failed: $out"
grep -qF -- "<!-- lead-review sha=$HEAD_A verdict=pass scenarios=0/0 -->" "$COMMENT_FILE" \
  || fail "review comment missing the HEAD_A marker: $(cat "$COMMENT_FILE")"

# --- 2. review は助言: 0/0 の古い pass はマージを止めない ---
echo y >> "$repo/f.txt"
git -C "$repo" commit -qam head-b
HEAD_B="$(git -C "$repo" rev-parse HEAD)"
[ "$HEAD_A" != "$HEAD_B" ] || fail "fixture: HEAD_B equals HEAD_A"
printf '%s' "$HEAD_B" > "$HEAD_SHA_FILE"
: > "$GH_LOG"
out="$(cd "$repo" && "$tmp/lead" finish 36 --pr 7 --merge --close --timeout 60s --poll-interval 1s)" \
  || fail "stale-review finish exited non-zero: $out"
[ -f "$MERGED_MARK" ] || fail "stale-review: a 0/0 advisory marker blocked the merge"
[ -f "$CLOSED_MARK" ] || fail "stale-review: issue was not closed"
grep -qx "GH pr merge 7 --squash --delete-branch --match-head-commit $HEAD_B" "$GH_LOG" \
  || fail "stale-review: squash merge not invoked: $(cat "$GH_LOG")"

[ "$HOME" = "$tmp/home" ] || fail "HOME isolation broken"

echo "review head-SHA binding behavioral checks passed"
