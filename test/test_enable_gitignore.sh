#!/usr/bin/env bash
# issue #232: `lead enable` records the paths it generates (.worktrees/ and
# the lead-flow skill dirs) in the repository's git exclude file so a freshly
# enabled repo does not report them as untracked. `--yes` applies, the
# interactive prompt defaults to yes, and declining leaves the file untouched
# with guidance. Behavioral checks only: generated exclude file, git
# status/check-ignore, exit statuses, and output.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
fail() { echo "FAIL: $*" >&2; exit 1; }

CGO_ENABLED=0 go build -o "$tmp/lead" ./cmd/lead || fail "go build failed"

export HOME="$tmp/home"
mkdir -p "$HOME"

mkrepo() {
  mkdir -p "$1"
  git -C "$1" init -q -b main || fail "git init failed"
  git -C "$1" config user.email "t@t"
  git -C "$1" config user.name "t"
}

# --- 1. dry-run previews the ignore entries without writing them ---
proj="$tmp/project"
mkrepo "$proj"
exclude="$proj/.git/info/exclude"

out="$(cd "$proj" && "$tmp/lead" enable --dry-run)" || fail "enable --dry-run failed: $out"
case "$out" in
  *"ignore: new"*|*"ignore: append"*) ;;
  *) fail "dry-run missing ignore preview: $out" ;;
esac
if [ -f "$exclude" ] && grep -q '^\.worktrees/$' "$exclude"; then
  fail "dry-run wrote .worktrees/ to the exclude file"
fi

# --- 2. --yes ignores generated paths; git status shows no untracked ones ---
mkdir -p "$proj/.worktrees/issue-1"
printf 'x\n' > "$proj/.worktrees/issue-1/file"

out="$(cd "$proj" && "$tmp/lead" enable --yes)" || fail "enable --yes failed: $out"
grep -q '^\.worktrees/$' "$exclude" || fail "exclude missing .worktrees/: $(cat "$exclude")"
grep -q '^\.agents/skills/lead-flow/$' "$exclude" || fail "exclude missing lead-flow skill dir: $(cat "$exclude")"
git -C "$proj" check-ignore -q .worktrees/issue-1 || fail ".worktrees/ is not ignored"
git -C "$proj" check-ignore -q .agents/skills/lead-flow/SKILL.md || fail "skill dir is not ignored"

status="$(git -C "$proj" status --porcelain)"
case "$status" in *".worktrees"*) fail "git status still reports .worktrees: $status" ;; esac
case "$status" in *".agents/skills"*) fail "git status still reports .agents/skills: $status" ;; esac

(cd "$proj" && "$tmp/lead" enable --check >/dev/null) || fail "enable --check failed after enable"

out="$(cd "$proj" && "$tmp/lead" enable --yes)" || fail "re-enable failed: $out"
case "$out" in *"no changes"*) ;; *) fail "re-enable not idempotent: $out" ;; esac
[ "$(grep -c '^# lead-flow begin' "$exclude")" = "1" ] || fail "ignore block duplicated: $(cat "$exclude")"

# --- 3. declining the ignore prompt leaves the file untouched and guides ---
proj2="$tmp/project2"
mkrepo "$proj2"
exclude2="$proj2/.git/info/exclude"

out="$(cd "$proj2" && printf 'y\nn\n' | "$tmp/lead" enable)" || fail "declined enable failed: $out"
if [ -f "$exclude2" ] && grep -q '^\.worktrees/$' "$exclude2"; then
  fail "declined enable wrote the ignore block"
fi
case "$out" in *"ignore: skipped"*) ;; *) fail "declined enable missing guidance: $out" ;; esac
case "$out" in *"untracked"*) ;; *) fail "declined enable missing untracked guidance: $out" ;; esac

# --- 4. disable removes the managed block but keeps foreign entries ---
printf '# foreign\n' >> "$exclude"
(cd "$proj" && "$tmp/lead" disable >/dev/null) || fail "disable failed"
if grep -q '^\.worktrees/$' "$exclude"; then
  fail "disable kept the managed ignore block: $(cat "$exclude")"
fi
grep -q '^# foreign$' "$exclude" || fail "disable removed a foreign exclude entry: $(cat "$exclude")"

echo "PASS: enable git-ignore checks passed"
