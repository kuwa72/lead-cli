#!/usr/bin/env bash
# issue #94: `lead say` (仕様 AI) の振る舞いテスト。
# 一時HOME・一時リポジトリ・ダミー gh・ダミーエージェント（claude）の下で
#   - 一言 → エージェントをヘッドレス起動（-p --dangerously-skip-permissions）
#   - 返った JSON 配列 → gh issue create --title ... --label needs-review
#   - 複数件は本文で相互参照（gh issue edit）
#   - --follow-up N: 元 Issue を取得し本文に #N を含む
#   - --redraft N: gh issue edit + コメント、create しない
#   - --dry-run: gh の変更系を一切呼ばない
#   - エージェントログが状態ディレクトリ配下に残る
# をアサートする（grep 検査なし・実HOME/実リポジトリに触れない）。
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
unset XDG_STATE_HOME HERDR_ENV LEAD_SPEC_AGENT || true
export LEAD_STATE_FILE="$tmp/state/workflows.json"
mkdir -p "$HOME"

repo="$tmp/repo"
git init -q -b main "$repo" || fail "git init failed"
git -C "$repo" config user.email "t@t"
git -C "$repo" config user.name "t"
printf '# rules\nNO GREP TESTS IN THIS REPO\n' > "$repo/AGENTS.md"
git -C "$repo" add . && git -C "$repo" commit -qm init

export GH_LOG="$tmp/gh.log" AGENT_LOG="$tmp/agent.log" AGENT_OUT="$tmp/agent.out" COUNTER="$tmp/counter"
: > "$GH_LOG"; : > "$AGENT_LOG"; echo 200 > "$COUNTER"

mkdir -p "$tmp/bin"
# Dummy gh: one line per call ("GH <args...>", newlines in args shown as ~), canned answers.
cat > "$tmp/bin/gh" <<'EOF'
#!/bin/sh
printf 'GH %s\n' "$(printf '%s' "$*" | tr '\n' '~')" >> "$GH_LOG"
case "$1 $2" in
  "issue create")
    n=$(cat "$COUNTER"); echo $((n+1)) > "$COUNTER"
    echo "https://github.com/o/r/issues/$n" ;;
  "issue view")
    if [ "$5" = "comments" ]; then
      printf '{"comments":[{"author":{"login":"kuwa72"},"body":"exit code also","createdAt":"2026-09-08T00:00:00Z"}]}'
    else
      printf '{"number":%s,"title":"parent issue","body":"parent body","state":"OPEN"}' "$3"
    fi ;;
  "issue edit")
    # capture the --body-file content (last arg) when present
    for a in "$@"; do last="$a"; done
    case "$*" in *"--body-file"*) cp "$last" "$GH_LOG.body-$3" ;; esac ;;
  "issue comment") ;;
  *) echo "unexpected: $*" >&2; exit 3 ;;
esac
EOF
# Dummy agent: log argv one per line, cwd, then print canned output.
cat > "$tmp/bin/claude" <<'EOF'
#!/bin/sh
printf '<%s>\n' "$@" >> "$AGENT_LOG"
printf 'CWD=%s\n' "$(pwd)" >> "$AGENT_LOG"
cat "$AGENT_OUT"
EOF
chmod +x "$tmp/bin/gh" "$tmp/bin/claude"
export PATH="$tmp/bin:$PATH"

CGO_ENABLED=0 go build -o "$tmp/lead" ./cmd/lead || fail "go build failed"

# --- 1. one-liner → two issues, cross-referenced ---------------------------------
cat > "$AGENT_OUT" <<'EOF'
Sure, here are the issues:
```json
[{"title":"feat(cli): add lead say","body":"## Mode\nimplement\n\n## Purpose\nsay"},
 {"title":"docs: describe lead say","body":"## Mode\ndocs"}]
```
EOF
out="$(cd "$repo" && "$tmp/lead" say "一言で Issue を起票したい" --agent claude 2>&1)" || fail "lead say exited non-zero: $out"

grep -qxF '<-p>' "$AGENT_LOG" || fail "agent missing -p: $(cat "$AGENT_LOG")"
grep -qxF '<--dangerously-skip-permissions>' "$AGENT_LOG" || fail "agent missing skip-permissions flag"
grep -q '一言で Issue を起票したい' "$AGENT_LOG" || fail "prompt lacks the one-liner"
grep -q 'NO GREP TESTS IN THIS REPO' "$AGENT_LOG" || fail "prompt lacks AGENTS.md content"
grep -qxF "CWD=$repo" "$AGENT_LOG" || fail "agent cwd was not the repo: $(cat "$AGENT_LOG")"

grep -q '^GH issue create --title feat(cli): add lead say --body ' "$GH_LOG" || fail "first issue create missing: $(cat "$GH_LOG")"
grep -q '^GH issue create --title docs: describe lead say --body ' "$GH_LOG" || fail "second issue create missing"
[ "$(grep -c '^GH issue create .* --label needs-review$' "$GH_LOG")" -eq 2 ] || fail "needs-review label not on both creates: $(cat "$GH_LOG")"
grep -q '^GH issue edit 200 --title feat(cli): add lead say --body-file ' "$GH_LOG" || fail "cross-reference edit for #200 missing"
grep -q '^GH issue edit 201 --title docs: describe lead say --body-file ' "$GH_LOG" || fail "cross-reference edit for #201 missing"
grep -q '#201' "$GH_LOG.body-200" || fail "#200 body does not reference #201: $(cat "$GH_LOG.body-200")"
grep -q '#200' "$GH_LOG.body-201" || fail "#201 body does not reference #200"
grep -q '^## Mode' "$GH_LOG.body-200" || fail "original body lost in cross-reference edit"
case "$out" in *"created #200 https://github.com/o/r/issues/200"*"created #201"*) ;; *) fail "output lacks created lines: $out";; esac
ls "$tmp/state/logs"/say-*.log >/dev/null 2>&1 || fail "agent log not written under state dir"
grep -q 'feat(cli): add lead say' "$tmp/state/logs"/say-*.log || fail "agent log lacks agent output"

# --- 2. --dry-run: agent runs, gh mutates nothing --------------------------------
: > "$GH_LOG"; : > "$AGENT_LOG"
out="$(cd "$repo" && "$tmp/lead" say "dry" --agent claude --dry-run 2>&1)" || fail "dry-run exited non-zero: $out"
[ -s "$GH_LOG" ] && fail "dry-run called gh: $(cat "$GH_LOG")"
grep -qxF '<-p>' "$AGENT_LOG" || fail "dry-run did not run the agent"
case "$out" in *"[dry-run] would create issue 1/2"*"labels: needs-review"*"docs: describe lead say"*) ;; *) fail "dry-run output unexpected: $out";; esac

# --- 3. --follow-up N: fetches N, body references #N -----------------------------
: > "$GH_LOG"; : > "$AGENT_LOG"
echo '[{"title":"fix(dispatch): handle NG","body":"## Purpose\nno explicit ref"}]' > "$AGENT_OUT"
(cd "$repo" && "$tmp/lead" say "実機で NG" --agent claude --follow-up 42 >/dev/null 2>&1) || fail "follow-up exited non-zero"
grep -q '^GH issue view 42 --json number,title,body,state$' "$GH_LOG" || fail "parent issue not fetched: $(cat "$GH_LOG")"
grep -q 'parent body' "$AGENT_LOG" || fail "prompt lacks parent issue body"
grep -q '^GH issue create --title fix(dispatch): handle NG --body .*#42.* --label needs-review$' "$GH_LOG" || fail "follow-up body lacks #42: $(cat "$GH_LOG")"
grep -q '^GH issue edit' "$GH_LOG" && fail "single follow-up issue should not be edited"

# --- 4. --redraft N: edit + label + comment, no create ---------------------------
: > "$GH_LOG"; : > "$AGENT_LOG"
echo '{"title":"parent issue (revised)","body":"parent body\n- exit code asserted"}' > "$AGENT_OUT"
out="$(cd "$repo" && "$tmp/lead" say "exit code を受入条件に" --agent claude --redraft 42 2>&1)" || fail "redraft exited non-zero: $out"
grep -q '^GH issue view 42 --json comments$' "$GH_LOG" || fail "comments not fetched: $(cat "$GH_LOG")"
grep -q 'exit code also' "$AGENT_LOG" || fail "prompt lacks existing comments"
grep -q '^GH issue edit 42 --title parent issue (revised) --body-file ' "$GH_LOG" || fail "redraft edit missing: $(cat "$GH_LOG")"
grep -q 'exit code asserted' "$GH_LOG.body-42" || fail "redraft body not written"
grep -q '^GH issue edit 42 --add-label needs-review$' "$GH_LOG" || fail "needs-review label not kept"
grep -q '^GH issue comment 42 --body .*exit code を受入条件に' "$GH_LOG" || fail "change summary comment missing"
grep -q '^GH issue create' "$GH_LOG" && fail "redraft must not create issues"
case "$out" in *"redrafted #42"*) ;; *) fail "output lacks redrafted line: $out";; esac

# --- 5. bad agent output → non-zero, nothing created -----------------------------
: > "$GH_LOG"
echo 'I refuse.' > "$AGENT_OUT"
if (cd "$repo" && "$tmp/lead" say "x" --agent claude >/dev/null 2>&1); then fail "invalid agent output accepted"; fi
[ -s "$GH_LOG" ] && fail "gh called despite invalid agent output"

# --- 6. help ---------------------------------------------------------------------
"$tmp/lead" say --help | grep -q -- '--follow-up' || fail "say --help lacks --follow-up"
"$tmp/lead" --help | grep -q 'say' || fail "root help lacks say"

echo "PASS: test_say"
