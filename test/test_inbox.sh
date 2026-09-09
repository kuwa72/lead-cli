#!/usr/bin/env bash
# issue #91: 受信箱 TUI（既定サブコマンド）の振る舞いテスト。
# 一時HOME・一時リポジトリ・ダミー gh/EDITOR の下で
#   非TTY の bare `lead` は非ゼロ終了し --help を案内する
#   `lead --help` / `--version` は従来どおり
#   LEAD_TEST_INBOX_KEYS で各キーが gh に渡す argv（ラベル付け外し・コメント・close・本文）
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
unset XDG_STATE_HOME HERDR_ENV LEAD_TEST_INBOX_KEYS || true
export LEAD_STATE_FILE="$tmp/state/workflows.json"
export PROTECTED=1
mkdir -p "$HOME"

repo="$tmp/repo"
git init -q -b main "$repo" || fail "git init failed"
git -C "$repo" config user.email "t@t"
git -C "$repo" config user.name "t"
git -C "$repo" remote add origin git@github.com:acme/widgets.git
echo "# rules" > "$repo/AGENTS.md"
git -C "$repo" add . && git -C "$repo" commit -qm init

export GH_LOG="$tmp/gh.log" GH_STDIN="$tmp/gh-stdin.txt" EDITOR_LOG="$tmp/editor.log"
: > "$GH_LOG"; : > "$EDITOR_LOG"

mkdir -p "$tmp/bin"
cat > "$tmp/bin/gh" <<'EOF'
#!/bin/sh
echo "GH $*" >> "$GH_LOG"
case "$1 $2" in
  "issue list")
    case "$*" in
      *"--label needs-review"*) printf '[{"number":7,"title":"spec: warn on zero stock"},{"number":8,"title":"spec: second"}]' ;;
      *"--label blocked"*) printf '[{"number":9,"title":"impl: stuck on flaky test"}]' ;;
      *"--state closed"*) printf '[{"number":42,"title":"feat: merged","closedAt":"2026-09-09T17:00:00Z"}]' ;;
      *) printf '[]' ;;
    esac ;;
  "issue view")
    case "$*" in
      *"--web"*) : ;;
      *) printf '{"number":%s,"title":"spec: warn on zero stock","body":"## Acceptance\\n- warns","state":"OPEN"}' "$3" ;;
    esac ;;
  "issue edit")
    case "$*" in *"--body-file -"*) cat > "$GH_STDIN" ;; esac ;;
  "issue comment"|"issue close") : ;;
  "issue create") echo "https://github.com/acme/widgets/issues/101" ;;
  "api repos/acme/widgets")
    case "$*" in
      *default_branch*) printf 'main\n' ;;
      *allow_auto_merge*) printf 'true\n' ;;
      *) echo "unexpected gh api jq: $*" >&2; exit 3 ;;
    esac ;;
  "api repos/acme/widgets/branches/main/protection")
    if [ -n "${PROTECTED:-}" ]; then
      printf '{"required_status_checks":{"contexts":["test"]},"required_pull_request_reviews":{}}'
    else
      echo "gh: Branch not protected (HTTP 404)" >&2; exit 1
    fi ;;
  "api repos/acme/widgets/rules/branches/main") printf '[]' ;;
  *) echo "unexpected gh call: $*" >&2; exit 3 ;;
esac
EOF
# Dummy spec agent (default: agy): record argv, print one draft on stdout.
cat > "$tmp/bin/agy" <<'EOF'
#!/bin/sh
echo "AGY $*" >> "$AGY_LOG"
printf '[{"title":"fix(stock): warn on zero stock","body":"## Acceptance\\n- warns"}]\n'
EOF
# Dummy $EDITOR: log argv, append one line to the file it was given.
cat > "$tmp/bin/fake-editor" <<'EOF'
#!/bin/sh
for a in "$@"; do printf '<%s>\n' "$a" >> "$EDITOR_LOG"; done
printf -- '- edited by human\n' >> "$1"
EOF
chmod +x "$tmp/bin/gh" "$tmp/bin/fake-editor" "$tmp/bin/agy"

# Dummy herdr (tab/pane API) and less ($PAGER fallback) for the p key.
export HERDR_LOG="$tmp/herdr.log" PAGER_LOG="$tmp/pager.log"
: > "$HERDR_LOG"; : > "$PAGER_LOG"
cat > "$tmp/bin/herdr" <<'EOF'
#!/bin/sh
for a in "$@"; do printf '<%s>\n' "$a" >> "$HERDR_LOG"; done
case "$1 $2" in
  "tab create") printf '{"result":{"root_pane":{"pane_id":"p-new"}}}' ;;
  "pane run") : ;;
  "pane move") : ;;
  *) echo "unexpected herdr: $*" >&2; exit 3 ;;
esac
EOF
cat > "$tmp/bin/less" <<'EOF'
#!/bin/sh
for a in "$@"; do printf '<%s>\n' "$a" >> "$PAGER_LOG"; done
EOF
chmod +x "$tmp/bin/herdr" "$tmp/bin/less"

export AGY_LOG="$tmp/agy.log"; : > "$AGY_LOG"
export PATH="$tmp/bin:$PATH"
export EDITOR=fake-editor

# Provide a workflow record so the blocked issue has an agent log to peek.
mkdir -p "$(dirname "$LEAD_STATE_FILE")"
cat > "$LEAD_STATE_FILE" <<'EOF'
{"version":1,"workflows":[{"issue":9,"status":"blocked","agent":"claude","attempts":3,"log_path":"/logs/issue-9.log","branch":"issue/9-stuck"}]}
EOF

CGO_ENABLED=0 go build -o "$tmp/lead" ./cmd/lead || fail "go build failed"
cd "$repo"

# --- 1. non-TTY bare lead: non-zero + --help hint; help/version still work -----
set +e
err_out="$("$tmp/lead" </dev/null 2>&1 >/dev/null)"
rc=$?
set -e
[ "$rc" -ne 0 ] || fail "bare lead on non-TTY exited 0"
case "$err_out" in *"--help"*) ;; *) fail "non-TTY error lacks --help hint: $err_out";; esac
[ ! -s "$GH_LOG" ] || fail "non-TTY bare lead must not call gh: $(cat "$GH_LOG")"

help_out="$("$tmp/lead" --help)" || fail "lead --help exited non-zero"
case "$help_out" in *"dispatch"*"run"*|*"run"*"dispatch"*) ;; *) fail "lead --help lacks command list";; esac
"$tmp/lead" -h >/dev/null || fail "lead -h exited non-zero"
ver_out="$("$tmp/lead" --version)" || fail "lead --version exited non-zero"
case "$ver_out" in *lead*) ;; *) fail "lead --version output unexpected: $ver_out";; esac

# --- 2. a: approve first needs-review issue (7) ------------------------------------
: > "$GH_LOG"
out="$(LEAD_TEST_INBOX_KEYS=a,q "$tmp/lead" 2>&1)" || fail "headless a,q exited non-zero: $out"
grep -qxF 'GH issue list --state open --label needs-review --limit 50 --json number,title' "$GH_LOG" \
  || fail "needs-review listing argv wrong: $(cat "$GH_LOG")"
grep -qxF 'GH issue list --state open --label blocked --limit 50 --json number,title' "$GH_LOG" \
  || fail "blocked listing argv wrong"
grep -qxF 'GH issue edit 7 --remove-label needs-review' "$GH_LOG" || fail "needs-review not removed: $(cat "$GH_LOG")"
grep -qxF 'GH issue edit 7 --add-label ready' "$GH_LOG" || fail "ready not added"
grep -q 'issue close' "$GH_LOG" && fail "approve must not close"
case "$out" in *"acme/widgets"*"#7"*) ;; *) fail "screen lacks repo header / issue row: $out";; esac
case "$out" in *"止まってる (1)"*"#9"*) ;; *) fail "blocked section missing: $out";; esac

# --- 3. j,a: approve second issue (8) --------------------------------------------
: > "$GH_LOG"
LEAD_TEST_INBOX_KEYS=j,a,q "$tmp/lead" >/dev/null 2>&1 || fail "headless j,a,q failed"
grep -qxF 'GH issue edit 8 --add-label ready' "$GH_LOG" || fail "second issue not approved: $(cat "$GH_LOG")"
grep -q 'edit 7 ' "$GH_LOG" && fail "first issue touched by j,a"

# --- 4. a on blocked issue (9): no label change ----------------------------------
: > "$GH_LOG"
LEAD_TEST_INBOX_KEYS=j,j,a,q "$tmp/lead" >/dev/null 2>&1 || fail "headless j,j,a,q failed"
grep -q 'issue edit' "$GH_LOG" && fail "blocked issue must not be approved: $(cat "$GH_LOG")"

# --- 5. x with reason: comment then close -----------------------------------------
: > "$GH_LOG"
LEAD_TEST_INBOX_KEYS='x,text:duplicate of #3,enter,q' "$tmp/lead" >/dev/null 2>&1 || fail "headless x flow failed"
grep -qxF 'GH issue comment 7 --body duplicate of #3' "$GH_LOG" || fail "reject comment wrong: $(cat "$GH_LOG")"
grep -qxF 'GH issue close 7' "$GH_LOG" || fail "reject did not close"
c_line="$(grep -n 'issue comment 7' "$GH_LOG" | cut -d: -f1)"
x_line="$(grep -n 'issue close 7' "$GH_LOG" | cut -d: -f1)"
[ "$c_line" -lt "$x_line" ] || fail "comment must precede close"

# --- 6. t on blocked issue: comment only ------------------------------------------
: > "$GH_LOG"
LEAD_TEST_INBOX_KEYS='j,j,t,text:retry with -race,enter,q' "$tmp/lead" >/dev/null 2>&1 || fail "headless t flow failed"
grep -qxF 'GH issue comment 9 --body retry with -race' "$GH_LOG" || fail "t comment wrong: $(cat "$GH_LOG")"
grep -q 'issue close' "$GH_LOG" && fail "t must not close"
grep -q 'issue edit' "$GH_LOG" && fail "t must not relabel"

# --- 7. e: $EDITOR edits body, then body update + approve --------------------------
: > "$GH_LOG"; : > "$EDITOR_LOG"; rm -f "$GH_STDIN"
LEAD_TEST_INBOX_KEYS=e,q "$tmp/lead" >/dev/null 2>&1 || fail "headless e flow failed"
grep -q '^<' "$EDITOR_LOG" || fail "editor was not invoked"
grep -qxF 'GH issue view 7 --json number,title,body,state' "$GH_LOG" || fail "e did not fetch body: $(cat "$GH_LOG")"
grep -qxF 'GH issue edit 7 --body-file -' "$GH_LOG" || fail "body not updated via stdin: $(cat "$GH_LOG")"
[ -f "$GH_STDIN" ] || fail "gh did not receive body on stdin"
printf '## Acceptance\n- warns- edited by human\n' | cmp -s - "$GH_STDIN" \
  || fail "edited body mismatch: $(cat "$GH_STDIN")"
grep -qxF 'GH issue edit 7 --remove-label needs-review' "$GH_LOG" || fail "e did not remove needs-review"
grep -qxF 'GH issue edit 7 --add-label ready' "$GH_LOG" || fail "e did not add ready"

# --- 8. r: AGENTS.md opens in $EDITOR, gh untouched ------------------------------
: > "$GH_LOG"; : > "$EDITOR_LOG"
LEAD_TEST_INBOX_KEYS=r,q "$tmp/lead" >/dev/null 2>&1 || fail "headless r failed"
grep -qxF "<$repo/AGENTS.md>" "$EDITOR_LOG" || fail "editor did not open repo AGENTS.md: $(cat "$EDITOR_LOG")"
grep -q 'issue edit\|issue comment\|issue close' "$GH_LOG" && fail "r must not touch gh"

# --- 9. o: browser; enter: detail fetch --------------------------------------------
: > "$GH_LOG"
LEAD_TEST_INBOX_KEYS=o,enter,esc,q "$tmp/lead" >/dev/null 2>&1 || fail "headless o/enter failed"
grep -qxF 'GH issue view 7 --web' "$GH_LOG" || fail "o did not open browser: $(cat "$GH_LOG")"
grep -qxF 'GH issue view 7 --json number,title,body,state' "$GH_LOG" || fail "enter did not fetch detail"

# --- 10. s: 一言から needs-review Issue 起票（spec AI 経由） -------------------------
: > "$GH_LOG"; : > "$AGY_LOG"
LEAD_TEST_INBOX_KEYS='s,text:add alert on zero stock,enter,q' "$tmp/lead" >/dev/null 2>&1 || fail "headless s flow failed"
grep -q 'add alert on zero stock' "$AGY_LOG" || fail "one-liner not passed to spec agent: $(cat "$AGY_LOG")"
# --body contains a newline, so the argv record spans two log lines.
tr '\n' ' ' < "$GH_LOG" | grep -q 'issue create --title fix(stock): warn on zero stock --body ## Acceptance - warns --label needs-review' \
  || fail "s did not create a needs-review issue from the draft: $(cat "$GH_LOG")"

# --- 11. n: 実機NG → 元 Issue 参照つき追い Issue --------------------------------------
: > "$GH_LOG"; : > "$AGY_LOG"
LEAD_TEST_INBOX_KEYS='n,text:still broken on main,enter,q' "$tmp/lead" >/dev/null 2>&1 || fail "headless n flow failed"
grep -qxF 'GH issue view 7 --json number,title,body,state' "$GH_LOG" \
  || fail "n did not fetch the parent issue: $(cat "$GH_LOG")"
grep -q 'issue create' "$GH_LOG" || fail "n did not create a follow-up issue"
tr '\n' ' ' < "$GH_LOG" | grep -q 'issue create.*#7' \
  || fail "follow-up body lacks the #7 reference: $(cat "$GH_LOG")"

# --- 12. p with herdr: new tab + tail -f on the agent log ----------------------------
: > "$HERDR_LOG"; unset HERDR_ENV
HERDR_ENV=1 LEAD_TEST_INBOX_KEYS='j,j,p,q' "$tmp/lead" >/dev/null 2>&1 || fail "headless p herdr failed"
grep -qxF '<tab>' "$HERDR_LOG" || fail "p did not call herdr tab create: $(cat "$HERDR_LOG")"
grep -qxF '<create>' "$HERDR_LOG" || fail "p herdr tab create args wrong: $(cat "$HERDR_LOG")"
grep -qxF '<--focus>' "$HERDR_LOG" || fail "p herdr tab create missing --focus: $(cat "$HERDR_LOG")"
grep -qxF '<pane>' "$HERDR_LOG" || fail "p did not call herdr pane run: $(cat "$HERDR_LOG")"
grep -qxF '<tail>' "$HERDR_LOG" && grep -qxF '<-f>' "$HERDR_LOG" && grep -qxF '</logs/issue-9.log>' "$HERDR_LOG" || fail "p herdr pane run tail -f log wrong: $(cat "$HERDR_LOG")"

# --- 13. p without herdr: $PAGER (less) fallback -----------------------------------
: > "$PAGER_LOG"; unset HERDR_ENV
LEAD_TEST_INBOX_KEYS='j,j,p,q' PAGER= "$tmp/lead" >/dev/null 2>&1 || fail "headless p pager failed"
grep -qxF '</logs/issue-9.log>' "$PAGER_LOG" || fail "p did not open the log via $PAGER/less: $(cat "$PAGER_LOG")"

# --- 14. unknown key token is an error ------------------------------------------------
LEAD_TEST_INBOX_KEYS=bogus-key "$tmp/lead" >/dev/null 2>&1 && fail "unknown key token exited 0"

# --- 15. merged section: recently closed issues show, c confirms and hides them ----
seen_dir="$(dirname "$LEAD_STATE_FILE")"
mkdir -p "$seen_dir"
cat > "$seen_dir/inbox-seen.json" <<'EOF'
{"last_seen_at":"2026-09-09T00:00:00Z","confirmed":[]}
EOF

out="$(LEAD_TEST_INBOX_KEYS='z,q' "$tmp/lead" 2>&1)" || fail "headless merged show failed"
case "$out" in *"最近マージ (1)"*"#42"*"feat: merged"*) ;; *) fail "merged issue #42 not shown: $out";; esac
grep -q 'issue list --state closed' "$GH_LOG" || fail "ListMergedSince not called: $(cat "$GH_LOG")"

# Reset the open timestamp and confirm the merged issue.
cat > "$seen_dir/inbox-seen.json" <<'EOF'
{"last_seen_at":"2026-09-09T00:00:00Z","confirmed":[]}
EOF
out="$(LEAD_TEST_INBOX_KEYS='z,j,j,j,c,q' "$tmp/lead" 2>&1)" || fail "headless c on merged failed"
case "$out" in *"最近マージ (0)"*) ;; *) fail "merged issue still shown after c: $out";; esac
case "$out" in *"#42 を確認しました"*) ;; *) fail "c status missing: $out";; esac
grep -q '42' "$seen_dir/inbox-seen.json" || fail "inbox-seen not updated: $(cat "$seen_dir/inbox-seen.json")"

echo "inbox tests passed"
