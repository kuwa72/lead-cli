#!/usr/bin/env bash
# issue #214: inbox-config.json の agent を say/dispatch/run(work)/resume の
# 既定エージェントにする振る舞いテスト。
# 一時HOME・一時リポジトリ・ダミー gh/herdr/各エージェントの下で
#   - dispatch --once が config agent の headless argv で起動する
#   - say --dry-run が config agent を起動する
#   - work / picker 経路 run / resume が send-text に config agent のコマンドを送る
#   - --agent / $LEAD_SPEC_AGENT(say) / ピッカー明示 agent / resume の前回記録が優先
#   - config 非存在・JSON 不正・agent 空のとき agy にフォールバック
# をアサートする（ソースgrep検査なし・実HOME/実リポジトリに触れない）。
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
export XDG_CACHE_HOME="$tmp/cache"
unset XDG_STATE_HOME || true
unset LEAD_SPEC_AGENT || true
export LEAD_NOTIFY=0
export LEAD_STATE_FILE="$tmp/state/workflows.json"
export HERDR_ENV=1
mkdir -p "$HOME" "$tmp/state" "$XDG_CACHE_HOME"

repo="$tmp/repo"
git init -q -b main "$repo" || fail "git init failed"
git -C "$repo" config user.email "t@t"
git -C "$repo" config user.name "t"
echo x > "$repo/f.txt"
git -C "$repo" add . && git -C "$repo" commit -qm init
git -C "$repo" remote add origin https://github.com/o/r.git
git -C "$repo" branch "issue/36-ports-adapter"

export GH_LOG="$tmp/gh.log" HERDR_LOG="$tmp/herdr.log" AGENT_OUT="$tmp/agent.out"
mkdir -p "$tmp/bin"
for a in claude agy codex devin; do
  : > "$tmp/$a.log"
done
: > "$GH_LOG"; : > "$HERDR_LOG"
printf '[{"title":"feat: t","body":"b"}]' > "$AGENT_OUT"

cat > "$tmp/bin/gh" <<'EOF'
#!/bin/sh
echo "GH $*" >> "$GH_LOG"
case "$1 $2" in
  "issue list")
    case "$*" in
      *"--label ready"*) printf '[{"number":7,"title":"dispatch me"}]' ;;
      *) printf '[{"number":36,"title":"ports adapter"}]' ;;
    esac ;;
  "issue view")
    case "$3" in
      7) printf '{"number":7,"title":"dispatch me","body":"do the thing","state":"OPEN"}' ;;
      36) printf '{"number":36,"title":"ports adapter","body":"demo body","state":"OPEN"}' ;;
      *) echo "issue not found: $3" >&2; exit 1 ;;
    esac ;;
  "pr view") echo "pr not found: $3" >&2; exit 1 ;;
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
# Each dummy agent logs its argv (one <arg> per line) + cwd, then prints
# canned spec output so `say` can parse it.
for a in claude agy codex devin; do
  cat > "$tmp/bin/$a" <<EOF
#!/bin/sh
for x in "\$@"; do printf '<%s>\n' "\$x" >> "$tmp/$a.log"; done
cat "$AGENT_OUT"
EOF
  chmod +x "$tmp/bin/$a"
done
chmod +x "$tmp/bin/gh"
cat > "$tmp/bin/herdr" <<'EOF'
#!/bin/sh
for x in "$@"; do printf '<%s>\n' "$x" >> "$HERDR_LOG"; done
case "$1 $2" in
  "pane split") printf '{"result":{"pane":{"pane_id":"pane-1"}}}' ;;
  "pane send-text") : ;;
  *) exit 0 ;;
esac
EOF
chmod +x "$tmp/bin/herdr"
export PATH="$tmp/bin:$PATH"

CGO_ENABLED=0 go build -o "$tmp/lead" ./cmd/lead || fail "go build failed"

write_config() { printf '%s' "$1" > "$tmp/state/inbox-config.json"; }

# --- 1. config agent=claude ---------------------------------------------------
write_config '{"agent":"claude"}'

# say --dry-run runs the spec agent headlessly with claude argv.
: > "$tmp/claude.log"; : > "$tmp/agy.log"
out="$(cd "$repo" && "$tmp/lead" say "x" --dry-run 2>&1)" || fail "say exited non-zero: $out"
grep -qxF '<-p>' "$tmp/claude.log" || fail "say did not run claude -p: $(cat "$tmp/claude.log")"
grep -qxF '<--dangerously-skip-permissions>' "$tmp/claude.log" || fail "say claude argv lacks skip-permissions"
[ -s "$tmp/agy.log" ] && fail "say ran agy despite config=claude"

# LEAD_SPEC_AGENT beats config (say only).
: > "$tmp/claude.log"; : > "$tmp/codex.log"
(cd "$repo" && LEAD_SPEC_AGENT=codex "$tmp/lead" say "x" --dry-run >/dev/null 2>&1) || fail "say with LEAD_SPEC_AGENT failed"
grep -qxF '<exec>' "$tmp/codex.log" || fail "say did not run codex exec: $(cat "$tmp/codex.log")"
[ -s "$tmp/claude.log" ] && fail "say ran claude despite LEAD_SPEC_AGENT=codex"

# --agent beats config.
: > "$tmp/claude.log"; : > "$tmp/devin.log"
(cd "$repo" && "$tmp/lead" say "x" --dry-run --agent devin >/dev/null 2>&1) || fail "say --agent devin failed"
grep -qxF '<--permission-mode>' "$tmp/devin.log" || fail "say did not run devin: $(cat "$tmp/devin.log")"

# dispatch --once launches claude headless (claude -p --dangerously-skip-permissions).
# Each dispatch run starts from a clean state file: a leftover record is
# settled by the next pass's reconnect instead of being re-dispatched.
: > "$tmp/claude.log"; : > "$tmp/agy.log"; rm -f "$LEAD_STATE_FILE"
out="$(cd "$repo" && "$tmp/lead" dispatch --once --parallel 1 2>&1)" || fail "dispatch --once exited non-zero: $out"
grep -qxF '<-p>' "$tmp/claude.log" || fail "dispatch did not run claude -p: $(cat "$tmp/claude.log")"
grep -qxF '<--dangerously-skip-permissions>' "$tmp/claude.log" || fail "dispatch claude argv lacks skip-permissions"
grep -q 'Issue #7: dispatch me' "$tmp/claude.log" || fail "dispatch prompt missing issue header"
[ -s "$tmp/agy.log" ] && fail "dispatch ran agy despite config=claude"

# dispatch --agent beats config.
: > "$tmp/claude.log"; : > "$tmp/codex.log"; rm -f "$LEAD_STATE_FILE"
(cd "$repo" && "$tmp/lead" dispatch --once --parallel 1 --agent codex >/dev/null 2>&1) || fail "dispatch --agent codex failed"
grep -qxF '<exec>' "$tmp/codex.log" || fail "dispatch did not run codex exec: $(cat "$tmp/codex.log")"
[ -s "$tmp/claude.log" ] && fail "dispatch ran claude despite --agent codex"

# work 36 prepares `claude "<prompt>"` via herdr send-text.
: > "$HERDR_LOG"
out="$(cd "$repo" && "$tmp/lead" work 36 2>&1)" || fail "work 36 exited non-zero: $out"
case "$out" in *"Agent: claude"*) ;; *) fail "work output lacks 'Agent: claude': $out";; esac
grep -q 'claude "' "$HERDR_LOG" || fail "work send-text lacks claude command: $(cat "$HERDR_LOG")"

# picker path (no number): LEAD_TEST_SELECTION=36 (no agent) uses config too.
rm -f "$LEAD_STATE_FILE"
: > "$HERDR_LOG"
out="$(cd "$repo" && LEAD_TEST_SELECTION=36 "$tmp/lead" run 2>&1)" || fail "run (picker) exited non-zero: $out"
case "$out" in *"Agent: claude"*) ;; *) fail "picker run lacks 'Agent: claude': $out";; esac
grep -q 'claude "' "$HERDR_LOG" || fail "picker send-text lacks claude command: $(cat "$HERDR_LOG")"

# picker explicit agent is kept: LEAD_TEST_SELECTION=36:devin beats config.
rm -f "$LEAD_STATE_FILE"
: > "$HERDR_LOG"
out="$(cd "$repo" && LEAD_TEST_SELECTION=36:devin "$tmp/lead" run 2>&1)" || fail "run (picker devin) failed: $out"
case "$out" in *"Agent: devin"*) ;; *) fail "picker run lacks 'Agent: devin': $out";; esac
grep -q 'devin "' "$HERDR_LOG" || fail "picker send-text lacks devin command: $(cat "$HERDR_LOG")"

# resume without a recorded agent uses config.
rm -f "$LEAD_STATE_FILE"
: > "$HERDR_LOG"
out="$(cd "$repo" && "$tmp/lead" resume 36 2>&1)" || fail "resume 36 exited non-zero: $out"
case "$out" in *"Agent: claude"*) ;; *) fail "resume output lacks 'Agent: claude': $out";; esac
grep -q 'claude "' "$HERDR_LOG" || fail "resume send-text lacks claude command: $(cat "$HERDR_LOG")"

# resume keeps the recorded previous agent over config.
cat > "$LEAD_STATE_FILE" <<'EOF'
{"version":1,"workflows":[{"repository":"o/r","issue":36,"mode":"implement","branch":"issue/36-ports-adapter","status":"in_progress","agent":"devin","updated_at":"2026-09-18T00:00:00Z"}]}
EOF
: > "$HERDR_LOG"
out="$(cd "$repo" && "$tmp/lead" resume 36 2>&1)" || fail "resume 36 (recorded devin) failed: $out"
case "$out" in *"Agent: devin"*) ;; *) fail "resume should keep recorded devin over config: $out";; esac
grep -q 'devin "' "$HERDR_LOG" || fail "resume send-text lacks devin command: $(cat "$HERDR_LOG")"

# --- 2. fallbacks --------------------------------------------------------------
# corrupt JSON config → agy.
write_config '{broken'
: > "$tmp/agy.log"; : > "$tmp/claude.log"
(cd "$repo" && "$tmp/lead" say "x" --dry-run >/dev/null 2>&1) || fail "say on corrupt config failed"
grep -qxF '<--print-timeout>' "$tmp/agy.log" || fail "corrupt config: agy not used: $(cat "$tmp/agy.log")"
[ -s "$tmp/claude.log" ] && fail "corrupt config ran claude"

# empty agent field → agy.
write_config '{"agent":""}'
: > "$tmp/agy.log"; : > "$tmp/claude.log"
(cd "$repo" && "$tmp/lead" say "x" --dry-run >/dev/null 2>&1) || fail "say on empty-agent config failed"
grep -qxF '<--print-timeout>' "$tmp/agy.log" || fail "empty agent: agy not used: $(cat "$tmp/agy.log")"

# no config file → agy (dispatch path).
rm -f "$tmp/state/inbox-config.json"
: > "$tmp/agy.log"; : > "$tmp/claude.log"; rm -f "$LEAD_STATE_FILE"
(cd "$repo" && "$tmp/lead" dispatch --once --parallel 1 >/dev/null 2>&1) || fail "dispatch without config failed"
grep -qxF '<--print-timeout>' "$tmp/agy.log" || fail "no config: dispatch did not fall back to agy: $(cat "$tmp/agy.log")"
[ -s "$tmp/claude.log" ] && fail "no config: dispatch ran claude"

# no config file → work send-text uses agy -i.
rm -f "$LEAD_STATE_FILE"
: > "$HERDR_LOG"
out="$(cd "$repo" && "$tmp/lead" work 36 2>&1)" || fail "work without config failed: $out"
case "$out" in *"Agent: agy"*) ;; *) fail "work without config lacks 'Agent: agy': $out";; esac
grep -q 'agy -i "' "$HERDR_LOG" || fail "work send-text lacks agy -i command: $(cat "$HERDR_LOG")"

[ "$HOME" = "$tmp/home" ] || fail "HOME isolation broken"

echo "PASS: inbox-config agent defaults"
