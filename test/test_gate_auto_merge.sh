#!/usr/bin/env bash
# issue #278: 承認を廃止し、CI・シナリオ・保護パスからマージ可否を計算する。
# ダミー gh / git で、次の振る舞いを検証する（grep 検査なし）:
#   - CI pass + シナリオ 1/1 + 保護パス非該当 → --approve なしでマージ
#   - review:needs-work でもシナリオ 2/2 ならマージ（review は助言）
#   - シナリオ 1/2 → マージせず exit 0、理由を stdout と Issue コメントに残す
#   - --approve は unknown flag で exit 1
set -euo pipefail

# This test exercises the merge gate, not the post-merge smoke (#275).
export LEAD_TEST_SKIP_POSTMERGE=1

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

SHA="0123456789abcdef0123456789abcdef01234567"
mkdir -p "$tmp/bin"
cat > "$tmp/bin/gh" <<'EOF'
#!/bin/sh
echo "GHLOG $*" >> "$GH_ARGS_LOG"
case "$1 $2" in
  "issue view")
    printf '{"number":36,"title":"demo","body":"b","state":"OPEN","labels":[]}' ;;
  "pr view")
    case "$*" in
      *"--json headRefOid"*) printf '{"headRefOid":"%s"}' "$MERGE_SHA" ;;
      *"--json comments"*) printf '{"comments":[{"author":{"login":"lead"},"body":"%s","createdAt":"2026-10-25T00:00:00Z"}]}' "$GH_MARKER" ;;
      *"--json files"*) printf '%s\n' "${GH_FILES:-src/main.go}" ;;
      *) printf '{"number":7,"state":"OPEN","mergeable":"MERGEABLE","mergeStateStatus":"CLEAN"}' ;;
    esac ;;
  "pr checks") printf '[{"bucket":"pass","name":"test","state":"SUCCESS"}]' ;;
  "pr merge") printf '{"number":7,"state":"MERGED"}' ;;
  "api user") printf 'lead\n' ;;
  "issue close" | "issue comment") : ;;
  *)
    if [ "$1" = "api" ]; then printf 'true'
    else echo "unexpected gh call: $*" >&2; exit 3
    fi ;;
esac
EOF
chmod +x "$tmp/bin/gh"
export PATH="$tmp/bin:$PATH"
export GH_ARGS_LOG="$tmp/gh-args.log" MERGE_SHA="$SHA"

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

# --- A: CI pass + scenario 1/1 + clean files → merge without --approve --------
repo="$tmp/repo-a"; mk_repo "$repo"
export LEAD_STATE_FILE="$tmp/state-a.json"
export GH_MARKER="<!-- lead-review sha=$SHA verdict=pass scenarios=1/1 -->"
unset GH_FILES || true
: > "$GH_ARGS_LOG"
( cd "$repo" && "$tmp/lead" run 36 >/dev/null ) || fail "A: lead run failed"
out="$(cd "$repo" && "$tmp/lead" finish 36 --pr 7 --merge --close --timeout 60s --poll-interval 1s)" || fail "A: finish failed: $out"
case "$out" in *merged*closed*) ;; *) fail "A: missing merge+close report: $out";; esac
grep -q "GHLOG pr merge 7 --squash --delete-branch --match-head-commit $SHA" "$GH_ARGS_LOG" || fail "A: merge not invoked: $(cat "$GH_ARGS_LOG")"

# --- B: review:needs-work + scenario 2/2 → still merges (review advisory) -----
repo="$tmp/repo-b"; mk_repo "$repo"
export LEAD_STATE_FILE="$tmp/state-b.json"
export GH_MARKER="<!-- lead-review sha=$SHA verdict=needs-work scenarios=2/2 -->"
: > "$GH_ARGS_LOG"
( cd "$repo" && "$tmp/lead" run 36 >/dev/null ) || fail "B: lead run failed"
out="$(cd "$repo" && "$tmp/lead" finish 36 --pr 7 --merge --close --timeout 60s --poll-interval 1s)" || fail "B: finish failed: $out"
case "$out" in *merged*) ;; *) fail "B: review:needs-work blocked a 2/2 merge: $out";; esac

# --- C: scenario 1/2 → no merge, exit 0, reason on stdout and the issue ------
repo="$tmp/repo-c"; mk_repo "$repo"
export LEAD_STATE_FILE="$tmp/state-c.json"
export GH_MARKER="<!-- lead-review sha=$SHA verdict=pass scenarios=1/2 -->"
: > "$GH_ARGS_LOG"
( cd "$repo" && "$tmp/lead" run 36 >/dev/null ) || fail "C: lead run failed"
out="$(cd "$repo" && "$tmp/lead" finish 36 --pr 7 --merge --timeout 60s --poll-interval 1s)" || fail "C: scenario pause was not exit 0: $out"
case "$out" in *"1/2"*) ;; *) fail "C: stdout lacks the scenario score: $out";; esac
grep -q "GHLOG pr merge" "$GH_ARGS_LOG" && fail "C: merged at 1/2"
grep -q "GHLOG issue comment 36 " "$GH_ARGS_LOG" || fail "C: pause reason not commented on the issue: $(cat "$GH_ARGS_LOG")"

# --- D: --approve is gone → unknown flag, exit 1 ------------------------------
set +e
( cd "$repo" && "$tmp/lead" finish 36 --pr 7 --merge --approve ) >"$tmp/d.out" 2>&1
status=$?
set -e
[ "$status" -ne 0 ] || fail "D: --approve was accepted"
grep -qi "unknown flag" "$tmp/d.out" || fail "D: --approve error is not an unknown flag: $(cat "$tmp/d.out")"

[ "$HOME" = "$tmp/home" ] || fail "HOME isolation broken"
echo "auto-merge gate behavioral checks passed"
