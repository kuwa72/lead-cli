#!/usr/bin/env bash
# issue #41: マージ・クローズ自動化の振る舞いテスト。
# ダミーgh＋ダミーCI状態で 待機→マージ→クローズ の一連フロー、
# 失敗時停止、auto-merge無効時の代替表示をアサートする (grep検査なし)。
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
command -v git >/dev/null || fail "git not available"

# --- stateful dummy gh: pending CI becomes pass after 2 polls ---
mkdir -p "$tmp/bin"
cat > "$tmp/bin/gh" <<'EOF'
#!/bin/sh
echo "GHLOG $@" >> "$GH_ARGS_LOG"
case "$1 $2" in
  "issue view")
    printf '{"number":36,"title":"demo","body":"b","state":"OPEN"}' ;;
  "pr view")
    printf '{"number":7,"state":"OPEN","mergeable":"MERGEABLE","mergeStateStatus":"CLEAN"}' ;;
  "pr checks")
    if [ "${GH_MODE:-pass}" = "fail" ]; then
      printf '[{"bucket":"fail","name":"test","state":"FAILURE"}]'
    else
      n=0; [ -f "$GH_COUNT" ] && n=$(cat "$GH_COUNT")
      n=$((n + 1)); echo "$n" > "$GH_COUNT"
      if [ "$n" -lt 3 ]; then
        printf '[{"bucket":"pending","name":"test","state":"PENDING"}]'
      else
        printf '[{"bucket":"pass","name":"test","state":"SUCCESS"}]'
      fi
    fi ;;
  "pr merge")
    printf '{"number":7,"state":"MERGED"}' ;;
  "issue close" | "issue comment")
    : ;;
  *)
    if [ "$1" = "api" ]; then
      if [ "${GH_MODE:-pass}" = "noauto" ]; then printf 'false'; else printf 'true'; fi
    else
      echo "unexpected gh call: $@" >&2; exit 3
    fi ;;
esac
EOF
chmod +x "$tmp/bin/gh"
export PATH="$tmp/bin:$PATH"
export GH_ARGS_LOG="$tmp/gh-args.log"
export GH_COUNT="$tmp/checks.count"

CGO_ENABLED=0 go build -o "$tmp/lead" ./cmd/lead || fail "go build failed"

mk_repo() {
  local repo="$1"
  git init -q -b main "$repo"
  git -C "$repo" config user.email "t@t"
  git -C "$repo" config user.name "t"
  echo x > "$repo/f.txt"
  git -C "$repo" add .
  git -C "$repo" commit -qm init
}

# --- Scenario A: wait(pending→pass) → merge → close ---
repo_a="$tmp/repo-a"; mk_repo "$repo_a"
export LEAD_STATE_FILE="$tmp/state-a.json" GH_MODE=pass
: > "$GH_ARGS_LOG"; rm -f "$GH_COUNT"
cd "$repo_a"
"$tmp/lead" work 36 >/dev/null || fail "scenario A: work failed"
finish_out="$("$tmp/lead" finish 36 --pr 7 --merge --close --comment done \
  --timeout 60s --poll-interval 1s)" || fail "scenario A: finish failed: $finish_out"
case "$finish_out" in *"merged"*closed*) ;; *) fail "scenario A: missing merge+close report: $finish_out";; esac
grep -q "GHLOG pr merge 7 --squash --delete-branch" "$GH_ARGS_LOG" || fail "scenario A: squash merge not invoked"
grep -q "GHLOG issue close 36" "$GH_ARGS_LOG" || fail "scenario A: issue close not invoked"
grep -q "GHLOG issue comment 36 --body done" "$GH_ARGS_LOG" || fail "scenario A: comment not posted"
polls=$(grep -c "GHLOG pr checks" "$GH_ARGS_LOG"); [ "$polls" -ge 3 ] || fail "scenario A: expected CI polling, got $polls checks calls"
python3 -c "import json;d=json.load(open('$LEAD_STATE_FILE'));w=d['workflows'][0];assert w['status']=='closed',w;assert w['pull_requests'][0]['status']=='merged',w" \
  || fail "scenario A: state not merged+closed"

# --- Scenario B: failing CI stops before merge/close ---
repo_b="$tmp/repo-b"; mk_repo "$repo_b"
export LEAD_STATE_FILE="$tmp/state-b.json" GH_MODE=fail
: > "$GH_ARGS_LOG"; rm -f "$GH_COUNT"
cd "$repo_b"
"$tmp/lead" work 36 >/dev/null || fail "scenario B: work failed"
"$tmp/lead" finish 36 --pr 7 --merge --close --timeout 30s --poll-interval 1s >/dev/null 2>&1 \
  && fail "scenario B: finish on failing CI exited 0"
grep -q "GHLOG pr merge" "$GH_ARGS_LOG" && fail "scenario B: merge attempted despite failing CI"
grep -q "GHLOG issue close" "$GH_ARGS_LOG" && fail "scenario B: close attempted despite failing CI"
python3 -c "import json;d=json.load(open('$LEAD_STATE_FILE'));assert d['workflows'][0]['status']=='in_progress',d" \
  || fail "scenario B: state touched despite failing CI"

# --- Scenario C: repo disallows auto-merge → fallback display, no merge ---
repo_c="$tmp/repo-c"; mk_repo "$repo_c"
git -C "$repo_c" remote add origin https://github.com/o/r.git
export LEAD_STATE_FILE="$tmp/state-c.json" GH_MODE=noauto
: > "$GH_ARGS_LOG"; rm -f "$GH_COUNT"
cd "$repo_c"
"$tmp/lead" work 36 >/dev/null || fail "scenario C: work failed"
fallback_out="$("$tmp/lead" finish 36 --pr 7 --timeout 60s --poll-interval 1s)" \
  || fail "scenario C: fallback pause exited non-zero"
case "$fallback_out" in *"auto-merge"*) ;; *) fail "scenario C: missing auto-merge fallback display: $fallback_out";; esac
grep -q "GHLOG pr merge" "$GH_ARGS_LOG" && fail "scenario C: merged despite auto-merge disabled"

[ "$HOME" = "$tmp/home" ] || fail "HOME isolation broken"

echo "finish flow behavioral checks passed"
