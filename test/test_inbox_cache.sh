#!/usr/bin/env bash
set -euo pipefail

# test_inbox_cache.sh: verifies persistent issue caching and offline fallback (issue #140).
# Asserts that issues are cached to disk and reloaded when offline.

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

fail() { echo "FAIL: $*" >&2; exit 1; }

REAL_GOPATH="$(go env GOPATH)"
REAL_GOCACHE="$(go env GOCACHE)"
export HOME="$tmp/home"
export GOPATH="$REAL_GOPATH" GOCACHE="$REAL_GOCACHE"
if [ -d "/home/linuxbrew/.linuxbrew/bin" ]; then
  export PATH="/home/linuxbrew/.linuxbrew/bin:$PATH"
fi
unset XDG_STATE_HOME || true
unset LEAD_STATE_FILE || true
unset HERDR_ENV || true
mkdir -p "$HOME"

repo="$tmp/repo"
git init -q -b main "$repo" || fail "git init failed"
git -C "$repo" config user.email "t@t"
git -C "$repo" config user.name "t"
git -C "$repo" remote add origin https://github.com/kuwa72/lead-cli.git
echo x > "$repo/f.txt"
git -C "$repo" add .
git -C "$repo" commit -qm init || fail "fixture commit failed"

mkdir -p "$tmp/bin"
cat > "$tmp/bin/gh" <<'EOF'
#!/bin/sh
case "$*" in
  *"issue list"*"needs-review"*)
    printf '[{"number":140,"title":"cache test issue","updatedAt":"2026-09-11T00:00:00Z"}]'
    ;;
  *"issue list"*"blocked"*)
    printf '[]'
    ;;
  *"issue list"*"closed"*)
    printf '[]'
    ;;
  *"issue list"*"open"*)
    printf '[{"number":140,"title":"cache test issue"}]'
    ;;
  *)
    exit 0
    ;;
esac
EOF
chmod +x "$tmp/bin/gh"

export PATH="$tmp/bin:$PATH"
CGO_ENABLED=0 go build -o "$tmp/lead" ./cmd/lead || fail "go build failed"
cd "$repo"

CACHE_DIR="$HOME/.local/state/lead/cache"

# 1. Run inbox with mock gh, then immediately quit.
LEAD_TEST_INBOX_KEYS="q" "$tmp/lead" || fail "first lead run failed"

# Verify cache file exists
cache_file="$CACHE_DIR/kuwa72_lead-cli.json"
[ -f "$cache_file" ] || fail "cache file not created at $cache_file"

python3 -c "
import json
d = json.load(open('$cache_file'))
assert d['repo'] == 'kuwa72/lead-cli', d
assert len(d['review']) == 1 and d['review'][0]['number'] == 140, d
" || fail "cache file contents invalid"

# 2. Break gh CLI (simulate network failure/offline)
cat > "$tmp/bin/gh" <<'EOF'
#!/bin/sh
echo "GitHub API unavailable" >&2
exit 1
EOF
chmod +x "$tmp/bin/gh"

# 3. Run inbox in offline mode. It should load cached issues and exit cleanly on q.
out_offline=$(LEAD_TEST_INBOX_KEYS="q" "$tmp/lead" 2>&1) || fail "offline lead run failed"

# Ensure cached issue was rendered
echo "$out_offline" | grep -F "cache test issue" > /dev/null || fail "cached issue not rendered in offline mode"

echo "PASS: inbox cache behavioral checks passed"
