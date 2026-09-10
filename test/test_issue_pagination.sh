#!/usr/bin/env bash
# issue #138: GitHub Issues pagination & limit handling behavioral tests.
# Validates that lead lists open issues with higher limits (DefaultListLimit=300),
# honors LEAD_ISSUE_LIMIT override, handles >50 issues without dropping any,
# and allows selecting issues beyond index 50.
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
export GH_LOG="$tmp/gh.log"
: > "$GH_LOG"
mkdir -p "$HOME" "$tmp/bin"

repo="$tmp/repo"
git init -q -b main "$repo"
git -C "$repo" config user.email "t@t"
git -C "$repo" config user.name "t"
echo x > "$repo/f.txt"
git -C "$repo" add .
git -C "$repo" commit -qm init

# Generate JSON array with 70 issues
GEN_ISSUES_PY=$(cat <<'PYEOF'
import json
issues = [{"number": i, "title": f"Issue title {i}"} for i in range(1, 71)]
print(json.dumps(issues))
PYEOF
)
ISSUES_JSON=$(python3 -c "$GEN_ISSUES_PY")

cat > "$tmp/bin/gh" <<GHEOF
#!/bin/sh
echo "GH \$*" >> "\$GH_LOG"
case "\$1 \$2" in
  "issue list")
    case "\$*" in
      *"--label lgtm"*)
        printf '[]'
        ;;
      *)
        cat <<'EOF'
$ISSUES_JSON
EOF
        ;;
    esac
    ;;
  "issue view")
    # Return basic issue JSON
    printf '{"number":'\$3',"title":"Issue title '\$3'","body":"body '\$3'","state":"OPEN"}'
    ;;
  "api"*)
    case "\$*" in
      *"repos/"*) printf '{"default_branch":"main"}' ;;
      *) printf '{}' ;;
    esac
    ;;
  *) exit 0 ;;
esac
GHEOF
chmod +x "$tmp/bin/gh"

cat > "$tmp/bin/herdr" <<'HEOF'
#!/bin/sh
case "$1 $2" in
  "pane split") printf '{"result":{"pane":{"pane_id":"p-test"}}}' ;;
  *) exit 0 ;;
esac
HEOF
chmod +x "$tmp/bin/herdr"

export PATH="$tmp/bin:$PATH"

CGO_ENABLED=0 go build -o "$tmp/lead" ./cmd/lead || fail "go build failed"

cd "$repo"

# 1. Default limit behavior: limit 300 and selecting issue #65 (beyond the legacy 50 limit)
: > "$GH_LOG"
LEAD_TEST_SELECTION=65:agy "$tmp/lead" run >/dev/null || fail "selecting issue #65 failed"

grep -qxF 'GH issue list --state open --limit 300 --json number,title' "$GH_LOG" \
  || fail "default issue list did not use limit 300: $(cat "$GH_LOG")"

git -C "$repo" rev-parse --verify --quiet refs/heads/issue/65-issue-title-65 >/dev/null \
  || fail "work branch for issue #65 was not created"

# 2. LEAD_ISSUE_LIMIT override behavior
: > "$GH_LOG"
git -C "$repo" checkout -q main
LEAD_ISSUE_LIMIT=150 LEAD_TEST_SELECTION=65:agy "$tmp/lead" run >/dev/null || fail "re-run with LEAD_ISSUE_LIMIT failed"
grep -qxF 'GH issue list --state open --limit 150 --json number,title' "$GH_LOG" \
  || fail "LEAD_ISSUE_LIMIT override not passed to gh issue list: $(cat "$GH_LOG")"

# 3. Error resilience: gh exits with error
cat > "$tmp/bin/gh" <<'GHEOF'
#!/bin/sh
echo "GH $*" >> "$GH_LOG"
exit 1
GHEOF

: > "$GH_LOG"
if "$tmp/lead" run >"$tmp/err.log" 2>&1; then
  fail "lead run should exit non-zero when gh issue list fails"
fi
grep -q "gh" "$tmp/err.log" || fail "expected error message mentioning gh failure"

echo "issue pagination behavioral checks passed"
