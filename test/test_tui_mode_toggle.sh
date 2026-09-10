#!/usr/bin/env bash
set -euo pipefail

# test_tui_mode_toggle.sh: verifies keyboard toggling of agent mode and agent in TUI (issue #144).
# Asserts that 'm' cycles agent mode, 'g' cycles agent, config is persisted,
# and settings reload on next launch.

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
cat > "$state_dir/inbox-seen.json" <<'EOF'
{"last_seen_at":"2026-09-11T00:00:00Z","confirmed":[],"help_shown":true}
EOF

mkdir -p "$tmp/bin"
cat > "$tmp/bin/gh" <<'EOF'
#!/bin/sh
case "$*" in
  *"--jq .default_branch"*) echo "main" ;;
  *"--jq .allow_auto_merge"*) echo "true" ;;
  *"branches/main/protection"*)
    printf '{"required_status_checks":{"contexts":["ci"]},"required_pull_request_reviews":{}}'
    ;;
  *"rules/branches/main"*) printf '[]' ;;
  *"api repos/"*) printf '{"default_branch":"main","allow_auto_merge":true}' ;;
  *"issue list"*) printf '[]' ;;
  *) printf '[]' ;;
esac
EOF
chmod +x "$tmp/bin/gh"

export PATH="$tmp/bin:$PATH"
CGO_ENABLED=0 go build -o "$tmp/lead" ./cmd/lead || fail "go build failed"

cd "$repo"

# 1. Initial launch: defaults to Mode: batch and Agent: agy
out="$(LEAD_TEST_INBOX_WIDTH=120 LEAD_TEST_INBOX_KEYS=q "$tmp/lead" 2>&1)" || fail "lead exited non-zero: $out"
case "$out" in
  *"[m] Mode: batch"*) ;;
  *) fail "initial screen missing '[m] Mode: batch': $out" ;;
esac
case "$out" in
  *"[g] Agent: agy"*) ;;
  *) fail "initial screen missing '[g] Agent: agy': $out" ;;
esac

# 2. Press 'm' to toggle mode to dangerous, then quit
out="$(LEAD_TEST_INBOX_WIDTH=120 LEAD_TEST_INBOX_KEYS=m,q "$tmp/lead" 2>&1)" || fail "toggle m failed: $out"
case "$out" in
  *"[m] Mode: dangerous"*) ;;
  *) fail "screen after 'm' missing '[m] Mode: dangerous': $out" ;;
esac

# Verify config persistence
cfg_file="$state_dir/inbox-config.json"
[ -f "$cfg_file" ] || fail "inbox-config.json was not created"
grep -q '"agent_mode": "dangerous"' "$cfg_file" || fail "inbox-config.json missing agent_mode dangerous: $(cat "$cfg_file")"

# 3. Press 'g' to toggle agent to claude, then quit
out="$(LEAD_TEST_INBOX_WIDTH=120 LEAD_TEST_INBOX_KEYS=g,q "$tmp/lead" 2>&1)" || fail "toggle g failed: $out"
case "$out" in
  *"[g] Agent: claude"*) ;;
  *) fail "screen after 'g' missing '[g] Agent: claude': $out" ;;
esac
grep -q '"agent": "claude"' "$cfg_file" || fail "inbox-config.json missing agent claude: $(cat "$cfg_file")"

# 4. Re-launch without key inputs (q only): settings are restored
out="$(LEAD_TEST_INBOX_WIDTH=120 LEAD_TEST_INBOX_KEYS=q "$tmp/lead" 2>&1)" || fail "re-launch failed: $out"
case "$out" in
  *"[m] Mode: dangerous"*) ;;
  *) fail "reloaded screen missing '[m] Mode: dangerous': $out" ;;
esac
case "$out" in
  *"[g] Agent: claude"*) ;;
  *) fail "reloaded screen missing '[g] Agent: claude': $out" ;;
esac

echo "tui agent and mode toggle behavioral checks passed"
