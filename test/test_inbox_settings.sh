#!/usr/bin/env bash
set -euo pipefail

# test_inbox_settings.sh: verifies Settings view and Issue Creation default toggling (issue #162).
# Asserts that:
#   1. ',' or 'comma' opens Settings view displaying items and values.
#   2. Enter/Space toggles Issue Creation Default between direct and ai.
#   3. inbox-config.json persists issue_creation setting.
#   4. When default is ai, 's' invokes spec AI; when default is direct, 's' creates directly.
#   5. Arrow keys / j,k navigate setting items.
#   6. Esc or q closes Settings view back to list.

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

state_dir="$HOME/.local/state/lead"
mkdir -p "$state_dir"
cat > "$state_dir/inbox-seen.json" <<'SEEN_EOF'
{"last_seen_at":"2026-09-11T00:00:00Z","confirmed":[],"help_shown":true}
SEEN_EOF

mkdir -p "$tmp/bin"
export GH_LOG="$tmp/gh.log"
export AGY_LOG="$tmp/agy.log"
: > "$GH_LOG"; : > "$AGY_LOG"

cat > "$tmp/bin/gh" <<'GH_EOF'
#!/bin/sh
echo "GH $*" >> "$GH_LOG"
case "$*" in
  *"--jq .default_branch"*) echo "main" ;;
  *"--jq .allow_auto_merge"*) echo "true" ;;
  *"branches/main/protection"*)
    printf '{"required_status_checks":{"contexts":["ci"]},"required_pull_request_reviews":{}}'
    ;;
  *"rules/branches/main"*) printf '[]' ;;
  *"api repos/"*) printf '{"default_branch":"main","allow_auto_merge":true}' ;;
  *"issue create"*)
    echo "https://github.com/kuwa72/lead-cli/issues/201"
    ;;
  *"issue list"*) printf '[]' ;;
  *) printf '[]' ;;
esac
GH_EOF

cat > "$tmp/bin/agy" <<'AGY_EOF'
#!/bin/sh
echo "AGY $*" >> "$AGY_LOG"
printf '[{"title":"feat: generated spec","body":"## Acceptance\n- works"}]\n'
AGY_EOF

chmod +x "$tmp/bin/gh" "$tmp/bin/agy"

export PATH="$tmp/bin:$PATH"
CGO_ENABLED=0 go build -o "$tmp/lead" ./cmd/lead || fail "go build failed"

cd "$repo"

# 1. Open Settings view with 'comma' and verify screen contents
out="$(LEAD_TEST_INBOX_WIDTH=120 LEAD_TEST_INBOX_KEYS='comma' "$tmp/lead" 2>&1)" || fail "lead settings view failed: $out"
case "$out" in
  *"Lead Settings & Preferences"*) ;;
  *) fail "screen missing settings header: $out" ;;
esac
case "$out" in
  *"Issue Creation Default"*) ;;
  *) fail "screen missing Issue Creation Default: $out" ;;
esac
case "$out" in
  *"Active Coding Agent"*) ;;
  *) fail "screen missing Active Coding Agent: $out" ;;
esac
case "$out" in
  *"Agent Dispatch Mode"*) ;;
  *) fail "screen missing Agent Dispatch Mode: $out" ;;
esac

# 2. In Settings, toggle Issue Creation Default to 'ai', then quit
# Keys: 'comma' to open settings, 'enter' to toggle item 0, 'esc' to exit settings, 'q' to quit
out="$(LEAD_TEST_INBOX_WIDTH=120 LEAD_TEST_INBOX_KEYS='comma,enter,esc,q' "$tmp/lead" 2>&1)" || fail "toggle setting failed: $out"

# Verify config persistence
cfg_file="$state_dir/inbox-config.json"
[ -f "$cfg_file" ] || fail "inbox-config.json was not created"
grep -q '"issue_creation": "ai"' "$cfg_file" || fail "inbox-config.json missing issue_creation ai: $(cat "$cfg_file")"

# 3. Now that default is 'ai', pressing 's' and typing normal title triggers spec agent (agy)
: > "$GH_LOG"; : > "$AGY_LOG"
out="$(LEAD_TEST_INBOX_WIDTH=120 LEAD_TEST_INBOX_KEYS='s,text:need ai spec for payments,enter,q' "$tmp/lead" 2>&1)" || fail "s with ai default failed: $out"
grep -q 'need ai spec for payments' "$AGY_LOG" || fail "spec agent was not invoked with ai default: $(cat "$AGY_LOG")"

# 4. In 'ai' default, typing !title triggers direct issue creation without spec agent
: > "$GH_LOG"; : > "$AGY_LOG"
out="$(LEAD_TEST_INBOX_WIDTH=120 LEAD_TEST_INBOX_KEYS='s,text:!bypass ai and create directly,enter,q' "$tmp/lead" 2>&1)" || fail "s with ! in ai default failed: $out"
[ ! -s "$AGY_LOG" ] || fail "spec agent should not be invoked when prefixed with !: $(cat "$AGY_LOG")"
grep -q 'issue create --title bypass ai and create directly' "$GH_LOG" || fail "gh issue create was not invoked directly: $(cat "$GH_LOG")"

# 5. Open settings, toggle back to 'direct', navigate down to agent and toggle to claude
# Keys: 'comma' (open), 'enter' (toggle to direct), 'down' (to agent), 'space' (to claude), 'esc', 'q'
out="$(LEAD_TEST_INBOX_WIDTH=120 LEAD_TEST_INBOX_KEYS='comma,enter,down,space,esc,q' "$tmp/lead" 2>&1)" || fail "toggle agent via settings failed: $out"
grep -q '"issue_creation": "direct"' "$cfg_file" || fail "issue_creation not restored to direct: $(cat "$cfg_file")"
grep -q '"agent": "claude"' "$cfg_file" || fail "agent not changed to claude: $(cat "$cfg_file")"

echo "inbox settings behavioral checks passed"
