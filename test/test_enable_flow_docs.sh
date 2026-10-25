#!/usr/bin/env bash
# issue #251: the lead-flow block `lead enable` writes to AGENTS.md and the
# lead-flow skill it distributes must reflect the independent review gate
# (#246) and the risk gate (#245): run `lead review <pr>` after CI, and let
# `lead finish --merge` stop for risk:a without a review pass / risk:b (and
# unlabelled) without human approval. Behavioral checks only: run enable in
# a temp repository and assert the generated artifacts.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

fail() { echo "FAIL: $*" >&2; exit 1; }

CGO_ENABLED=0 go build -o "$tmp/lead" ./cmd/lead || fail "go build failed"

export HOME="$tmp/home"
mkdir -p "$HOME"

proj="$tmp/project"
mkdir -p "$proj"
git -C "$proj" init -q -b main || fail "git init failed"
git -C "$proj" config user.email "t@t"
git -C "$proj" config user.name "t"

# Isolate PATH: only git resolves, so enable writes .agents/skills only.
mkdir -p "$tmp/bin"
ln -s "$(command -v git)" "$tmp/bin/git"
(cd "$proj" && env PATH="$tmp/bin" "$tmp/lead" enable --yes >/dev/null) || fail "enable failed"

agents="$proj/AGENTS.md"
skill="$proj/.agents/skills/lead-flow/SKILL.md"
[ -f "$agents" ] || fail "AGENTS.md missing"
[ -f "$skill" ] || fail ".agents skill missing"

# Assertions target the managed block, the part `lead enable` owns.
block="$(awk '/<!-- lead-flow begin/{f=1} f{print} /<!-- lead-flow end -->/{f=0}' "$agents")"
case "$block" in *"lead-flow"*) ;; *) fail "managed lead-flow block not found in AGENTS.md";; esac

expect() {
  local where="$1" needle="$2" text="$3"
  case "$text" in *"$needle"*) ;; *) fail "$where missing '$needle'";; esac
}

# --- AGENTS.md block: review gate, risk classes, human-only flags ---
for needle in "lead review" "needs-work" "risk:a" "risk:b" "--approve" \
  "--no-review" "--override-guardrail" "human-only" "人間専用"; do
  expect "AGENTS.md block" "$needle" "$block"
done

# The review step must come before the merge step: finish depends on it.
review_line="$(printf '%s\n' "$block" | grep -n 'lead review' | head -1 | cut -d: -f1)"
finish_line="$(printf '%s\n' "$block" | grep -n 'lead finish' | head -1 | cut -d: -f1)"
[ -n "$review_line" ] && [ -n "$finish_line" ] \
  || fail "AGENTS.md block lacks an ordered review/finish step"
[ "$review_line" -lt "$finish_line" ] \
  || fail "AGENTS.md block runs finish (line $finish_line) before review (line $review_line)"

# A stopped finish must not be finished by hand.
expect "AGENTS.md block" "gh pr merge" "$block"
expect "AGENTS.md block" "gh issue close" "$block"

# --- distributed skill: same procedure in English ---
for needle in "lead review" "needs-work" "risk:a" "risk:b" "--approve" \
  "--no-review" "--override-guardrail" "human-only"; do
  expect "skill" "$needle" "$(cat "$skill")"
done

skill_review="$(grep -n 'lead review' "$skill" | head -1 | cut -d: -f1)"
skill_finish="$(grep -n 'lead finish' "$skill" | head -1 | cut -d: -f1)"
[ -n "$skill_review" ] && [ -n "$skill_finish" ] \
  || fail "skill lacks an ordered review/finish step"
[ "$skill_review" -lt "$skill_finish" ] \
  || fail "skill runs finish (line $skill_finish) before review (line $skill_review)"

# Re-running enable keeps the block idempotent (no duplicated review step).
(cd "$proj" && env PATH="$tmp/bin" "$tmp/lead" enable --yes >/dev/null) || fail "re-enable failed"
[ "$(grep -c 'lead review' "$agents")" = "1" ] || fail "re-enable duplicated the review step in AGENTS.md"
[ "$(grep -c '<!-- lead-flow begin' "$agents")" = "1" ] || fail "re-enable duplicated the managed block"

echo "PASS: enable distributes review/risk lead-flow docs"
