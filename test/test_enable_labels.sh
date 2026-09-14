#!/usr/bin/env bash
# issue #172: `lead enable` verifies required issue labels (needs-review,
# ready, blocked) against the repository and reports label/<name>: ok|missing.
# Behavioral checks only: exit statuses, command outputs, gh argv records.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

fail() { echo "FAIL: $*" >&2; exit 1; }

CGO_ENABLED=0 go build -o "$tmp/lead" ./cmd/lead || fail "go build failed"

export HOME="$tmp/home"
mkdir -p "$HOME"

# --- dummy gh: answers `label list` with $LEAD_TEST_LABELS JSON, logs argv ---
mkdir -p "$tmp/bin"
cat > "$tmp/bin/gh" <<'EOF'
#!/usr/bin/env bash
printf '<%s>\n' "$@" >> "$GH_ARG_LOG"
if [ "${LEAD_TEST_GH_FAIL:-0}" = "1" ]; then
  echo "gh: HTTP 401: Bad credentials (HTTP 401)" >&2
  exit 1
fi
if [ "$1" = "label" ] && [ "$2" = "list" ]; then
  printf '%s' "$LEAD_TEST_LABELS"
  exit 0
fi
echo "unexpected gh call: $@" >&2
exit 3
EOF
chmod +x "$tmp/bin/gh"
export PATH="$tmp/bin:$PATH"
export GH_ARG_LOG="$tmp/gh-args.log"

proj="$tmp/project"
mkdir -p "$proj"
cd "$proj"
git init >/dev/null || fail "git init failed"
git config user.email "test@example.com"
git config user.name "Test"
git remote add origin "https://github.com/o/r.git"

clear_log() { : > "$GH_ARG_LOG"; }

# --- 1. all labels present: --check exits 0 with ok lines ---
export LEAD_TEST_LABELS='[{"name":"needs-review"},{"name":"ready"},{"name":"blocked"}]'
clear_log
"$tmp/lead" enable --write --yes >/dev/null || fail "enable --write failed"
out="$("$tmp/lead" enable --check)" || fail "enable --check failed with all labels present: $out"
case "$out" in *"label/needs-review: ok"*) ;; *) fail "missing label/needs-review ok line: $out";; esac
case "$out" in *"label/ready: ok"*) ;; *) fail "missing label/ready ok line: $out";; esac
case "$out" in *"label/blocked: ok"*) ;; *) fail "missing label/blocked ok line: $out";; esac
case "$(cat "$GH_ARG_LOG")" in *"label"*"list"*) ;; *) fail "gh label list was not called";; esac
case "$(cat "$GH_ARG_LOG")" in *"o/r"*) ;; *) fail "gh was not scoped to o/r";; esac

# --- 2. missing label: --check exits 1 with missing lines ---
export LEAD_TEST_LABELS='[{"name":"needs-review"},{"name":"ready"}]'
clear_log
out="$("$tmp/lead" enable --check 2>&1)" && fail "enable --check succeeded with a missing label"
case "$out" in *"label/blocked: missing"*) ;; *) fail "missing label/blocked missing line: $out";; esac
case "$out" in *"label/needs-review: ok"*) ;; *) fail "missing label/needs-review ok line: $out";; esac

# --- 3. dry-run reports label status without writing ---
out="$("$tmp/lead" enable)" || fail "enable dry-run failed"
case "$out" in *"label/ready: ok"*) ;; *) fail "dry-run missing label/ready ok line: $out";; esac
case "$out" in *"label/blocked: missing"*) ;; *) fail "dry-run missing label/blocked missing line: $out";; esac

# --- 4. gh failure: --check warns and still exits 0 (local checks unblocked) ---
export LEAD_TEST_GH_FAIL=1
out="$("$tmp/lead" enable --check)" || fail "enable --check failed on gh error (must skip with warning): $out"
case "$out" in *"skip"*) ;; *) fail "gh failure missing skip warning: $out";; esac
export LEAD_TEST_GH_FAIL=0

# --- 5. no origin remote: label check skipped, gh never called ---
git remote remove origin || fail "git remote remove failed"
clear_log
out="$("$tmp/lead" enable --check)" || fail "enable --check without remote failed: $out"
case "$out" in *"skip"*) ;; *) fail "no-remote run missing skip notice: $out";; esac
[ ! -s "$GH_ARG_LOG" ] || fail "gh was called without a remote: $(cat "$GH_ARG_LOG")"

echo "enable label checks passed"
