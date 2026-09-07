#!/usr/bin/env bash
# issue #68: `lead install` project-oriented agent config behavior tests.
# Verifies dry-run, --write, idempotency, --check, and --uninstall
# using a temporary git project (no source-grep assertions).
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

fail() { echo "FAIL: $*" >&2; exit 1; }

CGO_ENABLED=0 go build -o "$tmp/lead" ./cmd/lead || fail "go build failed"

# --- 1. help renders ---
out="$("$tmp/lead" install --help)" || fail "install --help exited non-zero"
case "$out" in *"lead install"*) ;; *) fail "install --help missing usage header";; esac

# --- 2. dry-run makes no changes ---
proj="$tmp/project"
mkdir -p "$proj"
cd "$proj"
git init >/dev/null || fail "git init failed"
git config user.email "test@example.com"
git config user.name "Test"

out="$("$tmp/lead" install)" || fail "install dry-run failed"
case "$out" in *"dry run"*) ;; *) fail "dry-run missing dry-run notice";; esac
[ ! -e .claude ] && [ ! -e .devin ] && [ ! -e AGENTS.md ] || fail "dry-run wrote files"

# --- 3. --write --yes installs skill files and AGENTS.md block ---
"$tmp/lead" install --write --yes >/dev/null || fail "install --write failed"
[ -f .claude/skills/lead-flow/SKILL.md ] || fail "claude skill file missing"
[ -f .devin/skills/lead-flow/SKILL.md ] || fail "devin skill file missing"
[ -f AGENTS.md ] || fail "AGENTS.md missing"
case "$(cat AGENTS.md)" in *"lead-flow"*) ;; *) fail "AGENTS.md missing lead-flow block";; esac

# --- 4. idempotency: re-run reports no changes ---
out="$("$tmp/lead" install --write --yes)" || fail "re-install failed"
case "$out" in *"no changes"*) ;; *) fail "re-install not idempotent";; esac

# --- 5. --check passes after install ---
"$tmp/lead" install --check >/dev/null || fail "install --check failed after install"

# --- 6. --uninstall removes managed files and block ---
"$tmp/lead" install --uninstall >/dev/null || fail "uninstall failed"
[ ! -e .claude/skills/lead-flow/SKILL.md ] || fail "claude skill remains after uninstall"
[ ! -e .devin/skills/lead-flow/SKILL.md ] || fail "devin skill remains after uninstall"
case "$(cat AGENTS.md)" in *"lead-flow"*) fail "AGENTS.md block remains after uninstall";; esac

# --- 7. --check fails after uninstall ---
"$tmp/lead" install --check >/dev/null 2>&1 && fail "install --check after uninstall succeeded"

echo "lead install project checks passed"
