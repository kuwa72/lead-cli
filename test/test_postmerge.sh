#!/usr/bin/env bash
# issue #275: マージ後のスモークと自動 revert の振る舞いテスト。
# ダミー git / gh を PATH 注入し、スモーク失敗時に revert と incident Issue
# 作成へ渡る引数を検証する。成功時は revert も Issue 作成も起きないこと、
# `lead postmerge <n>` の手動再実行が同じ判定になることをアサートする。
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

fail() { echo "FAIL: $*" >&2; exit 1; }

REAL_GOPATH="$(go env GOPATH)"
REAL_GOCACHE="$(go env GOCACHE)"
REAL_PATH="$PATH"
export HOME="$tmp/home"
export GOPATH="$REAL_GOPATH" GOCACHE="$REAL_GOCACHE"
unset XDG_STATE_HOME || true
mkdir -p "$HOME"
command -v git >/dev/null || fail "git not available"

SHA="0123456789abcdef0123456789abcdef01234567"

# --- dummy gh: CI passes, review passed, merge commit known ---
mkdir -p "$tmp/ghbin"
cat > "$tmp/ghbin/gh" <<'EOF'
#!/bin/sh
printf 'GHLOG %s\n' "$(printf '%s' "$*" | tr '\n' ' ')" >> "$GH_ARGS_LOG"
case "$1 $2" in
  "issue view")
    printf '{"number":36,"title":"demo","body":"b","state":"OPEN","labels":[{"name":"risk:a"}]}' ;;
  "pr view")
    case "$*" in
      *"--json mergeCommit"*) printf '{"mergeCommit":{"oid":"%s"}}' "$MERGE_SHA" ;;
      *"--json labels"*) printf 'review:pass\n' ;;
      *"--json headRefOid"*) printf '{"headRefOid":"%s"}' "$MERGE_SHA" ;;
      *"--json comments"*) printf '{"comments":[{"author":{"login":"lead"},"body":"<!-- lead-review sha=%s verdict=pass -->","createdAt":"2026-10-25T00:00:00Z"}]}' "$MERGE_SHA" ;;
      *"--json files"*) printf 'src/main.go\n' ;;
      *) printf '{"number":7,"state":"OPEN","mergeable":"MERGEABLE","mergeStateStatus":"CLEAN"}' ;;
    esac ;;
  "pr checks") printf '[{"bucket":"pass","name":"test","state":"SUCCESS"}]' ;;
  "pr create") printf 'https://github.com/o/r/pull/201\n' ;;
  "pr merge")
    case "$*" in *--auto*) [ "${AUTO_MERGE_FAIL:-}" = "1" ] && { echo "auto-merge disabled" >&2; exit 1; } ;; esac
    printf '{"number":7,"state":"MERGED"}' ;;
  "api user") printf 'lead\n' ;;
  "api repos/o/r") printf 'main\n' ;;
  "label list") printf '[{"name":"incident"}]' ;;
  "label create") : ;;
  "issue create") printf 'https://github.com/o/r/issues/900\n' ;;
  "issue close" | "issue comment" | "issue edit") : ;;
  *) echo "unexpected gh call: $@" >&2; exit 3 ;;
esac
EOF
chmod +x "$tmp/ghbin/gh"

# --- dummy git: worktree/revert/push are logged and simulated ---
mkdir -p "$tmp/gitbin"
cat > "$tmp/gitbin/git" <<'EOF'
#!/bin/sh
printf 'GITLOG %s\n' "$(printf '%s' "$*" | tr '\n' ' ')" >> "$GIT_ARGS_LOG"
case "$1 $2" in
  "rev-parse --show-toplevel") pwd ;;
  "worktree add")
    mkdir -p "$4"; echo "$4" >> "$GIT_WT_LIST" ;;
  "worktree list")
    echo "worktree $(pwd)"
    if [ -f "$GIT_WT_LIST" ]; then
      while IFS= read -r p; do echo "worktree $p"; done < "$GIT_WT_LIST"
    fi ;;
  "worktree remove")
    rm -rf "$3"
    if [ -f "$GIT_WT_LIST" ]; then grep -vxF "$3" "$GIT_WT_LIST" > "$GIT_WT_LIST.tmp" || true; mv "$GIT_WT_LIST.tmp" "$GIT_WT_LIST"; fi ;;
  "revert --no-edit")
    [ "${GIT_REVERT_FAIL:-}" = "1" ] && { echo "conflict" >&2; exit 1; }; : ;;
  "push origin")
    [ "${GIT_PUSH_FAIL:-}" = "1" ] && { echo "push rejected" >&2; exit 1; }; : ;;
  "cat-file -e" | "fetch origin") : ;;
  "config --get") : ;;
  *) : ;;
esac
EOF
chmod +x "$tmp/gitbin/git"

export GH_ARGS_LOG="$tmp/gh-args.log"
export GIT_ARGS_LOG="$tmp/git-args.log"
export MERGE_SHA="$SHA"

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

# seed <name>: fresh repo + workflow record; sets $repo and the env for it.
seed() {
  local name="$1"
  repo="$tmp/repo-$name"
  mk_repo "$repo"
  git -C "$repo" remote add origin https://github.com/o/r.git
  mkdir -p "$tmp/state-$name"
  export LEAD_STATE_FILE="$tmp/state-$name/wf.json"
  export GIT_WT_LIST="$tmp/worktrees-$name.list"
  : > "$GIT_WT_LIST"
  : > "$GH_ARGS_LOG"; : > "$GIT_ARGS_LOG"
  ( cd "$repo" && PATH="$tmp/ghbin:$REAL_PATH" "$tmp/lead" run 36 >/dev/null ) \
    || fail "$name: lead run failed"
}

reset_logs() { : > "$GH_ARGS_LOG"; : > "$GIT_ARGS_LOG"; }

# finish_in <repo> [extra args]: run finish with the dummy git/gh on PATH.
finish_in() {
  local repo="$1"; shift
  ( cd "$repo" && PATH="$tmp/gitbin:$tmp/ghbin:$REAL_PATH" "$tmp/lead" finish 36 --pr 7 --merge --close "$@" )
}

# postmerge_in <repo>: run the manual post-merge check with dummy git/gh.
postmerge_in() {
  local repo="$1"
  ( cd "$repo" && PATH="$tmp/gitbin:$tmp/ghbin:$REAL_PATH" "$tmp/lead" postmerge 36 )
}

# --- A: smoke passes after finish → merged, closed, no revert, no incident ---
seed a; repo_a="$repo"
export LEAD_TEST_POSTMERGE_SMOKE="true"
reset_logs
out="$(finish_in "$repo_a" 2>&1)" || fail "scenario A: finish failed: $out"
case "$out" in *merged*closed*) ;; *) fail "scenario A: missing merge+close report: $out";; esac
grep -q "GITLOG worktree add --detach" "$GIT_ARGS_LOG" || fail "scenario A: smoke worktree not created"
grep -q "GITLOG revert" "$GIT_ARGS_LOG" && fail "scenario A: reverted on a passing smoke"
grep -q "GITLOG push" "$GIT_ARGS_LOG" && fail "scenario A: pushed on a passing smoke"
grep -q "GHLOG issue create" "$GH_ARGS_LOG" && fail "scenario A: incident filed on a passing smoke"

# --- B: smoke fails after finish → revert, push, incident; issue stays open ---
seed b; repo_b="$repo"
export LEAD_TEST_POSTMERGE_SMOKE="exit 1"
reset_logs
if finish_in "$repo_b" >/dev/null 2>&1; then fail "scenario B: finish exited 0 on a failing smoke"; fi
grep -q "GITLOG revert --no-edit $SHA" "$GIT_ARGS_LOG" || fail "scenario B: revert of the merge commit not invoked: $(cat "$GIT_ARGS_LOG")"
grep -q "GITLOG push origin HEAD:refs/heads/revert/pr-7-" "$GIT_ARGS_LOG" || fail "scenario B: revert branch not pushed: $(cat "$GIT_ARGS_LOG")"
grep -q "GHLOG pr create --head revert/pr-7-.* --base main" "$GH_ARGS_LOG" || fail "scenario B: revert PR not created: $(cat "$GH_ARGS_LOG")"
grep -q "GHLOG pr merge 201 --auto --squash --delete-branch" "$GH_ARGS_LOG" || fail "scenario B: auto-merge not enabled: $(cat "$GH_ARGS_LOG")"
grep -q "GHLOG issue create --title \[P0\] postmerge smoke failed after PR #7" "$GH_ARGS_LOG" \
  || fail "scenario B: incident issue not created: $(cat "$GH_ARGS_LOG")"
grep -q "GHLOG issue create .*--label incident" "$GH_ARGS_LOG" || fail "scenario B: incident lacks the P0 label"
grep -q "GHLOG issue close 36" "$GH_ARGS_LOG" && fail "scenario B: closed the issue although the merge was reverted"
grep -q "GHLOG issue edit 36 --remove-label ready" "$GH_ARGS_LOG" || fail "scenario B: ready label not removed from the reverted issue"
grep -q "GHLOG issue edit 36 --add-label blocked" "$GH_ARGS_LOG" || fail "scenario B: blocked label not added to the reverted issue"
grep -q "GHLOG issue comment 36 --body postmerge:" "$GH_ARGS_LOG" || fail "scenario B: no revert note on the issue"
grep -q "GHLOG api repos/o/r --jq .default_branch" "$GH_ARGS_LOG" || fail "scenario B: default branch not resolved"
python3 - "$LEAD_STATE_FILE" <<'PY' || fail "scenario B: ledger/state do not record the revert"
import json, sys, os
state = json.load(open(sys.argv[1]))
w = state["workflows"][0]
assert w["status"] == "blocked", w
assert w["postmerge_incident"] == 900, w
ledger = os.path.join(os.path.dirname(sys.argv[1]), "runs.jsonl")
recs = [json.loads(l) for l in open(ledger) if l.strip()]
assert any(r["outcome"] == "postmerge-failed" and r.get("revert_of") for r in recs), recs
PY

# --- C: manual `lead postmerge <n>` gives the same verdict ---
seed c; repo_c="$repo"
export LEAD_TEST_POSTMERGE_SMOKE="true"
finish_in "$repo_c" --no-close >/dev/null || fail "scenario C: setup finish failed"
export LEAD_TEST_POSTMERGE_SMOKE="exit 1"
reset_logs
if postmerge_in "$repo_c" >/dev/null 2>&1; then fail "scenario C: postmerge exited 0 on a failing smoke"; fi
grep -q "GITLOG revert --no-edit $SHA" "$GIT_ARGS_LOG" || fail "scenario C: manual postmerge did not revert"
grep -q "GITLOG push origin HEAD:refs/heads/revert/pr-7-" "$GIT_ARGS_LOG" || fail "scenario C: manual postmerge did not push the revert"
grep -q "GHLOG issue create .*--label incident" "$GH_ARGS_LOG" || fail "scenario C: manual postmerge did not file an incident"
# The success verdict is the same check with a passing smoke: no revert,
# and the blocked issue returns to the queue (issue #275 F7).
reset_logs
export LEAD_TEST_POSTMERGE_SMOKE="true"
postmerge_in "$repo_c" >/dev/null || fail "scenario C: manual postmerge failed on a passing smoke"
grep -q "GITLOG revert" "$GIT_ARGS_LOG" && fail "scenario C: reverted on a passing manual smoke"
grep -q "GHLOG issue create" "$GH_ARGS_LOG" && fail "scenario C: incident filed on a passing manual smoke"
grep -q "GHLOG issue edit 36 --remove-label blocked" "$GH_ARGS_LOG" || fail "scenario C: blocked label not removed after recovery"
grep -q "GHLOG issue edit 36 --add-label ready" "$GH_ARGS_LOG" || fail "scenario C: ready label not restored after recovery"
python3 - "$LEAD_STATE_FILE" <<'PY' || fail "scenario C: state still marks the postmerge incident"
import json, sys
w = json.load(open(sys.argv[1]))["workflows"][0]
assert not w.get("postmerge_incident"), w
PY

# --- D: revert failure → incident says manual recovery is required ---
seed d; repo_d="$repo"
export LEAD_TEST_POSTMERGE_SMOKE="exit 1"
export GIT_REVERT_FAIL=1
reset_logs
if finish_in "$repo_d" >/dev/null 2>&1; then fail "scenario D: finish exited 0 although the revert failed"; fi
grep -q "GITLOG push" "$GIT_ARGS_LOG" && fail "scenario D: pushed although the revert failed"
grep -q "GHLOG pr create" "$GH_ARGS_LOG" && fail "scenario D: opened a revert PR although the revert failed"
grep -q "手動復旧が必要" "$GH_ARGS_LOG" || fail "scenario D: incident does not demand manual recovery: $(cat "$GH_ARGS_LOG")"
unset GIT_REVERT_FAIL

# --- E: auto-merge refusal → incident asks a human to merge the revert PR ---
seed e; repo_e="$repo"
export LEAD_TEST_POSTMERGE_SMOKE="exit 1"
export AUTO_MERGE_FAIL=1
reset_logs
if finish_in "$repo_e" >/dev/null 2>&1; then fail "scenario E: finish exited 0 although auto-merge was refused"; fi
grep -q "GHLOG pr create --head revert/pr-7-" "$GH_ARGS_LOG" || fail "scenario E: revert PR not created"
grep -q "手動でマージ" "$GH_ARGS_LOG" || fail "scenario E: incident does not ask for a manual merge: $(cat "$GH_ARGS_LOG")"
unset AUTO_MERGE_FAIL

[ "$HOME" = "$tmp/home" ] || fail "HOME isolation broken"

echo "postmerge behavioral checks passed"
