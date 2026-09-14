#!/usr/bin/env bash
# issue #169: `lead setup` (user environment) vs `lead enable` (repository) separation.
# Behavioral checks only: exit statuses, command outputs, generated artifacts.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

fail() { echo "FAIL: $*" >&2; exit 1; }

CGO_ENABLED=0 go build -o "$tmp/lead" ./cmd/lead || fail "go build failed"

export HOME="$tmp/home"
mkdir -p "$HOME"
export SHELL="/bin/bash"

# --- 1. `lead enable` exists, `lead init` stays as alias, `lead install` stays gone ---
"$tmp/lead" enable --help >/dev/null || fail "enable --help exited non-zero"
"$tmp/lead" init --help >/dev/null || fail "init --help (alias) exited non-zero"
"$tmp/lead" disable --help >/dev/null || fail "disable --help exited non-zero"
"$tmp/lead" install --help >/dev/null 2>&1 && fail "old 'install' command still exists"

# --- 2. help wording separates user environment from repository ---
case "$("$tmp/lead" setup --help)" in *"user environment"*) ;; *) fail "setup --help does not say user environment";; esac
case "$("$tmp/lead" enable --help)" in *"repository"*) ;; *) fail "enable --help does not say repository";; esac
case "$("$tmp/lead" enable --help)" in *"lead enable"*) ;; *) fail "enable --help missing usage header";; esac

# --- 3. `lead enable` needs a repository ---
mkdir -p "$tmp/norepo"
(cd "$tmp/norepo" && "$tmp/lead" enable >/dev/null 2>&1) && fail "enable outside a repo succeeded"

# --- 4. enable --dry-run writes nothing ---
proj="$tmp/project"
mkdir -p "$proj"
cd "$proj"
git init >/dev/null || fail "git init failed"
git config user.email "test@example.com"
git config user.name "Test"

out="$("$tmp/lead" enable --dry-run)" || fail "enable --dry-run failed"
case "$out" in *"dry run"*) ;; *) fail "enable --dry-run missing dry-run notice";; esac
[ ! -e .claude ] && [ ! -e .devin ] && [ ! -e AGENTS.md ] || fail "enable --dry-run wrote files"

# --- 5. `enable --yes` installs repo files and never touches $HOME ---
"$tmp/lead" enable --yes >/dev/null || fail "enable --yes failed"
[ -f .claude/skills/lead-flow/SKILL.md ] || fail "claude skill file missing"
[ -f .devin/skills/lead-flow/SKILL.md ] || fail "devin skill file missing"
[ -f AGENTS.md ] || fail "AGENTS.md missing"
case "$(cat AGENTS.md)" in *"lead-flow"*) ;; *) fail "AGENTS.md missing lead-flow block";; esac
[ -z "$(ls -A "$HOME")" ] || fail "enable touched \$HOME: $(ls -A "$HOME")"

# --- 6. idempotency and --check (`--write` stays accepted for compatibility) ---
out="$("$tmp/lead" enable --write --yes)" || fail "re-enable failed"
case "$out" in *"no changes"*) ;; *) fail "re-enable not idempotent";; esac
"$tmp/lead" enable --check >/dev/null || fail "enable --check failed after enable"

# --- 7. doctor reports the project as enabled ---
doc_out="$("$tmp/lead" doctor --offline 2>&1 || true)"
case "$doc_out" in *"project"*) ;; *) fail "doctor output missing project check";; esac

# --- 8. `disable` removes managed files and block; --check then fails ---
"$tmp/lead" disable >/dev/null || fail "disable failed"
[ ! -e .claude/skills/lead-flow/SKILL.md ] || fail "claude skill remains after disable"
[ ! -e .devin/skills/lead-flow/SKILL.md ] || fail "devin skill remains after disable"
case "$(cat AGENTS.md)" in *"lead-flow"*) fail "AGENTS.md block remains after disable";; esac
"$tmp/lead" enable --check >/dev/null 2>&1 && fail "enable --check after disable succeeded"

# --- 8b. `enable --uninstall` stays accepted for compatibility ---
"$tmp/lead" enable --yes >/dev/null || fail "re-enable failed"
"$tmp/lead" enable --uninstall >/dev/null || fail "enable --uninstall compat failed"
[ ! -e .claude/skills/lead-flow/SKILL.md ] || fail "claude skill remains after uninstall"

# --- 9. doctor guides toward `lead enable` when the repo is not enabled ---
doc_out="$("$tmp/lead" doctor --offline 2>&1 || true)"
case "$doc_out" in *"lead enable"*) ;; *) fail "doctor missing lead enable guidance";; esac

# --- 10. `lead setup` touches only $HOME, never the repository ---
proj2="$tmp/other"
mkdir -p "$proj2"
cd "$proj2"
git init >/dev/null || fail "git init failed"
git config user.email "test@example.com"
git config user.name "Test"
HOME2="$tmp/home2"; mkdir -p "$HOME2"
HOME="$HOME2" "$tmp/lead" setup --shell bash --write --yes --no-keybinding >/dev/null \
  || fail "setup --write failed"
[ -z "$(git status --porcelain)" ] || fail "setup touched the repository: $(git status --porcelain)"

# --- 11. opt-in keybinding snippet starts `lead run` ---
HOME3="$tmp/home3"; mkdir -p "$HOME3"
printf 'y\ny\n' | HOME="$HOME3" "$tmp/lead" setup --shell bash --write >/dev/null \
  || fail "opt-in setup failed"
case "$(cat "$HOME3/.bashrc")" in *"lead run"*) ;; *) fail "rc keybinding does not run lead run";; esac
case "$(cat "$HOME3/.bashrc")" in *"lead work"*) fail "rc keybinding still references lead work";; esac

echo "setup/enable separation checks passed"
