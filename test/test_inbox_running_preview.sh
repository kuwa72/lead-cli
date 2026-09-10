#!/usr/bin/env bash
set -euo pipefail

# test_inbox_running_preview.sh: verifies real-time log preview and attach for running agents (issue #143).
# Asserts that In Progress issues show live agent logs in the preview pane and support peek/attach.

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

log_file="$tmp/agent-143.log"
cat > "$log_file" <<'EOF'
[agy] Initializing workspace
[agy] Step 1: reading issue requirements
[agy] Step 2: running TDD tests
[agy] Result: all checks passed!
EOF

state_dir="$HOME/.local/state/lead"
mkdir -p "$state_dir"
cat > "$state_dir/workflows.json" <<EOF
{
  "version": 1,
  "workflows": [
    {
      "repository": "https://github.com/kuwa72/lead-cli.git",
      "issue": 143,
      "mode": "implement",
      "status": "in_progress",
      "branch": "issue/143-preview-test",
      "agent": "agy",
      "pane": "p-143",
      "log_path": "$log_file",
      "updated_at": "2026-09-11T07:00:00Z"
    }
  ]
}
EOF

cat > "$state_dir/inbox-seen.json" <<'EOF'
{"last_seen_at":"2026-09-11T00:00:00Z","confirmed":[],"help_shown":true}
EOF

mkdir -p "$tmp/bin"
cat > "$tmp/bin/gh" <<'EOF'
#!/bin/sh
case "$*" in
  *"--jq .default_branch"*)
    echo "main"
    ;;
  *"--jq .allow_auto_merge"*)
    echo "true"
    ;;
  *"branches/main/protection"*)
    printf '{"required_status_checks":{"contexts":["ci"]},"required_pull_request_reviews":{}}'
    ;;
  *"rules/branches/main"*)
    printf '[]'
    ;;
  *"api repos/"*)
    printf '{"default_branch":"main","allow_auto_merge":true}'
    ;;
  *"issue list"*)
    printf '[]'
    ;;
  *"issue view 143"*)
    printf '{"number":143,"title":"feat: running agent log preview","body":"issue body","state":"OPEN"}'
    ;;
  *)
    printf '[]'
    ;;
esac
EOF
chmod +x "$tmp/bin/gh"

herdr_log="$tmp/herdr.log"
: > "$herdr_log"
cat > "$tmp/bin/herdr" <<EOF
#!/bin/sh
echo "HERDR \$*" >> "$herdr_log"
case "\$1 \$2" in
  "pane peek"|"peek"*) exit 0 ;;
  *) exit 0 ;;
esac
EOF
chmod +x "$tmp/bin/herdr"

export PATH="$tmp/bin:$PATH"
CGO_ENABLED=0 go build -o "$tmp/lead" ./cmd/lead || fail "go build failed"

cd "$repo"

# 1. Start inbox headless, expand all (z), move down 4 times to issue 143 in Running, quit
out="$(LEAD_TEST_INBOX_WIDTH=130 LEAD_TEST_INBOX_HEIGHT=30 LEAD_TEST_INBOX_KEYS=z,j,j,j,j,q "$tmp/lead" 2>&1)" || fail "lead exited non-zero: $out"
printf '%s\n' "$out" > "$tmp/screen.log"

case "$out" in
  *"Agent Live Output"*) ;;
  *) fail "preview did not contain 'Agent Live Output': $out" ;;
esac

case "$out" in
  *"Step 2: running TDD tests"*) ;;
  *) fail "preview did not contain expected log lines: $out" ;;
esac

case "$out" in
  *"Agent: agy"*) ;;
  *) fail "preview did not contain 'Agent: agy': $out" ;;
esac

# 2. Test peek action with 'p' key using Herdr
: > "$herdr_log"
cat > "$tmp/bin/herdr" <<'EOF'
#!/bin/sh
for arg in "$@"; do
  echo "<$arg>" >> "$HERDR_LOG"
done
case "$1 $2" in
  "tab create")
    printf '{"result":{"root_pane":{"pane_id":"p-new"}}}'
    ;;
  *) exit 0 ;;
esac
EOF

HERDR_LOG="$herdr_log" HERDR_ENV=1 LEAD_TEST_INBOX_KEYS=z,j,j,j,j,p,q "$tmp/lead" >/dev/null 2>&1 || fail "peek keypress failed"
grep -qxF '<tab>' "$herdr_log" || fail "p did not call herdr tab create: $(cat "$herdr_log")"
grep -qxF '<create>' "$herdr_log" || fail "p herdr tab create args wrong: $(cat "$herdr_log")"
grep -qxF '<--focus>' "$herdr_log" || fail "p herdr tab create missing --focus: $(cat "$herdr_log")"
grep -qxF '<tail>' "$herdr_log" || fail "p herdr missing tail command: $(cat "$herdr_log")"

echo "inbox running log preview and attach checks passed"
