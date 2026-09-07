#!/usr/bin/env bash
# issue #37: 組み込みTUI picker の振る舞いテスト。
# LEAD_TEST_SELECTION フックで非TTY選択を駆動し、選択→作業・
# ブラウザopen・キャッシュ有効/無効の gh 呼び出し回数を検証する (grep検査なし)。
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
export XDG_CACHE_HOME="$tmp/cache"
unset LEAD_STATE_FILE || true
unset LEAD_TEST_SELECTION || true
mkdir -p "$HOME" "$XDG_CACHE_HOME"
command -v git >/dev/null || fail "git not available"

repo="$tmp/repo"
git init -q -b main "$repo"
git -C "$repo" config user.email "t@t"
git -C "$repo" config user.name "t"
echo x > "$repo/f.txt"
git -C "$repo" add .
git -C "$repo" commit -qm init

mkdir -p "$tmp/bin"
cat > "$tmp/bin/gh" <<'EOF'
#!/bin/sh
echo "GHLOG $@" >> "$GH_ARGS_LOG"
case "$1 $2" in
  "issue list")
    printf '[{"number":36,"title":"ports adapter"}]' ;;
  "issue view")
    if [ "$3" = "--web" ] || [ "$4" = "--web" ]; then :; else
      printf '{"number":36,"title":"ports adapter","body":"demo body","state":"OPEN"}'
    fi ;;
  *) echo "unexpected gh call: $@" >&2; exit 3 ;;
esac
EOF
chmod +x "$tmp/bin/gh"
export PATH="$tmp/bin:$PATH"
export GH_ARGS_LOG="$tmp/gh-args.log"
: > "$GH_ARGS_LOG"

CGO_ENABLED=0 go build -o "$tmp/lead" ./cmd/lead || fail "go build failed"
cd "$repo"

# --- 1. work (no number) with work selection: branch + state + cached preview ---
LEAD_TEST_SELECTION=36:agy "$tmp/lead" work || fail "TUI work selection failed"
git -C "$repo" rev-parse --verify --quiet refs/heads/issue/36-ports-adapter >/dev/null \
  || fail "work branch not created via picker"
[ -f "$XDG_CACHE_HOME/lead/issues/36.json" ] || fail "preview cache not written"
grep -q "GHLOG issue view 36 --json" "$GH_ARGS_LOG" || fail "preview did not fetch issue"

# --- 2. second run: preview served from cache (no new gh view call) ---
views_before=$(grep -c "GHLOG issue view 36 --json" "$GH_ARGS_LOG")
LEAD_TEST_SELECTION=36:agy "$tmp/lead" work >/dev/null || fail "re-work via picker failed"
views_after=$(grep -c "GHLOG issue view 36 --json" "$GH_ARGS_LOG")
[ "$views_after" = "$views_before" ] || fail "cache miss: gh view calls $views_before -> $views_after"

# --- 3. browser selection: --web call, no branch, no state ---
git -C "$repo" checkout -q main
git -C "$repo" branch -D issue/36-ports-adapter >/dev/null 2>&1 || true
rm -f "$HOME/.local/state/lead/workflows.json"
LEAD_TEST_SELECTION=36:browser "$tmp/lead" work | grep -q "browser" \
  || fail "browser selection missing notice"
grep -q "GHLOG issue view 36 --web" "$GH_ARGS_LOG" || fail "browser open not invoked"
git -C "$repo" rev-parse --verify --quiet refs/heads/issue/36-ports-adapter >/dev/null 2>&1 \
  && fail "browse created a branch"
[ ! -f "$HOME/.local/state/lead/workflows.json" ] || fail "browse recorded state"

# --- 4. invalid hook value: non-zero, TTY untouched ---
LEAD_TEST_SELECTION=bogus "$tmp/lead" work >/dev/null 2>&1 \
  && fail "invalid selection exited 0"

[ "$HOME" = "$tmp/home" ] || fail "HOME isolation broken"

echo "tui picker behavioral checks passed"
