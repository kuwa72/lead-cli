#!/usr/bin/env bash
# issue #207: `lead dispatch --once` の着手順テスト。
# 一時HOME・一時リポジトリ・ダミーgh・ダミーエージェントの下で
#   ready Issue が親(トラッキング)Issueの sub_issues 順に起動される
#   open な blockedBy を持つ ready Issue がスキップ(deferred)される
# をアサートする（grep検査なし・実HOME/実リポジトリに触れない）。
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
export LEAD_NOTIFY=0
export LEAD_STATE_FILE="$tmp/state/workflows.json"
mkdir -p "$HOME"

repo="$tmp/repo"
git init -q -b main "$repo" || fail "git init failed"
git -C "$repo" config user.email "t@t"
git -C "$repo" config user.name "t"
echo x > "$repo/f.txt"
git -C "$repo" add . && git -C "$repo" commit -qm init
git -C "$repo" remote add origin https://github.com/o/r.git

export GH_LOG="$tmp/gh.log" AGENT_LOG="$tmp/agent.log" CLOSED_DIR="$tmp/closed"
mkdir -p "$CLOSED_DIR"
: > "$GH_LOG"; : > "$AGENT_LOG"

# ready queue (listed newest-first): #3,#1,#2 are sub-issues of tracking
# issue #50 (order 1,2,3); #8 has an open blocked-by dependency on #35.
mkdir -p "$tmp/bin"
cat > "$tmp/bin/gh" <<'EOF'
#!/bin/sh
echo "GH $*" >> "$GH_LOG"
n="${3:-}"
case "$1 $2" in
  "issue list")
    case "$*" in
      *"--label ready"*) printf '[{"number":3,"title":"third","parent":{"number":50}},{"number":1,"title":"first","parent":{"number":50}},{"number":2,"title":"second","parent":{"number":50}},{"number":8,"title":"gated","blockedBy":{"nodes":[{"number":35,"state":"OPEN"}],"totalCount":1}}]' ;;
      *) printf '[]' ;;
    esac ;;
  "issue view")
    case "$*" in
      *subIssues*) printf '{"state":"OPEN","subIssues":{"nodes":[{"number":1},{"number":2},{"number":3}],"totalCount":3}}' ;;
      *)
        if [ -f "$CLOSED_DIR/$n" ]; then st=CLOSED; else st=OPEN; fi
        printf '{"number":%s,"title":"issue %s","body":"do the thing","state":"%s"}' "$n" "$n" "$st" ;;
    esac ;;
  "issue edit"|"issue comment"|"auth status") : ;;
  "api user") printf 'octocat\n' ;;
  "api repos/o/r")
    case "$*" in
      *default_branch*) printf 'main\n' ;;
      *allow_auto_merge*) printf 'true\n' ;;
      *) echo "unexpected gh api jq: $*" >&2; exit 3 ;;
    esac ;;
  "api repos/o/r/branches/main/protection")
    printf '{"required_status_checks":{"contexts":["test"]},"required_pull_request_reviews":{}}' ;;
  "api repos/o/r/rules/branches/main") printf '[]' ;;
  *) echo "unexpected gh call: $*" >&2; exit 3 ;;
esac
EOF
cat > "$tmp/bin/claude" <<'EOF'
#!/bin/sh
# Record the launched issue number and mark it closed for the re-view.
n="$(basename "$(pwd)")"; n="${n#issue-}"
printf 'LAUNCH %s\n' "$n" >> "$AGENT_LOG"
touch "$CLOSED_DIR/$n"
EOF
chmod +x "$tmp/bin/gh" "$tmp/bin/claude"
export PATH="$tmp/bin:$PATH"

CGO_ENABLED=0 go build -o "$tmp/lead" ./cmd/lead || fail "go build failed"

out="$(cd "$repo" && "$tmp/lead" dispatch --once --parallel 1 --agent claude 2>&1)" || fail "dispatch --once exited non-zero: $out"

order="$(grep '^LAUNCH ' "$AGENT_LOG" | awk '{print $2}' | tr '\n' ' ')"
[ "$order" = "1 2 3 " ] || fail "launch order = '$order', want '1 2 3 ' (agent log: $(cat "$AGENT_LOG"))"

grep -qxF 'GH issue view 50 --json state,subIssues' "$GH_LOG" || fail "tracking issue #50 sub-issues not consulted: $(cat "$GH_LOG")"

case "$out" in *"#8 deferred"*) ;; *) fail "output lacks deferred report for #8: $out";; esac
case "$out" in *"all done"*) ;; *) : ;; esac

grep -q '^LAUNCH 8$' "$AGENT_LOG" && fail "blocked-by issue #8 was dispatched"

# Once the blocker is closed, #8 dispatches on the next pass.
cat > "$tmp/bin/gh" <<'EOF'
#!/bin/sh
echo "GH $*" >> "$GH_LOG"
n="${3:-}"
case "$1 $2" in
  "issue list")
    case "$*" in
      *"--label ready"*) printf '[{"number":8,"title":"gated","blockedBy":{"nodes":[{"number":35,"state":"CLOSED"}],"totalCount":1}}]' ;;
      *) printf '[]' ;;
    esac ;;
  "issue view")
    if [ -f "$CLOSED_DIR/$n" ]; then st=CLOSED; else st=OPEN; fi
    printf '{"number":%s,"title":"issue %s","body":"do the thing","state":"%s"}' "$n" "$n" "$st" ;;
  "issue edit"|"issue comment"|"auth status") : ;;
  "api user") printf 'octocat\n' ;;
  "api repos/o/r")
    case "$*" in
      *default_branch*) printf 'main\n' ;;
      *allow_auto_merge*) printf 'true\n' ;;
      *) echo "unexpected gh api jq: $*" >&2; exit 3 ;;
    esac ;;
  "api repos/o/r/branches/main/protection")
    printf '{"required_status_checks":{"contexts":["test"]},"required_pull_request_reviews":{}}' ;;
  "api repos/o/r/rules/branches/main") printf '[]' ;;
  *) echo "unexpected gh call: $*" >&2; exit 3 ;;
esac
EOF
chmod +x "$tmp/bin/gh"
: > "$AGENT_LOG"

out="$(cd "$repo" && "$tmp/lead" dispatch --once --parallel 1 --agent claude 2>&1)" || fail "second dispatch exited non-zero: $out"
grep -qx 'LAUNCH 8' "$AGENT_LOG" || fail "unblocked #8 was not dispatched: $(cat "$AGENT_LOG")"
case "$out" in *"#8"*"completed"*) ;; *) fail "output lacks completed report for #8: $out";; esac

echo "dispatch order tests passed"
