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

export PATH="$tmp/bin:$PATH"

CGO_ENABLED=0 go build -o "$tmp/lead" ./cmd/lead || fail "go build failed"
cd "$repo"

# The agent command (with the rendered prompt) is printed inline on stdout.
# 1. Test custom template selection via --prompt-template on `lead run`
out="$("$tmp/lead" run 84 --prompt-template custom)" || fail "lead run 84 with --prompt-template failed"
echo "$out" | grep -F "CUSTOM_PROMPT_HEADER" > /dev/null || fail "output missing CUSTOM_PROMPT_HEADER: $out"
echo "$out" | grep -F "Issue: #84" > /dev/null || fail "output missing Issue: #84: $out"
echo "$out" | grep -F 'TDD required: Red -> Green -> Refactor' > /dev/null || fail "output missing AGENTS.md content: $out"
echo "$out" | grep -F "Branch: issue/84-" > /dev/null || fail "output missing branch: $out"

# 2. Test default implement template automatically includes AGENTS.md in ## プロジェクト規約
out="$("$tmp/lead" run 85)" || fail "lead run 85 default template failed"
echo "$out" | grep -F '## プロジェクト規約' > /dev/null || fail "output missing ## プロジェクト規約: $out"
echo "$out" | grep -F 'TDD required: Red -> Green -> Refactor' > /dev/null || fail "output missing AGENTS.md rules in default template: $out"

# 3. Test built-in review template selection via --prompt-template review
out="$("$tmp/lead" run 86 --prompt-template review)" || fail "lead run 86 --prompt-template review failed"
echo "$out" | grep -F "lead lgtm 86" > /dev/null || fail "output missing lead lgtm 86: $out"

# 4. Test `lead run` with --prompt-template
out="$("$tmp/lead" run 87 --prompt-template custom)" || fail "lead run 87 with --prompt-template failed"
echo "$out" | grep -F "CUSTOM_PROMPT_HEADER" > /dev/null || fail "lead run missing CUSTOM_PROMPT_HEADER: $out"
echo "$out" | grep -F "Issue: #87" > /dev/null || fail "lead run missing Issue: #87: $out"

echo "PASS: prompt template behavioral tests passed"
