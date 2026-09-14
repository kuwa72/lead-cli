#!/usr/bin/env bash
# issue #68/#79/#169/#176: `lead init` (hidden alias of `lead enable`)
# project-oriented agent config behavior tests.
# Verifies --dry-run, default apply, idempotency, --check, and --uninstall
# using a temporary git project (no source-grep assertions).
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

fail() { echo "FAIL: $*" >&2; exit 1; }

CGO_ENABLED=0 go build -o "$tmp/lead" ./cmd/lead || fail "go build failed"

# --- 0. `lead init` stays as a hidden alias of `lead enable` (issue #169),
# and old `lead install` is still gone (issue #79) ---
"$tmp/lead" enable --help >/dev/null || fail "enable --help exited non-zero"
"$tmp/lead" init --help >/dev/null || fail "init --help exited non-zero"
"$tmp/lead" install --help >/dev/null 2>&1 && fail "old 'install' command still exists"

# --- 1. help renders ---
out="$("$tmp/lead" init --help)" || fail "init --help exited non-zero"
case "$out" in *"lead init"*) ;; *) fail "init --help missing usage header";; esac

# --- 2. --dry-run makes no changes ---
proj="$tmp/project"
mkdir -p "$proj"
cd "$proj"
git init >/dev/null || fail "git init failed"
git config user.email "test@example.com"
git config user.name "Test"

out="$("$tmp/lead" init --dry-run)" || fail "init --dry-run failed"
case "$out" in *"dry run"*) ;; *) fail "dry-run missing dry-run notice";; esac
[ ! -e .claude ] && [ ! -e .devin ] && [ ! -e AGENTS.md ] || fail "dry-run wrote files"

# --- 3. default apply (--yes) installs skill files and AGENTS.md block ---
"$tmp/lead" init --yes >/dev/null || fail "init apply failed"
[ -f .claude/skills/lead-flow/SKILL.md ] || fail "claude skill file missing"
[ -f .devin/skills/lead-flow/SKILL.md ] || fail "devin skill file missing"
[ -f AGENTS.md ] || fail "AGENTS.md missing"
case "$(cat AGENTS.md)" in *"lead-flow"*) ;; *) fail "AGENTS.md missing lead-flow block";; esac

# --- 4. idempotency: re-run reports no changes ---
out="$("$tmp/lead" init --write --yes)" || fail "re-init failed"
case "$out" in *"no changes"*) ;; *) fail "re-init not idempotent";; esac

# --- 5. --check passes after init ---
"$tmp/lead" init --check >/dev/null || fail "init --check failed after init"

# --- 6. --uninstall removes managed files and block ---
"$tmp/lead" init --uninstall >/dev/null || fail "uninstall failed"
[ ! -e .claude/skills/lead-flow/SKILL.md ] || fail "claude skill remains after uninstall"
[ ! -e .devin/skills/lead-flow/SKILL.md ] || fail "devin skill remains after uninstall"
case "$(cat AGENTS.md)" in *"lead-flow"*) fail "AGENTS.md block remains after uninstall";; esac

# --- 7. --check fails after uninstall ---
"$tmp/lead" init --check >/dev/null 2>&1 && fail "init --check after uninstall succeeded"

echo "lead init project checks passed"
