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
  *) echo "unexpected gh call: $*" >&2; exit 3 ;;
esac
EOF
# Dummy $EDITOR: log argv, append one line to the file it was given.
cat > "$tmp/bin/fake-editor" <<'EOF'
#!/bin/sh
for a in "$@"; do printf '<%s>\n' "$a" >> "$EDITOR_LOG"; done
printf -- '- edited by human\n' >> "$1"
EOF
chmod +x "$tmp/bin/gh" "$tmp/bin/fake-editor"
export PATH="$tmp/bin:$PATH"
export EDITOR=fake-editor

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

# --- 10. unknown key token is an error ------------------------------------------------
LEAD_TEST_INBOX_KEYS=bogus-key "$tmp/lead" >/dev/null 2>&1 && fail "unknown key token exited 0"

echo "inbox tests passed"
