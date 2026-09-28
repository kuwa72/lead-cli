#!/usr/bin/env bash
# issue #225: herdr pane/tab integration removed. With HERDR_ENV=1 and a
# logging dummy herdr on PATH, `lead run`/`lead resume` must print
# `cd <dir> && <agent cmd>` on stdout without invoking herdr, and inbox `p`
# must open the agent log via $PAGER (lead built-in log display).
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
export LEAD_STATE_FILE="$tmp/state/workflows.json"
mkdir -p "$HOME" "$tmp/state"

repo="$tmp/repo"
git init -q -b main "$repo" || fail "git init failed"
git -C "$repo" config user.email "t@t"
git -C "$repo" config user.name "t"
git -C "$repo" remote add origin git@github.com:acme/widgets.git
echo x > "$repo/f.txt"
git -C "$repo" add . && git -C "$repo" commit -qm init

export HERDR_LOG="$tmp/herdr.log" PAGER_LOG="$tmp/pager.log"
: > "$HERDR_LOG"; : > "$PAGER_LOG"

mkdir -p "$tmp/bin"
cat > "$tmp/bin/gh" <<'EOF'
#!/bin/sh
case "$1 $2" in
  "issue list")
    case "$*" in
      *"--label needs-review"*) printf '[{"number":7,"title":"spec: a"},{"number":8,"title":"spec: b"}]' ;;
      *"--label blocked"*) printf '[{"number":9,"title":"impl: stuck"}]' ;;
      *) printf '[]' ;;
    esac ;;
  "issue view")
    printf '{"number":36,"title":"ports adapter","body":"b","state":"OPEN"}' ;;
  "pr view") echo "pr not found: $3" >&2; exit 1 ;;
  *) echo "unexpected gh call: $*" >&2; exit 3 ;;
esac
EOF
# Dummy herdr: every invocation is logged; the test asserts the log stays empty.
cat > "$tmp/bin/herdr" <<'EOF'
#!/bin/sh
for a in "$@"; do printf '<%s>\n' "$a" >> "$HERDR_LOG"; done
EOF
cat > "$tmp/bin/less" <<'EOF'
#!/bin/sh
for a in "$@"; do printf '<%s>\n' "$a" >> "$PAGER_LOG"; done
EOF
chmod +x "$tmp/bin/"*
export PATH="$tmp/bin:$PATH"

CGO_ENABLED=0 go build -o "$tmp/lead" ./cmd/lead || fail "go build failed"
cd "$repo"

# --- 1. work under HERDR_ENV=1: inline command on stdout, herdr silent ------
out="$(HERDR_ENV=1 "$tmp/lead" run 36 2>&1)" || fail "lead run 36 failed: $out"
echo "$out" | grep -F "cd \"$repo\" && agy -i \"" >/dev/null \
  || fail "work stdout missing inline agent command: $out"
[ ! -s "$HERDR_LOG" ] || fail "work invoked herdr: $(cat "$HERDR_LOG")"

# --- 2. HERDR_ENV unset: identical inline behavior ----------------------------
out2="$(env -u HERDR_ENV "$tmp/lead" run 36 2>&1)" || fail "re-work failed: $out2"
echo "$out2" | grep -F "cd \"$repo\" && agy -i \"" >/dev/null \
  || fail "work without HERDR_ENV differs: $out2"
[ ! -s "$HERDR_LOG" ] || fail "herdr invoked: $(cat "$HERDR_LOG")"

# --- 3. resume under HERDR_ENV=1: inline command, herdr silent ----------------
out="$(HERDR_ENV=1 "$tmp/lead" resume 36 2>&1)" || fail "lead resume 36 failed: $out"
echo "$out" | grep -F "cd \"$repo\" && agy -i \"" >/dev/null \
  || fail "resume stdout missing inline agent command: $out"
[ ! -s "$HERDR_LOG" ] || fail "resume invoked herdr: $(cat "$HERDR_LOG")"

# --- 4. inbox p under HERDR_ENV=1: pager opens the log, herdr silent ---------
cat > "$LEAD_STATE_FILE" <<'EOF'
{"version":1,"workflows":[{"issue":9,"status":"blocked","agent":"claude","attempts":3,"log_path":"/logs/issue-9.log","branch":"issue/9-stuck"}]}
EOF
cat > "$tmp/state/inbox-seen.json" <<'EOF'
{"help_shown":true}
EOF
HERDR_ENV=1 PAGER=less LEAD_TEST_INBOX_KEYS='j,j,j,p,q' "$tmp/lead" >/dev/null 2>&1 \
  || fail "headless p failed"
grep -qxF '</logs/issue-9.log>' "$PAGER_LOG" \
  || fail "p did not open the log via pager: $(cat "$PAGER_LOG")"
[ ! -s "$HERDR_LOG" ] || fail "inbox p invoked herdr: $(cat "$HERDR_LOG")"

echo "PASS: no herdr integration (inline work/resume + pager peek)"
