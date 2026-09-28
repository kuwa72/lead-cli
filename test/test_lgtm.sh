#!/usr/bin/env bash
# issue #89: lead lgtm / unlgtm および lead run --mode review の振る舞いテスト。
# ソースコードのgrepではなく、ダミーghへの引数ログと標準出力・終了ステータスをアサートする。
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
export GH_LOG="$tmp/gh.log"
: > "$GH_LOG"
mkdir -p "$HOME" "$tmp/bin"

cat > "$tmp/bin/gh" <<'GHEOF'
#!/bin/sh
echo "GH $*" >> "$GH_LOG"
case "$1 $2" in
  "issue edit"|"issue comment") exit 0 ;;
  "issue view")
    printf '{"number":42,"title":"review target","body":"review body text","state":"OPEN"}'
    ;;
  "issue list")
    case "$*" in
      *"--label needs-review"*) printf '[{"number":42,"title":"needs review issue"}]' ;;
      *"--label lgtm"*) printf '[{"number":10,"title":"ready issue"}]' ;;
      *) printf '[{"number":42,"title":"fallback open"}]' ;;
    esac
    ;;
  *) echo "unexpected gh call: $*" >&2; exit 3 ;;
esac
GHEOF
chmod +x "$tmp/bin/gh"

export PATH="$tmp/bin:$PATH"

CGO_ENABLED=0 go build -o "$tmp/lead" ./cmd/lead || fail "go build failed"

# 1. lead lgtm 42
: > "$GH_LOG"
"$tmp/lead" lgtm 42 || fail "lead lgtm 42 failed"
grep -q "issue edit 42 --add-label lgtm" "$GH_LOG" || fail "lgtm label not added: $(cat "$GH_LOG")"
grep -q "issue comment 42 --body LGTM" "$GH_LOG" || fail "LGTM comment not posted: $(cat "$GH_LOG")"

# 2. lead unlgtm 42
: > "$GH_LOG"
"$tmp/lead" unlgtm 42 || fail "lead unlgtm 42 failed"
grep -q "issue edit 42 --remove-label lgtm" "$GH_LOG" || fail "lgtm label not removed: $(cat "$GH_LOG")"

# 3. 引数エラー
"$tmp/lead" lgtm >/dev/null 2>&1 && fail "lead lgtm without args should fail"
"$tmp/lead" lgtm abc >/dev/null 2>&1 && fail "lead lgtm with invalid number should fail"
"$tmp/lead" unlgtm >/dev/null 2>&1 && fail "lead unlgtm without args should fail"
"$tmp/lead" unlgtm abc >/dev/null 2>&1 && fail "lead unlgtm with invalid number should fail"

# 4. lead run --mode review 42: review prompt passed to agent
repo="$tmp/repo"
git init -q -b main "$repo"
git -C "$repo" config user.email "t@t"
git -C "$repo" config user.name "t"
echo "# rules" > "$repo/AGENTS.md"
git -C "$repo" add . && git -C "$repo" commit -qm init
cd "$repo"

out="$("$tmp/lead" run --mode review 42)" || fail "lead run --mode review 42 failed"
echo "$out" | grep -qF "lead lgtm 42" || fail "review prompt with lgtm instruction not printed: $out"
echo "$out" | grep -qF "review body text" || fail "review body not printed: $out"

echo "lgtm and review workflow behavioral checks passed"
