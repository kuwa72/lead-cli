#!/usr/bin/env bash
# issue #216: `lead status` は in_progress workflow 毎に elapsed
# （started_at からの経過）と last-activity（log_path の mtime、なければ
# started_at —— dispatch watchdog と同じ定義）を表示し、--json にも
# 同フィールドを含めることをアサートする（grep 検査なし・実HOMEに触れない）。
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
unset XDG_STATE_HOME HERDR_ENV || true
mkdir -p "$HOME"

CGO_ENABLED=0 go build -o "$tmp/lead" ./cmd/lead || fail "go build failed"

state_dir="$HOME/.local/state/lead"
mkdir -p "$state_dir"

log_file="$tmp/agent-71.log"
echo "working" > "$log_file"
touch -d '5 minutes ago' "$log_file"
started_1h="$(date -u -d '1 hour ago' +%Y-%m-%dT%H:%M:%SZ)"
started_30m="$(date -u -d '30 minutes ago' +%Y-%m-%dT%H:%M:%SZ)"

# #71: log mtime 5m ago, started 1h ago → activity は mtime。
# #72: log_path なし（存在しないパス）→ activity は started_at fallback。
# #73: completed → activity フィールドなし。
cat > "$state_dir/workflows.json" <<EOF
{
  "version": 1,
  "workflows": [
    {
      "repository": "o/r",
      "issue": 71,
      "mode": "implement",
      "status": "in_progress",
      "branch": "issue/71-active",
      "agent": "agy",
      "log_path": "$log_file",
      "started_at": "$started_1h",
      "updated_at": "$started_1h"
    },
    {
      "repository": "o/r",
      "issue": 72,
      "mode": "implement",
      "status": "in_progress",
      "branch": "issue/72-nolog",
      "agent": "claude",
      "log_path": "$tmp/missing-72.log",
      "started_at": "$started_30m",
      "updated_at": "$started_30m"
    },
    {
      "repository": "o/r",
      "issue": 73,
      "mode": "implement",
      "status": "completed",
      "branch": "issue/73-done",
      "updated_at": "$started_1h"
    }
  ]
}
EOF

# 1. 人間可読: in_progress には Elapsed と Last activity、completed には無し
out="$("$tmp/lead" status)" || fail "lead status failed"
case "$out" in *"Elapsed:"*"Last activity:"*) ;; *) fail "status missing activity line: $out";; esac
sec73="${out##*issue/73-done}"
case "$sec73" in *"Last activity"*) fail "completed workflow shows activity: $out";; esac

# 2. --json: elapsed_seconds / last_activity_at / last_activity_seconds
"$tmp/lead" status --json | python3 -c '
import json, os, sys
from datetime import datetime, timezone
d = json.load(sys.stdin)
log_file = sys.argv[1]
by_issue = {w["issue"]: w for w in d["workflows"]}
w71 = by_issue[71]
assert w71["elapsed_seconds"] >= 3500, w71
assert w71["last_activity_seconds"] is not None and 200 <= w71["last_activity_seconds"] <= 400, w71
want_ts = os.path.getmtime(log_file)
got_ts = datetime.fromisoformat(w71["last_activity_at"].replace("Z", "+00:00")).timestamp()
assert abs(got_ts - want_ts) < 1, (w71["last_activity_at"], want_ts)
w72 = by_issue[72]
assert w72["last_activity_seconds"] >= 1700, w72  # started_at fallback
w73 = by_issue[73]
assert "last_activity_seconds" not in w73 and "elapsed_seconds" not in w73, w73
' "$log_file" || fail "status --json missing activity fields"

echo "PASS: test_status_activity"
