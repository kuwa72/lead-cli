#!/usr/bin/env bash
# issue #38: CI guard against grep-only tests (AGENTS.md rule 1).
# Test scripts must assert behavior (exec args, stdio, exit codes, artifacts),
# never just grep implementation source for strings/flags.
# A `# allow-grep: <reason>` trailing comment opts a line out explicitly.
set -euo pipefail

SCAN_DIR="${TEST_SCAN_DIR:-test}"

# check_file returns 0 when clean, 1 when the file greps implementation source.
check_file() {
  local file="$1"
  local hits
  hits="$(grep -nE 'grep[[:space:]].*(internal/|cmd/|bin/|\.go\b)' "$file" 2>/dev/null | grep -v 'allow-grep:' || true)"
  if [ -n "$hits" ]; then
    echo "source-grep found in $file:" >&2
    echo "$hits" >&2
    return 1
  fi
  return 0
}

fail() { echo "FAIL: $*" >&2; exit 1; }

# --- self-verification on fixtures (behavioral, temp dir only) ---
self_tmp="$(mktemp -d)"
trap 'rm -rf "$self_tmp"' EXIT

cat > "$self_tmp/test_ok.sh" <<'EOF'
#!/usr/bin/env bash
out="$("$tmp/lead" version)" || exit 1
case "$out" in *lead*) ;; *) exit 1;; esac
EOF
cat > "$self_tmp/test_bad.sh" <<'EOF'
#!/usr/bin/env bash
grep -q -- '--agent' bin/lead || exit 1
EOF
cat > "$self_tmp/test_allowed.sh" <<'EOF'
#!/usr/bin/env bash
grep -q 'internal/' docs/testing.md || exit 1 # allow-grep: docs mention, not a test assertion
EOF

check_file "$self_tmp/test_ok.sh" || fail "clean fixture flagged"
check_file "$self_tmp/test_bad.sh" && fail "violating fixture NOT flagged"
check_file "$self_tmp/test_allowed.sh" || fail "allow-grep fixture flagged"
echo "self-check passed (clean ok / violation caught / allow-list honored)"

# --- real scan of the repo test suite ---
violations=0
self_name="$(basename "$0")"
for f in "$SCAN_DIR"/test_*.sh; do
  [ -f "$f" ] || continue
  # Skip this guard itself: its self-verification fixtures intentionally
  # contain a violating grep line inside a heredoc.
  [ "$(basename "$f")" = "$self_name" ] && continue
  check_file "$f" || violations=1
done
[ "$violations" -eq 0 ] || fail "grep-only test assertions detected (see hits above)"
echo "no source-grep test assertions in $SCAN_DIR"
