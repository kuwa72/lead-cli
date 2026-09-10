#!/usr/bin/env bash
set -euo pipefail

# test_agent_mode.sh: verifies --agent-mode flag behavior (issue #83).
# Asserts agent command string in prepared output and state persistence.

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
echo x > "$repo/f.txt"
git -C "$repo" add .
git -C "$repo" commit -qm init || fail "fixture commit failed"

mkdir -p "$tmp/bin"
cat > "$tmp/bin/gh" <<'EOF'
#!/bin/sh
if [ "$1 $2" = "issue view" ]; then
  printf '{"number":83,"title":"agent mode test","body":"test body","state":"OPEN"}'
else
  echo "unexpected gh call: $@" >&2; exit 3
fi
EOF
chmod +x "$tmp/bin/gh"

export PATH="$tmp/bin:$PATH"
CGO_ENABLED=0 go build -o "$tmp/lead" ./cmd/lead || fail "go build failed"
cd "$repo"

STATE="$HOME/.local/state/lead/workflows.json"

# 1. Test default agent (agy) with --agent-mode batch
out_batch=$("$tmp/lead" work 83 --agent-mode batch)
echo "$out_batch" | grep -F -- "--dangerously-skip-permissions" > /dev/null || fail "missing --dangerously-skip-permissions in batch output"
echo "$out_batch" | grep -F -- " -p " > /dev/null || fail "missing -p in batch output"

# Verify state recorded agent_mode: batch
python3 -c "import json; d=json.load(open('$STATE')); assert d['workflows'][0]['agent_mode'] == 'batch', d['workflows'][0]" || fail "state agent_mode != batch"

# 2. Test claude with --agent-mode dangerous
"$tmp/lead" clean 83 > /dev/null 2>&1 || true
out_claude=$("$tmp/lead" work 83 --agent claude --agent-mode dangerous)
echo "$out_claude" | grep -F -- "claude -p --dangerously-skip-permissions" > /dev/null || fail "missing claude batch flags"
python3 -c "import json; d=json.load(open('$STATE')); assert d['workflows'][0]['agent_mode'] == 'dangerous', d['workflows'][0]" || fail "state agent_mode != dangerous"

# 3. Test default (interactive) mode
"$tmp/lead" clean 83 > /dev/null 2>&1 || true
out_interactive=$("$tmp/lead" work 83 --agent agy --agent-mode interactive)
echo "$out_interactive" | grep -F -- "agy -i " > /dev/null || fail "missing agy -i in interactive output"
python3 -c "import json; d=json.load(open('$STATE')); assert d['workflows'][0].get('agent_mode', 'interactive') == 'interactive', d['workflows'][0]" || fail "state agent_mode != interactive"

echo "PASS: agent mode behavioral checks passed"
