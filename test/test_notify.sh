#!/usr/bin/env bash
set -euo pipefail

# test_notify.sh: verifies desktop and terminal notification on agent completion (issue #145).
# Asserts that transitioning to Needs Review or Blocked invokes notification tools (notify-send).

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
cat > "$state_dir/workflows.json" <<EOF
{
  "version": 1,
  "workflows": []
}
EOF

cat > "$state_dir/inbox-seen.json" <<'EOF'
{"last_seen_at":"2026-09-11T00:00:00Z","confirmed":[],"help_shown":true}
EOF

mkdir -p "$tmp/bin"

notify_log="$tmp/notify.log"
: > "$notify_log"
cat > "$tmp/bin/notify-send" <<EOF
#!/bin/sh
for arg in "\$@"; do
  echo "<\$arg>" >> "$notify_log"
done
EOF
chmod +x "$tmp/bin/notify-send"

counter_file="$tmp/gh_call_count"
echo "0" > "$counter_file"

cat > "$tmp/bin/gh" <<EOF
#!/bin/sh
case "\$*" in
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
  *"issue list"*"needs-review"*)
    count=\$(cat "$counter_file")
    count=\$((count + 1))
    echo "\$count" > "$counter_file"
    if [ "\$count" -eq 1 ]; then
      printf '[{"number":10,"title":"old issue","labels":[{"name":"needs-review"}]}]'
    else
      printf '[{"number":10,"title":"old issue","labels":[{"name":"needs-review"}]},{"number":20,"title":"agent completed PR","labels":[{"name":"needs-review"}]}]'
    fi
    ;;
  *"issue list"*)
    printf '[]'
    ;;
  *"issue view 10"*)
    printf '{"number":10,"title":"old issue","body":"old body","state":"OPEN"}'
    ;;
  *"issue view 20"*)
    printf '{"number":20,"title":"agent completed PR","body":"ready for review","state":"OPEN"}'
    ;;
  *)
    printf '[]'
    ;;
esac
EOF
chmod +x "$tmp/bin/gh"

export PATH="$tmp/bin:$PATH"
CGO_ENABLED=0 go build -o "$tmp/lead" ./cmd/lead || fail "go build failed"

cd "$repo"

# 1. Initial load only (no reload): issue 10 is already in needs-review; should NOT notify.
echo "0" > "$counter_file"
: > "$notify_log"
LEAD_TEST_INBOX_WIDTH=130 LEAD_TEST_INBOX_HEIGHT=30 LEAD_TEST_INBOX_KEYS=q "$tmp/lead" >/dev/null 2>&1 || fail "initial lead run failed"
if [ -s "$notify_log" ]; then
  fail "initial load should not trigger notifications, got: $(cat "$notify_log")"
fi

# 2. In a single run with reload (R): first load gets #10, then R gets #10 and #20. Should trigger notification!
echo "0" > "$counter_file"
: > "$notify_log"
LEAD_TEST_INBOX_WIDTH=130 LEAD_TEST_INBOX_HEIGHT=30 LEAD_TEST_INBOX_KEYS=R,q "$tmp/lead" >/dev/null 2>&1 || fail "reload lead run failed"
if ! grep -qF "agent completed PR" "$notify_log"; then
  fail "reload did not notify new needs-review issue, got: $(cat "$notify_log")"
fi

# 3. LEAD_NOTIFY=0 disables notifications on reload
echo "0" > "$counter_file"
: > "$notify_log"
LEAD_NOTIFY=0 LEAD_TEST_INBOX_WIDTH=130 LEAD_TEST_INBOX_HEIGHT=30 LEAD_TEST_INBOX_KEYS=R,q "$tmp/lead" >/dev/null 2>&1 || fail "disabled lead run failed"
if [ -s "$notify_log" ]; then
  fail "LEAD_NOTIFY=0 should suppress notifications, got: $(cat "$notify_log")"
fi

echo "notification behavioral checks passed"
