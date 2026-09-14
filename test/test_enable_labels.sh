#!/usr/bin/env bash
# issue #172/#174: `lead enable` verifies required issue labels (needs-review,
# ready, blocked) and `lead enable --write` creates the missing ones.
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

# --- stateful dummy gh: `label list` reads $GH_LABEL_STATE (one name per
# --- line), `label create <name>` appends to it; every call logs its argv.
mkdir -p "$tmp/bin"
cat > "$tmp/bin/gh" <<'EOF'
#!/usr/bin/env bash
printf '<%s>\n' "$@" >> "$GH_ARG_LOG"
if [ "${LEAD_TEST_GH_FAIL:-0}" = "1" ]; then
  echo "gh: HTTP 401: Bad credentials (HTTP 401)" >&2
  exit 1
fi
if [ "$1" = "label" ] && [ "$2" = "list" ]; then
  first=1
  printf '['
  while IFS= read -r name; do
    [ -n "$name" ] || continue
    [ "$first" = 1 ] || printf ','
    first=0
    printf '{"name":"%s"}' "$name"
  done < "$GH_LABEL_STATE"
  printf ']'
  exit 0
fi
if [ "$1" = "label" ] && [ "$2" = "create" ]; then
  printf '%s\n' "$3" >> "$GH_LABEL_STATE"
  exit 0
fi
echo "unexpected gh call: $@" >&2
exit 3
EOF
chmod +x "$tmp/bin/gh"
export PATH="$tmp/bin:$PATH"
export GH_ARG_LOG="$tmp/gh-args.log"
export GH_LABEL_STATE="$tmp/gh-labels.txt"

proj="$tmp/project"
mkdir -p "$proj"
cd "$proj"
git init >/dev/null || fail "git init failed"
git config user.email "test@example.com"
git config user.name "Test"
git remote add origin "https://github.com/o/r.git"

clear_log() { : > "$GH_ARG_LOG"; }
set_labels() { printf '%s' "$1" > "$GH_LABEL_STATE"; }
label_count() { grep -c . "$GH_LABEL_STATE"; }

# --- 1. dry-run reports missing labels and creates nothing ---
set_labels 'needs-review
'
clear_log
out="$("$tmp/lead" enable)" || fail "enable dry-run failed"
case "$out" in *"label/ready: missing"*) ;; *) fail "dry-run missing label/ready missing line: $out";; esac
case "$out" in *"label/blocked: missing"*) ;; *) fail "dry-run missing label/blocked missing line: $out";; esac
[ "$(label_count)" = "1" ] || fail "dry-run created labels"
case "$(cat "$GH_ARG_LOG")" in *"create"*) fail "dry-run called gh label create";; esac

# --- 2. --check exits 1 with missing lines ---
out="$("$tmp/lead" enable --check 2>&1)" && fail "enable --check succeeded with missing labels"
case "$out" in *"label/blocked: missing"*) ;; *) fail "missing label/blocked missing line: $out";; esac
case "$out" in *"label/needs-review: ok"*) ;; *) fail "missing label/needs-review ok line: $out";; esac

# --- 3. --write --yes creates the missing labels ---
clear_log
out="$("$tmp/lead" enable --write --yes)" || fail "enable --write failed: $out"
case "$out" in *"label/ready: created"*) ;; *) fail "missing label/ready created line: $out";; esac
case "$out" in *"label/blocked: created"*) ;; *) fail "missing label/blocked created line: $out";; esac
case "$out" in *"label/needs-review: ok"*) ;; *) fail "present label must stay ok: $out";; esac
case "$(cat "$GH_ARG_LOG")" in *"label"*"<create>"*"ready"*) ;; *) fail "gh label create ready was not called";; esac
case "$(cat "$GH_ARG_LOG")" in *"<--repo>"*"<o/r>"*) ;; *) fail "gh label create was not scoped to o/r";; esac
[ "$(label_count)" = "3" ] || fail "expected 3 labels after --write"

# --- 4. --check passes once labels exist ---
out="$("$tmp/lead" enable --check)" || fail "enable --check failed after creation: $out"
case "$out" in *"label/ready: ok"*) ;; *) fail "missing label/ready ok line: $out";; esac

# --- 5. re-run is idempotent: no changes, no re-creation ---
clear_log
out="$("$tmp/lead" enable --write --yes)" || fail "re-enable failed"
case "$out" in *"no changes"*) ;; *) fail "re-enable not idempotent: $out";; esac
case "$(cat "$GH_ARG_LOG")" in *"create"*) fail "re-enable recreated labels";; esac
[ "$(label_count)" = "3" ] || fail "label count changed on re-enable"

# --- 6. gh failure: --write still installs locally with a skip warning ---
set_labels ''
export LEAD_TEST_GH_FAIL=1
proj2="$tmp/nogithub"
mkdir -p "$proj2"
cd "$proj2"
git init >/dev/null || fail "git init failed"
git config user.email "test@example.com"
git config user.name "Test"
git remote add origin "https://github.com/o/r.git"
out="$("$tmp/lead" enable --write --yes)" || fail "enable --write failed on gh error (must warn, not fail): $out"
case "$out" in *"skip"*) ;; *) fail "gh failure missing skip warning: $out";; esac
[ -f AGENTS.md ] || fail "local AGENTS.md was not installed on gh error"
export LEAD_TEST_GH_FAIL=0
cd "$proj"

# --- 7. no origin remote: label handling skipped, gh never called ---
git remote remove origin || fail "git remote remove failed"
clear_log
out="$("$tmp/lead" enable --check)" || fail "enable --check without remote failed: $out"
case "$out" in *"skip"*) ;; *) fail "no-remote run missing skip notice: $out";; esac
[ ! -s "$GH_ARG_LOG" ] || fail "gh was called without a remote: $(cat "$GH_ARG_LOG")"

echo "enable label checks passed"
