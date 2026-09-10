#!/usr/bin/env bash
# test_prompt_template.sh: verifies prompt template selection and AGENTS.md injection (issue #84).
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
if [ -d "/home/linuxbrew/.linuxbrew/bin" ]; then
  export PATH="/home/linuxbrew/.linuxbrew/bin:$PATH"
fi
unset XDG_STATE_HOME || true
unset LEAD_STATE_FILE || true
mkdir -p "$HOME"

repo="$tmp/repo"
git init -q -b main "$repo" || fail "git init failed"
git -C "$repo" config user.email "t@t"
git -C "$repo" config user.name "t"
cat > "$repo/AGENTS.md" <<'EOF'
# Project AGENTS.md
- TDD required: Red -> Green -> Refactor
- Run ./test/run-tests.sh
EOF
git -C "$repo" add .
git -C "$repo" commit -qm init || fail "fixture commit failed"

# Create a custom template in the repo
mkdir -p "$repo/prompts"
cat > "$repo/prompts/custom.md" <<'EOF'
CUSTOM_PROMPT_HEADER
Issue: #{{.Number}}
Title: {{.Title}}
Branch: {{.Branch}}
Mode: {{.Mode}}
Rules:
{{.Rules}}
EOF

mkdir -p "$tmp/bin"
cat > "$tmp/bin/gh" <<'EOF'
#!/bin/sh
if [ "$1 $2" = "issue view" ]; then
  num="$3"
  printf '{"number":%s,"title":"Issue %s Title","body":"Issue %s Body","state":"OPEN"}' "$num" "$num" "$num"
else
  echo "unexpected gh call: $@" >&2; exit 3
fi
EOF
chmod +x "$tmp/bin/gh"

export HERDR_LOG="$tmp/herdr.log"
: > "$HERDR_LOG"
cat > "$tmp/bin/herdr" <<'EOF'
#!/bin/sh
echo "HERDR $@" >> "$HERDR_LOG"
case "$1 $2" in
  "pane split") printf '{"result":{"pane":{"pane_id":"pane-%s"}}}' "$$" ;;
  "pane send-text") : ;;
  *) exit 0 ;;
esac
EOF
chmod +x "$tmp/bin/herdr"

export PATH="$tmp/bin:$PATH"
export HERDR_ENV=1

CGO_ENABLED=0 go build -o "$tmp/lead" ./cmd/lead || fail "go build failed"
cd "$repo"

# 1. Test custom template selection via --prompt-template on `lead work`
: > "$HERDR_LOG"
"$tmp/lead" work 84 --prompt-template custom > /dev/null || fail "lead work 84 with --prompt-template failed"
herdr_log="$(cat "$HERDR_LOG")"
echo "$herdr_log" | grep -F "CUSTOM_PROMPT_HEADER" > /dev/null || fail "herdr missing CUSTOM_PROMPT_HEADER: $herdr_log"
echo "$herdr_log" | grep -F "Issue: #84" > /dev/null || fail "herdr missing Issue: #84: $herdr_log"
echo "$herdr_log" | grep -F "TDD required: Red -> Green -> Refactor" > /dev/null || fail "herdr missing AGENTS.md content: $herdr_log"
echo "$herdr_log" | grep -F "Branch: issue/84-" > /dev/null || fail "herdr missing branch: $herdr_log"

# 2. Test default implement template automatically includes AGENTS.md in ## プロジェクト規約
: > "$HERDR_LOG"
"$tmp/lead" work 85 > /dev/null || fail "lead work 85 default template failed"
herdr_log="$(cat "$HERDR_LOG")"
echo "$herdr_log" | grep -F "## プロジェクト規約" > /dev/null || fail "herdr missing ## プロジェクト規約: $herdr_log"
echo "$herdr_log" | grep -F "TDD required: Red -> Green -> Refactor" > /dev/null || fail "herdr missing AGENTS.md rules in default template: $herdr_log"

# 3. Test built-in review template selection via --prompt-template review
: > "$HERDR_LOG"
"$tmp/lead" work 86 --prompt-template review > /dev/null || fail "lead work 86 --prompt-template review failed"
herdr_log="$(cat "$HERDR_LOG")"
echo "$herdr_log" | grep -F "lead lgtm 86" > /dev/null || fail "herdr missing lead lgtm 86: $herdr_log"

# 4. Test `lead run` with --prompt-template
: > "$HERDR_LOG"
"$tmp/lead" run 87 --prompt-template custom > /dev/null || fail "lead run 87 with --prompt-template failed"
herdr_log="$(cat "$HERDR_LOG")"
echo "$herdr_log" | grep -F "CUSTOM_PROMPT_HEADER" > /dev/null || fail "lead run missing CUSTOM_PROMPT_HEADER: $herdr_log"
echo "$herdr_log" | grep -F "Issue: #87" > /dev/null || fail "lead run missing Issue: #87: $herdr_log"

echo "PASS: prompt template behavioral tests passed"
