#!/usr/bin/env bash
# issue #93 Part A/B: `lead dispatch --once` の振る舞いテスト。
# 一時HOME・一時リポジトリ・ダミーgh・ダミーエージェントの下で
#   ready 取得 → worktree 払い出し → ヘッドレス起動 argv → 完了で削除
#   失敗 3 回で blocked ラベル + コメント、以後は再ディスパッチしない
#   保護未設定なら起動拒否（#67）、--skip-protection-check で警告付き続行
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
export LEAD_STATE_FILE="$tmp/state/workflows.json"
mkdir -p "$HOME"

repo="$tmp/repo"
git init -q -b main "$repo" || fail "git init failed"
git -C "$repo" config user.email "t@t"
git -C "$repo" config user.name "t"
echo x > "$repo/f.txt"
git -C "$repo" add . && git -C "$repo" commit -qm init
git -C "$repo" remote add origin https://github.com/o/r.git

export GH_LOG="$tmp/gh.log" AGENT_LOG="$tmp/agent.log" CLOSED_MARK="$tmp/closed"
: > "$GH_LOG"; : > "$AGENT_LOG"

# PROTECTED=1: main has classic protection with a required check.
# unset: classic endpoint 404s and no rulesets apply (unprotected repo).
mkdir -p "$tmp/bin"
cat > "$tmp/bin/gh" <<'EOF'
#!/bin/sh
echo "GH $*" >> "$GH_LOG"
case "$1 $2" in
  "issue list")
    case "$*" in *"--label ready"*) printf '[{"number":7,"title":"dispatch me"}]' ;; *) printf '[]' ;; esac ;;
  "issue view")
    if [ -f "$CLOSED_MARK" ]; then st=CLOSED; else st=OPEN; fi
    printf '{"number":7,"title":"dispatch me","body":"do the thing","state":"%s"}' "$st" ;;
  "issue edit"|"issue comment"|"auth status") : ;;
  "api user") printf 'octocat\n' ;;
  "api repos/o/r")
    case "$*" in
      *default_branch*) printf 'main\n' ;;
      *allow_auto_merge*) printf 'true\n' ;;
      *) echo "unexpected gh api jq: $*" >&2; exit 3 ;;
    esac ;;
  "api repos/o/r/branches/main/protection")
    if [ -n "${PROTECTED:-}" ]; then
      printf '{"required_status_checks":{"contexts":["test"]},"required_pull_request_reviews":{}}'
    else
      echo "gh: Branch not protected (HTTP 404)" >&2; exit 1
    fi ;;
  "api repos/o/r/rules/branches/main") printf '[]' ;;
  *) echo "unexpected gh call: $*" >&2; exit 3 ;;
esac
EOF
cat > "$tmp/bin/claude" <<'EOF'
#!/bin/sh
for a in "$@"; do printf '<%s>\n' "$a" >> "$AGENT_LOG"; done
printf 'CWD=%s\n' "$(pwd)" >> "$AGENT_LOG"
[ -n "${AGENT_FAIL:-}" ] && exit 1
touch "$CLOSED_MARK"
EOF
chmod +x "$tmp/bin/gh" "$tmp/bin/claude"
export PATH="$tmp/bin:$PATH"

CGO_ENABLED=0 go build -o "$tmp/lead" ./cmd/lead || fail "go build failed"

# --- 0. safety gate (#67): unprotected main → refuse before any launch -------
if out="$(cd "$repo" && "$tmp/lead" dispatch --once --parallel 1 --agent claude 2>&1)"; then
  fail "dispatch on unprotected repo exited 0: $out"
fi
case "$out" in *"not protected"*"--skip-protection-check"*) ;; *) fail "refusal lacks guidance: $out";; esac
grep -q '^GH api repos/o/r/branches/main/protection$' "$GH_LOG" || fail "protection API not consulted: $(cat "$GH_LOG")"
[ -s "$AGENT_LOG" ] && fail "agent launched despite refusal"
grep -q '^GH issue list' "$GH_LOG" && fail "ready issues listed before the gate"

# --skip-protection-check: warns, then proceeds (success path below).
rm -f "$CLOSED_MARK"; : > "$GH_LOG"
out="$(cd "$repo" && "$tmp/lead" dispatch --once --parallel 1 --agent claude --skip-protection-check 2>&1)" || fail "dispatch --skip-protection-check exited non-zero: $out"
case "$out" in *WARNING*"not protected"*) ;; *) fail "skip run lacks warning: $out";; esac
grep -qxF 'CWD='"$repo/.worktrees/issue-7" "$AGENT_LOG" || fail "skip run did not launch agent: $(cat "$AGENT_LOG")"

# --- 1. success path (protected repo) -----------------------------------------
export PROTECTED=1
rm -f "$CLOSED_MARK"; : > "$GH_LOG"; : > "$AGENT_LOG"
out="$(cd "$repo" && "$tmp/lead" dispatch --once --parallel 1 --agent claude 2>&1)" || fail "dispatch --once exited non-zero: $out"
case "$out" in *WARNING*) fail "protected repo must not warn: $out";; esac

wt="$repo/.worktrees/issue-7"
grep -qxF '<-p>' "$AGENT_LOG" || fail "agent not launched in print mode: $(cat "$AGENT_LOG")"
grep -qxF '<--dangerously-skip-permissions>' "$AGENT_LOG" || fail "agent missing skip-permissions flag"
grep -q 'Issue #7: dispatch me' "$AGENT_LOG" || fail "prompt missing issue header"
grep -q 'do the thing' "$AGENT_LOG" || fail "prompt missing issue body"
grep -qxF "CWD=$wt" "$AGENT_LOG" || fail "agent cwd was not the worktree: $(cat "$AGENT_LOG")"
[ -d "$wt" ] && fail "worktree not removed after completion"
git -C "$repo" worktree list | grep -q "issue-7" && fail "worktree still registered"
case "$out" in *"#7"*"completed"*) ;; *) fail "output lacks completed report: $out";; esac
if [ -f "$LEAD_STATE_FILE" ] && grep -q '"issue": 7' "$LEAD_STATE_FILE"; then fail "state record for #7 left behind"; fi
ls "$tmp/state/logs"/issue-7*.log >/dev/null 2>&1 || fail "agent log file not written under state dir"

# --- 2. failure path: 3 attempts → blocked, then no re-dispatch ---------------
rm -f "$CLOSED_MARK"; : > "$AGENT_LOG"; : > "$GH_LOG"
export AGENT_FAIL=1
for i in 1 2 3; do
  (cd "$repo" && "$tmp/lead" dispatch --once --parallel 1 --agent claude >/dev/null 2>&1) || fail "dispatch run $i exited non-zero"
done
grep -qxF 'GH issue edit 7 --remove-label ready' "$GH_LOG" || fail "ready label not removed: $(cat "$GH_LOG")"
grep -qxF 'GH issue edit 7 --add-label blocked' "$GH_LOG" || fail "blocked label not added"
grep -q '^GH issue comment 7 --body ' "$GH_LOG" || fail "blocked comment not posted"
launches_before="$(grep -c '^CWD=' "$AGENT_LOG")"
[ "$launches_before" -eq 3 ] || fail "expected 3 launches, got $launches_before"
grep -q '"status": "blocked"' "$LEAD_STATE_FILE" || fail "state not blocked"

(cd "$repo" && "$tmp/lead" dispatch --once --parallel 1 --agent claude >/dev/null 2>&1) || fail "dispatch run 4 exited non-zero"
launches_after="$(grep -c '^CWD=' "$AGENT_LOG")"
[ "$launches_after" -eq 3 ] || fail "blocked issue was re-dispatched ($launches_after launches)"

# --- 2b. doctor reports the repository gates (#67) -----------------------------
unset PROTECTED
doc_out="$(cd "$repo" && "$tmp/lead" doctor --offline 2>&1)" || true
case "$doc_out" in *"branch protection"*skipped*) ;; *) fail "doctor --offline should skip protection: $doc_out";; esac
if doc_out="$(cd "$repo" && "$tmp/lead" doctor 2>&1)"; then
  fail "doctor on unprotected repo exited 0: $doc_out"
fi
case "$doc_out" in *"branch protection"*"not protected"*"required checks"*) ;; *) fail "doctor lacks protection findings: $doc_out";; esac
export PROTECTED=1
doc_json="$(cd "$repo" && "$tmp/lead" doctor --json 2>&1)" || fail "doctor on protected repo failed: $doc_json"
python3 - "$doc_json" <<'PY' || fail "doctor json lacks OK protection checks: $doc_json"
import json, sys
d = json.loads(sys.argv[1]); c = {x["name"]: x for x in d["checks"]}
assert c["branch protection"]["ok"] and c["branch protection"]["required"], c
assert c["required checks"]["ok"] and "test" in c["required checks"]["detail"], c
assert c["auto-merge"]["ok"], c
PY

# --- 3. run keeps work as hidden alias ----------------------------------------
"$tmp/lead" run --help | grep -q "lead run" || fail "lead run --help missing usage"
"$tmp/lead" work --help >/dev/null 2>&1 || fail "lead work alias broken"
"$tmp/lead" --help | grep -qE '^[[:space:]]+work[[:space:]]' && fail "work alias should be hidden from root help"
"$tmp/lead" --help | grep -qE '^[[:space:]]+(run|dispatch)[[:space:]]' || fail "root help missing run/dispatch"

echo "dispatch tests passed"
