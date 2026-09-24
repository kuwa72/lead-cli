#!/usr/bin/env bash
# issue #224: `lead enable` distributes the lead-flow skill to
# .agents/skills plus the skills dir of every agent detected on PATH or
# configured in inbox-config.json. `lead disable` scans every known
# placement and keeps foreign files.
# Behavioral checks only: PATH-injected dummy agent binaries, generated
# artifacts, exit statuses.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

fail() { echo "FAIL: $*" >&2; exit 1; }

CGO_ENABLED=0 go build -o "$tmp/lead" ./cmd/lead || fail "go build failed"

export HOME="$tmp/home"
mkdir -p "$HOME"
export LEAD_STATE_FILE="$tmp/state/workflows.json"
mkdir -p "$tmp/state"

# Controlled PATH: git only, plus dummy agents injected per case.
mkdir -p "$tmp/bin"
ln -s "$(command -v git)" "$tmp/bin/git"
lead() { env PATH="$tmp/bin" "$tmp/lead" "$@"; }

mkagent() { printf '#!/bin/sh\nexit 0\n' > "$tmp/bin/$1"; chmod +x "$tmp/bin/$1"; }
mkrepo() {
  mkdir -p "$1"
  git -C "$1" init -q -b main || fail "git init failed"
  git -C "$1" config user.email "t@t"
  git -C "$1" config user.name "t"
}

# --- 1. no agents on PATH: only .agents/skills is written ---
mkrepo "$tmp/p1"
(cd "$tmp/p1" && lead enable --yes >/dev/null) || fail "enable failed"
[ -f "$tmp/p1/.agents/skills/lead-flow/SKILL.md" ] || fail ".agents skill missing"
for d in .claude .devin .codex .gemini .opencode; do
  [ ! -e "$tmp/p1/$d" ] || fail "$d created with no agent binaries on PATH"
done
out="$(cd "$tmp/p1" && lead enable --yes)" || fail "re-enable failed"
case "$out" in *"no changes"*) ;; *) fail "re-enable not idempotent: $out";; esac

# --- 2. detected agents (dummy codex + opencode) add their dirs ---
mkagent codex
mkagent opencode
mkrepo "$tmp/p2"
(cd "$tmp/p2" && lead enable --yes >/dev/null) || fail "enable with agents failed"
[ -f "$tmp/p2/.agents/skills/lead-flow/SKILL.md" ] || fail ".agents skill missing (p2)"
[ -f "$tmp/p2/.opencode/skills/lead-flow/SKILL.md" ] || fail "opencode skill missing"
# codex reads .agents/skills natively: no dedicated .codex dir.
[ ! -e "$tmp/p2/.codex" ] || fail ".codex created; codex repo skills live in .agents/skills"
for d in .claude .devin .gemini; do
  [ ! -e "$tmp/p2/$d" ] || fail "$d created without a detected binary"
done

# --dry-run/--check reflect the detected target set.
out="$(cd "$tmp/p2" && lead enable --dry-run)"
case "$out" in *"skill/opencode"*) ;; *) fail "dry-run missing detected target: $out";; esac
case "$out" in *"skill/claude"*) fail "dry-run lists an undetected agent: $out";; esac
(cd "$tmp/p2" && lead enable --check >/dev/null) || fail "--check failed after enable"

# --- 3. configured agent (inbox-config.json) targets its dir without a binary ---
printf '{"agent":"gemini"}\n' > "$tmp/state/inbox-config.json"
mkrepo "$tmp/p3"
(cd "$tmp/p3" && lead enable --yes >/dev/null) || fail "enable with configured agent failed"
[ -f "$tmp/p3/.gemini/skills/lead-flow/SKILL.md" ] || fail "configured gemini skill missing"
[ -f "$tmp/p3/.agents/skills/lead-flow/SKILL.md" ] || fail ".agents skill missing (p3)"

# --- 4. disable scans every known dir and keeps foreign files ---
printf 'name: other-skill\n' > "$tmp/p3/.gemini/skills/lead-flow/SKILL.md"
out="$(cd "$tmp/p3" && lead disable)" || fail "disable failed"
[ ! -e "$tmp/p3/.agents/skills/lead-flow/SKILL.md" ] || fail ".agents skill remains after disable"
[ ! -e "$tmp/p3/.opencode/skills/lead-flow/SKILL.md" ] || fail "opencode skill remains after disable"
[ -f "$tmp/p3/.gemini/skills/lead-flow/SKILL.md" ] || fail "foreign skill removed by disable"
case "$out" in *"foreign file kept"*) ;; *) fail "disable missing foreign-file notice: $out";; esac
(cd "$tmp/p3" && lead enable --check >/dev/null 2>&1) && fail "--check passed after disable"

echo "PASS: enable skill-target distribution checks passed"
