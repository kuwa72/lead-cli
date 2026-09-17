#!/usr/bin/env bash
# issue #212: inbox に Ready 区画を新設し、dispatch 順と deferred 理由を表示する。
# 一時HOME・一時リポジトリ・ダミーgh の下で
#   Ready 区画が dispatch と同じ順序規則（親 Issue の sub-issues 順）で並ぶ
#   open な blocked-by を持つ行に "deferred: blocked by #N" が出る
#   SubIssues 呼び出しがリロード毎に増えない（セッション内キャッシュ）
# をアサートする（grep検査なし・実HOME/実リポジトリに触れない）。
#
# リポジトリに origin を張らないことで dispatch ループを無効化し、
# `issue view 50 --json state,subIssues` の呼び出し回数を inbox 側だけに限定する。
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
export LEAD_NOTIFY=0
mkdir -p "$HOME"

repo="$tmp/repo"
git init -q -b main "$repo" || fail "git init failed"
git -C "$repo" config user.email "t@t"
git -C "$repo" config user.name "t"
echo x > "$repo/f.txt"
git -C "$repo" add . && git -C "$repo" commit -qm init
# 意図的に origin を付けない: Preflight が失敗し、inbox 内の dispatch
# ループが起動しない（SubIssues 呼び出しを inbox の表示側だけに限定する）。

export GH_LOG="$tmp/gh.log"
: > "$GH_LOG"

mkdir -p "$tmp/bin"
cat > "$tmp/bin/gh" <<'EOF'
#!/bin/sh
echo "GH $*" >> "$GH_LOG"
case "$1 $2" in
  "issue list")
    case "$*" in
      *"--label needs-review"*) printf '[]' ;;
      *"--label blocked"*) printf '[]' ;;
      *"--label ready"*)
        # listed newest-first: #3,#1,#2 are sub-issues of tracking issue #50
        # (display order 1,2,3); #8 is gated by open #35.
        printf '[{"number":3,"title":"third","parent":{"number":50}},{"number":1,"title":"first","parent":{"number":50}},{"number":2,"title":"second","parent":{"number":50}},{"number":8,"title":"gated","blockedBy":{"nodes":[{"number":35,"state":"OPEN"}],"totalCount":1}}]' ;;
      *) printf '[]' ;;
    esac ;;
  "issue view")
    case "$*" in
      *subIssues*) printf '{"state":"OPEN","subIssues":{"nodes":[{"number":1},{"number":2},{"number":3}],"totalCount":3}}' ;;
      *) printf '{"number":%s,"title":"issue","body":"","state":"OPEN"}' "${3:-0}" ;;
    esac ;;
  "issue edit"|"issue comment"|"auth status") : ;;
  *) echo "unexpected gh call: $*" >&2; exit 3 ;;
esac
EOF
chmod +x "$tmp/bin/gh"
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

# 2 reloads (R,R) after the initial load: 3 loads total.
out="$(LEAD_TEST_INBOX_KEYS='R,R,q' "$tmp/lead" 2>&1)" || fail "headless inbox exited non-zero: $out"

# The ready listing must use the dispatch label query.
grep -qxF 'GH issue list --state open --label ready --limit 300 --json number,title,updatedAt,parent,blockedBy' "$GH_LOG" \
  || fail "ready issue listing missing: $(cat "$GH_LOG")"

# Ready section with 4 items on the final screen.
case "$out" in *"Ready (4)"*) ;; *) fail "Ready section missing or empty: $out";; esac

# Rows appear in dispatch order on the final screen: #1,#2,#3 (sub-issues
# order of parent #50), then deferred #8.
last_line() { printf '%s\n' "$out" | awk -v pat="$1" 'index($0, pat) {p=NR} END{print p+0}'; }
p_ready="$(last_line 'Ready (4)')"
p1="$(last_line '#1 first')"
p2="$(last_line '#2 second')"
p3="$(last_line '#3 third')"
p8="$(last_line '#8 gated')"
[ "$p1" -gt "$p_ready" ] && [ "$p1" -lt "$p2" ] && [ "$p2" -lt "$p3" ] && [ "$p3" -lt "$p8" ] \
  || fail "ready order wrong (ready=$p_ready 1=$p1 2=$p2 3=$p3 8=$p8): $out"

# The gated row carries the deferred reason with the open blocker number.
printf '%s\n' "$out" | awk 'index($0, "#8 gated") && index($0, "deferred: blocked by #35") {ok=1} END{exit ok?0:1}' \
  || fail "deferred reason missing on #8: $out"

# SubIssues for parent #50 is fetched once and cached across reloads.
n_sub="$(grep -c 'issue view 50 --json state,subIssues' "$GH_LOG" || true)"
[ "$n_sub" = "1" ] || fail "SubIssues called $n_sub times across 3 loads, want 1 (cached): $(cat "$GH_LOG")"

echo "inbox ready section tests passed"
