#!/usr/bin/env bash
# issue #245: リスク分級ゲートの振る舞いテスト。
# ダミー gh で Issue のラベルを差し替え、`lead finish --merge` が
#   - ラベルなし（既定 risk:b）: マージせず awaiting-human を付け exit 0
#   - risk:a: マージして close
#   - risk:b + --approve: マージし awaiting-human を外す
#   - risk:b: マージしない
# を検証する（grep によるソース検査なし・一時 HOME/リポジトリ）。
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
export LEAD_NOTIFY=0
mkdir -p "$HOME"

export GH_LOG="$tmp/gh.log"
export LABELS_FILE="$tmp/labels.txt"
export MERGED_MARK="$tmp/merged" CLOSED_MARK="$tmp/closed"
: > "$GH_LOG"; : > "$LABELS_FILE"

mkdir -p "$tmp/bin"
cat > "$tmp/bin/gh" <<'EOF'
#!/bin/sh
echo "GH $*" >> "$GH_LOG"
labels_json() {
  first=1
  printf '['
  while IFS= read -r l; do
    [ -n "$l" ] || continue
    [ "$first" = 1 ] || printf ','
    first=0
    printf '{"name":"%s"}' "$l"
  done < "$LABELS_FILE"
  printf ']'
}
case "$1 $2" in
  "issue view")
    case "$*" in
      *"--json comments"*) printf '{"comments":[]}' ;;
      *)
        if [ -f "$CLOSED_MARK" ]; then st=CLOSED; else st=OPEN; fi
        printf '{"number":36,"title":"demo","body":"b","state":"%s","labels":%s}' "$st" "$(labels_json)" ;;
    esac ;;
  "issue edit")
    case "$*" in
      *--add-label*) printf '%s\n' "$5" >> "$LABELS_FILE" ;;
      *--remove-label*)
        grep -vxF "$5" "$LABELS_FILE" > "$LABELS_FILE.tmp" || true
        mv "$LABELS_FILE.tmp" "$LABELS_FILE" ;;
      *) : ;;
    esac ;;
  "issue close") touch "$CLOSED_MARK" ;;
  "pr list") printf '7\n' ;;
  "pr view")
    case "$*" in
      *"--json labels"*) printf 'review:pass\n' ;;
      *"--json headRefOid"*) printf '{"headRefOid":"0123456789abcdef0123456789abcdef01234567"}' ;;
      *"--json comments"*) printf '{"comments":[{"author":{"login":"lead"},"body":"<!-- lead-review sha=0123456789abcdef0123456789abcdef01234567 verdict=pass -->","createdAt":"2026-10-25T00:00:00Z"}]}' ;;
      *"--json files"*) printf '%s\n' "${FILES:-internal/x.go}" ;;
      *) printf '{"number":7,"state":"OPEN","mergeable":"MERGEABLE","mergeStateStatus":"CLEAN"}' ;;
    esac ;;
  "pr checks") printf '[{"name":"test","bucket":"pass","state":"SUCCESS"}]' ;;
  "pr merge") touch "$MERGED_MARK" ;;
  "api repos/o/r")
    case "$*" in
      *default_branch*) printf 'main\n' ;;
      *allow_auto_merge*) printf 'true\n' ;;
      *) echo "unexpected gh api: $*" >&2; exit 3 ;;
    esac ;;
  "api user") printf 'octocat\n' ;;
  "auth status") : ;;
  *) echo "unexpected gh call: $*" >&2; exit 3 ;;
esac
EOF
chmod +x "$tmp/bin/gh"
export PATH="$tmp/bin:$PATH"

CGO_ENABLED=0 go build -o "$tmp/lead" ./cmd/lead || fail "go build failed"

mk_repo() {
  local repo="$1"
  git init -q -b main "$repo" || fail "git init failed"
  git -C "$repo" config user.email "t@t"
  git -C "$repo" config user.name "t"
  echo x > "$repo/f.txt"
  git -C "$repo" add . && git -C "$repo" commit -qm init
  git -C "$repo" remote add origin https://github.com/o/r.git
}

# seed <case> <label>... : fresh repo, state file and label set.
seed() {
  local name="$1"; shift
  local repo="$tmp/repo-$name"
  mk_repo "$repo"
  export LEAD_STATE_FILE="$tmp/state-$name.json"
  : > "$LABELS_FILE"
  for l in "$@"; do printf '%s\n' "$l" >> "$LABELS_FILE"; done
  rm -f "$MERGED_MARK" "$CLOSED_MARK"
  : > "$GH_LOG"
  (cd "$repo" && "$tmp/lead" run 36 >/dev/null) || fail "$name: lead run failed"
}

state_has() {
  python3 -c "import json,sys;d=json.load(open('$LEAD_STATE_FILE'));w=d['workflows'][0];sys.exit(0 if w.get('$1')=='$2' else 1)" \
    || fail "$3: state $1 != $2: $(cat "$LEAD_STATE_FILE")"
}

# --- 1. ラベルなし + --merge: マージせず awaiting-human、exit 0、open のまま ---
seed no-label
out="$(cd "$tmp/repo-no-label" && "$tmp/lead" finish 36 --pr 7 --merge --timeout 60s --poll-interval 1s)" \
  || fail "no-label: finish exited non-zero: $out"
case "$out" in *awaiting*--approve*) ;; *) fail "no-label: missing awaiting-human/--approve guidance: $out";; esac
[ -f "$MERGED_MARK" ] && fail "no-label: PR was merged without approval"
grep -q '^GH pr merge ' "$GH_LOG" && fail "no-label: gh pr merge was called: $(cat "$GH_LOG")"
[ -f "$CLOSED_MARK" ] && fail "no-label: issue was closed"
grep -qx 'GH issue edit 36 --add-label awaiting-human' "$GH_LOG" \
  || fail "no-label: awaiting-human was not added: $(cat "$GH_LOG")"
state_has status awaiting_review no-label

# --- 2. risk:a + --merge: マージして close ---
seed risk-a risk:a
out="$(cd "$tmp/repo-risk-a" && "$tmp/lead" finish 36 --pr 7 --merge --close --timeout 60s --poll-interval 1s)" \
  || fail "risk-a: finish failed: $out"
[ -f "$MERGED_MARK" ] || fail "risk-a: PR was not merged"
[ -f "$CLOSED_MARK" ] || fail "risk-a: issue was not closed"
grep -qx 'GH pr merge 7 --squash --delete-branch' "$GH_LOG" || fail "risk-a: squash merge not invoked: $(cat "$GH_LOG")"
grep -q -- '--add-label awaiting-human' "$GH_LOG" && fail "risk-a: awaiting-human added to a risk:a merge"
state_has status closed risk-a

# --- 3. risk:b + --merge --approve: マージし awaiting-human を外す ---
seed risk-b-approve risk:b awaiting-human
out="$(cd "$tmp/repo-risk-b-approve" && "$tmp/lead" finish 36 --pr 7 --merge --approve --close --timeout 60s --poll-interval 1s)" \
  || fail "risk-b approve: finish failed: $out"
[ -f "$MERGED_MARK" ] || fail "risk-b approve: PR was not merged"
[ -f "$CLOSED_MARK" ] || fail "risk-b approve: issue was not closed"
grep -qx 'GH issue edit 36 --remove-label awaiting-human' "$GH_LOG" \
  || fail "risk-b approve: awaiting-human was not removed: $(cat "$GH_LOG")"
if grep -qx 'awaiting-human' "$LABELS_FILE"; then fail "risk-b approve: awaiting-human label still present"; fi
state_has status closed risk-b-approve

# --- 4. risk:b + --merge: マージしない ---
seed risk-b risk:b
out="$(cd "$tmp/repo-risk-b" && "$tmp/lead" finish 36 --pr 7 --merge --timeout 60s --poll-interval 1s)" \
  || fail "risk-b: finish exited non-zero: $out"
[ -f "$MERGED_MARK" ] && fail "risk-b: PR was merged without approval"
grep -q '^GH pr merge ' "$GH_LOG" && fail "risk-b: gh pr merge was called: $(cat "$GH_LOG")"
grep -qx 'GH issue edit 36 --add-label awaiting-human' "$GH_LOG" \
  || fail "risk-b: awaiting-human was not added: $(cat "$GH_LOG")"
state_has status awaiting_review risk-b

[ "$HOME" = "$tmp/home" ] || fail "HOME isolation broken"

echo "risk gate behavioral checks passed"
