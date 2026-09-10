#!/usr/bin/env bash
set -euo pipefail

# test_inbox_pr_diff_preview.sh: verifies PR diff preview and changed files in inbox (issue #146).
# Asserts that Needs Review issues with linked PRs show changed files and syntax-highlighted diffs.

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
  "workflows": [
    {
      "repository": "https://github.com/kuwa72/lead-cli.git",
      "issue": 146,
      "mode": "implement",
      "status": "awaiting_review",
      "branch": "issue/146-preview-test",
      "agent": "agy",
      "pull_requests": [{"number": 99, "status": "open"}],
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
  *"issue list"*"needs-review"*)
    printf '[{"number":146,"title":"feat: diff preview in inbox","labels":[{"name":"needs-review"}]}]'
    ;;
  *"issue list"*)
    printf '[]'
    ;;
  *"issue view 146"*)
    printf '{"number":146,"title":"feat: diff preview in inbox","body":"original issue body","state":"OPEN"}'
    ;;
  *"pr diff 99"*)
    cat <<'DIFF'
diff --git a/pkg/service.go b/pkg/service.go
--- a/pkg/service.go
+++ b/pkg/service.go
@@ -10,3 +10,5 @@
 func Serve() {
+	// added service logic
+	start()
 }
DIFF
    ;;
  *"pr view 99"*)
    printf '{"body":"pr description"}'
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

# 1. Start inbox in split preview mode. Navigate to trigger preview load.
out="$(LEAD_TEST_INBOX_WIDTH=130 LEAD_TEST_INBOX_HEIGHT=30 LEAD_TEST_INBOX_KEYS=j,k,q "$tmp/lead" 2>&1)" || fail "lead exited non-zero: $out"
printf '%s\n' "$out" > "$tmp/screen.log"

case "$out" in
  *"Changed files"*) ;;
  *) fail "preview did not contain 'Changed files': $out" ;;
esac

case "$out" in
  *"pkg/service.go"*) ;;
  *) fail "preview did not contain changed file name 'pkg/service.go': $out" ;;
esac

case "$out" in
  *"+	// added service logic"*) ;;
  *) fail "preview did not contain diff addition line: $out" ;;
esac

# 2. Enter full details mode (enter) and verify diff appears
out_detail="$(LEAD_TEST_INBOX_WIDTH=130 LEAD_TEST_INBOX_HEIGHT=30 LEAD_TEST_INBOX_KEYS=enter,q "$tmp/lead" 2>&1)" || fail "lead detail exited non-zero: $out_detail"

case "$out_detail" in
  *"Changed files"*) ;;
  *) fail "detail did not contain 'Changed files': $out_detail" ;;
esac

case "$out_detail" in
  *"pkg/service.go"*) ;;
  *) fail "detail did not contain changed file 'pkg/service.go': $out_detail" ;;
esac

case "$out_detail" in
  *"+	// added service logic"*) ;;
  *) fail "detail did not contain diff addition line: $out_detail" ;;
esac

echo "inbox PR diff preview checks passed"
