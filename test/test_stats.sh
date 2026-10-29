#!/usr/bin/env bash
# issue #274: `lead stats` aggregates runs.jsonl.
# Behavior only: exit status, stdout, and the --json shape.
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
unset XDG_STATE_HOME || true
mkdir -p "$HOME"

export LEAD_STATE_FILE="$tmp/state/workflows.json"
mkdir -p "$tmp/state"

CGO_ENABLED=0 go build -o "$tmp/lead" ./cmd/lead || fail "go build failed"

# --- empty history ---
out="$("$tmp/lead" stats)"
[ "$out" = "no runs recorded" ] || fail "empty stats: $out"

# --- 3 runs, 2 completed ---
cat > "$tmp/state/runs.jsonl" <<'EOF'
{"issue":1,"phase":"run","agent":"devin","outcome":"completed","started_at":"2026-10-29T01:00:00Z","ended_at":"2026-10-29T01:10:00Z"}
{"issue":2,"phase":"run","agent":"devin","outcome":"completed","started_at":"2026-10-29T02:00:00Z","ended_at":"2026-10-29T02:30:00Z"}
{"issue":3,"phase":"run","agent":"devin","outcome":"blocked","started_at":"2026-10-29T03:00:00Z","ended_at":"2026-10-29T03:20:00Z"}
{"issue":1,"phase":"merge","outcome":"completed","scenarios":"1/1"}
{"issue":2,"phase":"merge","outcome":"completed","scenarios":"2/2"}
{"issue":3,"phase":"merge","outcome":"paused","scenarios":"1/2"}
{"repo":"other/repo","issue":9,"phase":"run","agent":"devin","outcome":"blocked","started_at":"2026-10-29T04:00:00Z","ended_at":"2026-10-29T04:10:00Z"}
EOF

out="$("$tmp/lead" stats)"
case "$out" in
  *"runs: 3"*) ;;
  *) fail "stats missing run count: $out" ;;
esac
case "$out" in
  *"success rate: 66.7%"*) ;;
  *) fail "stats missing success rate: $out" ;;
esac
case "$out" in
  *"median duration: 20m0s"*) ;;
  *) fail "stats missing median duration: $out" ;;
esac
case "$out" in
  *"scenarios: 4/5"*) ;;
  *) fail "stats missing scenario counts: $out" ;;
esac

# --- JSON shape ---
"$tmp/lead" stats --json > "$tmp/stats.json"
python3 - "$tmp/stats.json" <<'PY' || fail "stats --json shape"
import json, sys
d = json.load(open(sys.argv[1]))
assert d["runs"] == 3, d
assert d["completed"] == 2, d
assert d["failed"] == 1, d
assert abs(d["success_rate"] - 2/3) < 1e-6, d
assert d["median_duration_seconds"] == 1200, d
PY

# --- malformed line is skipped, not fatal ---
printf 'not json\n' >> "$tmp/state/runs.jsonl"
out="$("$tmp/lead" stats)"
case "$out" in *"runs: 3"*) ;; *) fail "malformed line changed the count: $out" ;; esac

[ "$HOME" = "$tmp/home" ] || fail "HOME isolation broken"

echo "stats behavioral checks passed"
