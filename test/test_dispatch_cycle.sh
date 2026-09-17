#!/usr/bin/env bash
# issue #213: blocked-by が推移的に循環する ready 群 (#1→#2→#3→#1) で
#   pass が正常終了し、循環の各構成 Issue に循環パスを含むコメントが1回投稿される
#   2回目の pass では同一循環に重複コメントしない（投稿コメントを dummy gh が永続化）
#   循環 Issue は dispatch されず deferred のまま
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

export GH_LOG="$tmp/gh.log" AGENT_LOG="$tmp/agent.log" CLOSED_DIR="$tmp/closed" COMMENT_DIR="$tmp/comments"
mkdir -p "$CLOSED_DIR" "$COMMENT_DIR"
: > "$GH_LOG"; : > "$AGENT_LOG"

# ready queue: #1,#2,#3 form the transitive cycle 1→2→3→1; #9 is free.
# `issue comment` bodies persist under $COMMENT_DIR/<n>/ so `issue view
# --json comments` can serve them back on later passes.
mkdir -p "$tmp/bin"
cat > "$tmp/bin/gh" <<'EOF'
#!/bin/sh
echo "GH $*" >> "$GH_LOG"
n="${3:-}"
case "$1 $2" in
  "issue list")
    case "$*" in
      *"--label ready"*) printf '[{"number":1,"title":"a","blockedBy":{"nodes":[{"number":2,"state":"OPEN"}],"totalCount":1}},{"number":2,"title":"b","blockedBy":{"nodes":[{"number":3,"state":"OPEN"}],"totalCount":1}},{"number":3,"title":"c","blockedBy":{"nodes":[{"number":1,"state":"OPEN"}],"totalCount":1}},{"number":9,"title":"free"}]' ;;
      *) printf '[]' ;;
    esac ;;
  "issue view")
    case "$*" in
      *"--json comments"*)
        printf '{"comments":['
        first=1
        for f in "$COMMENT_DIR/$n"/*; do
          [ -e "$f" ] || continue
          [ "$first" -eq 0 ] && printf ','
          first=0
          esc="$(sed ':a;N;$!ba;s/\n/\\n/g' "$f")"
          printf '{"author":{"login":"lead"},"body":"%s","createdAt":"t"}' "$esc"
        done
        printf ']}' ;;
      *)
        if [ -f "$CLOSED_DIR/$n" ]; then st=CLOSED; else st=OPEN; fi
        printf '{"number":%s,"title":"issue %s","body":"do the thing","state":"%s"}' "$n" "$n" "$st" ;;
    esac ;;
  "issue comment")
    body=""
    while [ $# -gt 0 ]; do
      if [ "$1" = "--body" ]; then shift; body="${1:-}"; fi
      shift || break
    done
    mkdir -p "$COMMENT_DIR/$n"
    i="$(find "$COMMENT_DIR/$n" -type f | wc -l)"
    printf '%s' "$body" > "$COMMENT_DIR/$n/$i.txt" ;;
  "issue edit"|"auth status") : ;;
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
n="$(basename "$(pwd)")"; n="${n#issue-}"
printf 'LAUNCH %s\n' "$n" >> "$AGENT_LOG"
touch "$CLOSED_DIR/$n"
EOF
chmod +x "$tmp/bin/gh" "$tmp/bin/claude"
export PATH="$tmp/bin:$PATH"

CGO_ENABLED=0 go build -o "$tmp/lead" ./cmd/lead || fail "go build failed"

out="$(cd "$repo" && "$tmp/lead" dispatch --once --parallel 1 --agent claude 2>&1)" || fail "dispatch --once exited non-zero: $out"

# The pass completes normally: #9 dispatched, the cycle members deferred.
grep -qx 'LAUNCH 9' "$AGENT_LOG" || fail "#9 was not dispatched: $(cat "$AGENT_LOG")"
for n in 1 2 3; do
  grep -qx "LAUNCH $n" "$AGENT_LOG" && fail "cycle member #$n was dispatched"
  case "$out" in *"#$n deferred"*) ;; *) fail "output lacks deferred report for #$n: $out";; esac
done

# Each cycle member got exactly one comment containing the cycle path.
[ "$(grep -c '^GH issue comment ' "$GH_LOG")" -eq 3 ] || fail "comment calls != 3: $(cat "$GH_LOG")"
for n in 1 2 3; do
  [ "$(find "$COMMENT_DIR/$n" -type f | wc -l)" -eq 1 ] || fail "comments on #$n != 1: $(ls "$COMMENT_DIR/$n" 2>/dev/null)"
  grep -qF '#1 → #2 → #3 → #1' "$COMMENT_DIR/$n/0.txt" || fail "comment on #$n lacks the cycle path: $(cat "$COMMENT_DIR/$n"/*)"
done

# Second pass: comments are already on the issues, so nothing is reposted.
: > "$GH_LOG"; : > "$AGENT_LOG"
out="$(cd "$repo" && "$tmp/lead" dispatch --once --parallel 1 --agent claude 2>&1)" || fail "second dispatch exited non-zero: $out"
[ "$(grep -c '^GH issue comment ' "$GH_LOG")" -eq 0 ] || fail "cycle re-reported on second pass: $(cat "$GH_LOG")"
for n in 1 2 3; do
  case "$out" in *"#$n deferred"*) ;; *) fail "pass 2 lacks deferred report for #$n: $out";; esac
done

echo "dispatch cycle tests passed"
