#!/usr/bin/env bash
# issue #250: 128 KiB (MAX_ARG_STRLEN) を超える diff でも `lead review` が
# 起動できる。プロンプトは argv ではなく一時ファイルで渡し、エージェントの
# argv が肥大化しないことを検証する（grep によるソース検査なし・一時 HOME/リポジトリ）。
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

export GH_LOG="$tmp/gh.log" COMMENT_FILE="$tmp/comment.md" LABELS_FILE="$tmp/labels.txt"
export ARGV_MAX_FILE="$tmp/argv-max.txt" PROMPT_SIZE_FILE="$tmp/prompt-size.txt"
export PROMPT_COPY="$tmp/prompt.md" DIFF_FILE="$tmp/diff.txt"
: > "$GH_LOG"; : > "$COMMENT_FILE"; : > "$LABELS_FILE"
python3 - "$DIFF_FILE" <<'PY'
import sys
with open(sys.argv[1], "w") as f:
    f.write("diff --git a/big.txt b/big.txt\n")
    f.write("+" + "x" * 200000 + "\n")
PY
[ "$(wc -c < "$DIFF_FILE")" -gt 131072 ] || fail "fixture diff is not larger than MAX_ARG_STRLEN"

export AGENT_REPLY_FILE="$tmp/reply.md"
cat > "$AGENT_REPLY_FILE" <<'EOF'
## 所見
- 巨大 diff を確認した

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
      *"--json headRefName"*) printf '%s' '{"headRefName":"issue/250-review-large-diff"}' ;;
      *"--json headRefOid"*) printf '{"headRefOid":"%s"}' "$PR_SHA" ;;
      *) printf '%s' '{"number":7,"state":"OPEN","mergeable":"MERGEABLE","mergeStateStatus":"CLEAN"}' ;;
    esac ;;
  "pr diff") cat "$DIFF_FILE" ;;
  "pr checks") printf '%s' '[{"name":"test","bucket":"pass","state":"SUCCESS"}]' ;;
  "pr comment") cat > "$COMMENT_FILE" ;;
  "issue view") printf '%s' '{"number":250,"title":"large diff","body":"## 受入条件\n- large diff","state":"OPEN","labels":[]}' ;;
  "issue edit")
    case "$*" in
      *--add-label*) printf '%s\n' "$5" >> "$LABELS_FILE" ;;
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
max=0
last=""
for a in "$@"; do
  n=${#a}
  [ "$n" -gt "$max" ] && max=$n
  last="$a"
done
printf '%s\n' "$max" > "$ARGV_MAX_FILE"
case "$last" in
  *"file: "*)
    f="${last##*file: }"
    wc -c < "$f" > "$PROMPT_SIZE_FILE"
    cp "$f" "$PROMPT_COPY" ;;
esac
cat "$AGENT_REPLY_FILE"
EOF
chmod +x "$tmp/bin/gh" "$tmp/bin/claude"
export PATH="$tmp/bin:$PATH"

CGO_ENABLED=0 go build -o "$tmp/lead" ./cmd/lead || fail "go build failed"

out="$(cd "$repo" && "$tmp/lead" review 7 --agent claude 2>&1)" \
  || fail "review with a 200 KiB diff failed to start: $out"

max="$(cat "$ARGV_MAX_FILE")"
[ "$max" -lt 131072 ] || fail "agent argv reached $max bytes (MAX_ARG_STRLEN is 131072)"
size="$(cat "$PROMPT_SIZE_FILE")"
[ "$size" -gt 131072 ] || fail "prompt file is only $size bytes; want the full large diff"
grep -q '^diff --git a/big.txt b/big.txt' "$PROMPT_COPY" || fail "prompt file missing the diff header"
grep -qx 'review:pass' "$LABELS_FILE" || fail "review:pass was not applied"
grep -qF -- "<!-- lead-review sha=$PR_SHA verdict=pass scenarios=0/0 -->" "$COMMENT_FILE" \
  || fail "PR comment missing the marker: $(cat "$COMMENT_FILE")"

[ "$HOME" = "$tmp/home" ] || fail "HOME isolation broken"

echo "review large-diff behavioral checks passed"
