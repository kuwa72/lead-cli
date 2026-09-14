#!/usr/bin/env bash
# issue #196: inbox一覧画面に常時表示のメッセージ・ログ枠。
# `R` 再読込で "Loaded: ..." がイベントログに記録されることを利用し、
# 最終画面にログ枠 (ヘッダ + 枠内のログ行) があることを検証する
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
      *"--label needs-review"*) printf '[{"number":7,"title":"spec: warn on zero stock"},{"number":8,"title":"spec: second"}]' ;;
      *"--label blocked"*) printf '[{"number":9,"title":"impl: stuck on flaky test"}]' ;;
      *"--state closed"*) printf '[]' ;;
      *) printf '[{"number":7,"title":"spec: warn on zero stock"},{"number":8,"title":"spec: second"},{"number":9,"title":"impl: stuck on flaky test"}]' ;;
    esac ;;
  "issue view")
    case "$*" in
      *"--json comments"*) printf '{"comments":[]}' ;;
      *) printf '{"number":%s,"title":"spec: warn on zero stock","body":"## Acceptance\n- warns","state":"OPEN"}' "$3" ;;
    esac ;;
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

# Reload appends a "Loaded: ..." entry to the event log; the always-on
# log pane must show the pane header and that entry in the final screen.
: > "$GH_LOG"
out="$(LEAD_TEST_INBOX_KEYS=R,q "$tmp/lead" 2>&1)" || fail "headless R,q exited non-zero: $out"

# List itself must keep working alongside the pane.
case "$out" in *"#7"*) ;; *) fail "issue #7 missing from list: $out";; esac

# Pane header present.
case "$out" in *"Log"*) ;; *) fail "log pane header missing: $out";; esac

# The latest log entry ("Loaded: ...") must render after the pane header,
# i.e. inside the pane. Compare last occurrences: the dump contains the
# initial screen too, so only the final screen counts.
pos_pane="$(printf '%s\n' "$out" | awk '/Log/{p=NR} END{print p+0}')"
pos_loaded="$(printf '%s\n' "$out" | awk '/Loaded:/{p=NR} END{print p+0}')"
[ "$pos_loaded" -gt "$pos_pane" ] || fail "latest log entry not inside pane (loaded=$pos_loaded pane=$pos_pane): $out"

echo "log-pane tests passed"
