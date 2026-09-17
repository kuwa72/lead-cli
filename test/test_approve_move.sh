#!/usr/bin/env bash
# issue #184: lead内でapproveしたissueは他の箱 (Backlog) に移動すること。
# 状態遷移を記録するダミーghの下で `a` 後の最終画面を検証する
# (gh argvログ + 画面出力の振る舞いアサート。ソースgrepなし)。
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
unset XDG_STATE_HOME HERDR_ENV LEAD_TEST_INBOX_KEYS || true
export LEAD_STATE_FILE="$tmp/state/workflows.json"
mkdir -p "$HOME"

repo="$tmp/repo"
git init -q -b main "$repo" || fail "git init failed"
git -C "$repo" config user.email "t@t"
git -C "$repo" config user.name "t"
git -C "$repo" remote add origin git@github.com:acme/widgets.git
echo "# rules" > "$repo/AGENTS.md"
git -C "$repo" add . && git -C "$repo" commit -qm init

export GH_LOG="$tmp/gh.log"
: > "$GH_LOG"

mkdir -p "$tmp/bin"
cat > "$tmp/bin/gh" <<'EOF'
#!/bin/sh
echo "GH $*" >> "$GH_LOG"
case "$1 $2" in
  "issue list")
    case "$*" in
      *"--label needs-review"*)
        if [ -f "$APPROVED_FLAG" ]; then
          printf '[{"number":8,"title":"spec: second"}]'
        else
          printf '[{"number":7,"title":"spec: warn on zero stock"},{"number":8,"title":"spec: second"}]'
        fi ;;
      *"--label blocked"*) printf '[{"number":9,"title":"impl: stuck on flaky test"}]' ;;
      *"--label ready"*)
        if [ -f "$APPROVED_FLAG" ]; then
          printf '[{"number":7,"title":"spec: warn on zero stock"}]'
        else
          printf '[]'
        fi ;;
      *"--state closed"*) printf '[]' ;;
      *) printf '[{"number":7,"title":"spec: warn on zero stock"},{"number":8,"title":"spec: second"},{"number":9,"title":"impl: stuck on flaky test"}]' ;;
    esac ;;
  "issue view")
    case "$*" in
      *"--json comments"*) printf '{"comments":[]}' ;;
      *) printf '{"number":%s,"title":"spec: warn on zero stock","body":"## Acceptance\n- warns","state":"OPEN"}' "$3" ;;
    esac ;;
  "issue edit")
    case "$*" in
      *"7 --add-label ready"*) : > "$APPROVED_FLAG" ;;
    esac ;;
  *) echo "unexpected gh call: $*" >&2; exit 3 ;;
esac
EOF
chmod +x "$tmp/bin/gh"
export APPROVED_FLAG="$tmp/approved"
export PATH="$tmp/bin:$PATH"

mkdir -p "$(dirname "$LEAD_STATE_FILE")"
cat > "$LEAD_STATE_FILE" <<'EOF'
{"version":1,"workflows":[]}
EOF
cat > "$(dirname "$LEAD_STATE_FILE")/inbox-seen.json" <<'EOF'
{"help_shown":true}
EOF

CGO_ENABLED=0 go build -o "$tmp/lead" ./cmd/lead || fail "go build failed"
cd "$repo"

# approve #7, then quit. The dummy gh drops #7 from needs-review on
# subsequent listings; since issue #212 the approved issue shows under the
# Ready section (it carried the `ready` label all along — it used to fall
# through to Backlog because no Ready section existed).
: > "$GH_LOG"
out="$(LEAD_TEST_INBOX_KEYS=a,q "$tmp/lead" 2>&1)" || fail "headless a,q exited non-zero: $out"

grep -qxF 'GH issue edit 7 --remove-label needs-review' "$GH_LOG" || fail "needs-review not removed: $(cat "$GH_LOG")"
grep -qxF 'GH issue edit 7 --add-label ready' "$GH_LOG" || fail "ready not added: $(cat "$GH_LOG")"
case "$out" in *"Approved #7 (ready)"*) ;; *) fail "approve status missing: $out";; esac

# Needs review now holds only #8.
case "$out" in *"Needs review (1)"*) ;; *) fail "needs-review count wrong after approve: $out";; esac
# Ready now holds the approved #7.
case "$out" in *"Ready (1)"*) ;; *) fail "ready count wrong after approve: $out";; esac
case "$out" in *"Backlog (0)"*) ;; *) fail "#7 must not fall through to Backlog: $out";; esac
# The #7 row (title) must render after the Ready header, i.e. inside
# that box. Compare last occurrences: the dump contains the initial
# screen too, so only the final screen counts.
pos_ready="$(printf '%s\n' "$out" | awk '/Ready \(1\)/{p=NR} END{print p+0}')"
pos_row="$(printf '%s\n' "$out" | awk '/warn on zero stock/{p=NR} END{print p+0}')"
[ "$pos_row" -gt "$pos_ready" ] || fail "#7 did not move under Ready (row=$pos_row ready=$pos_ready): $out"

echo "approve-move tests passed"
