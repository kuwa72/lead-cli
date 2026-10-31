#!/usr/bin/env bash
# issue #302: `lead grill <n>` は未確定の Issue 本文を $EDITOR で開き、
# 保存後に `lead ready` と同じ検証・シナリオ凍結・ready 化を行う。
# 一時 HOME・一時リポジトリ・ダミー gh・ダミー $EDITOR の下で
#   - 編集後の本文を gh issue edit で保存する
#   - 必須項目を満たせば risk:* + ready を付け needs-review を外し、
#     シナリオを worktree 外に凍結する
#   - 未確定のままなら exit 1 で不足項目を表示し、ラベルを変えず凍結もしない
#   - 非 TTY では $EDITOR を起動しない
#   - リスク分類が無い Issue は --risk（既定 b）で補う
#   - help が $EDITOR と --risk を説明する
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
unset XDG_STATE_HOME LEAD_TEST_TTY || true
export LEAD_STATE_FILE="$tmp/state/workflows.json"
mkdir -p "$HOME"

repo="$tmp/repo"
git init -q -b main "$repo" || fail "git init failed"
git -C "$repo" config user.email "t@t"
git -C "$repo" config user.name "t"
git -C "$repo" remote add origin git@github.com:o/r.git
echo x > "$repo/f.txt"
git -C "$repo" add . && git -C "$repo" commit -qm init

export GH_LOG="$tmp/gh.log" EDITOR_LOG="$tmp/editor.log"
: > "$GH_LOG"; : > "$EDITOR_LOG"

mkdir -p "$tmp/bin"
# Dummy gh: issue view serves $ISSUE_JSON; issue edit records the body file.
cat > "$tmp/bin/gh" <<'EOF'
#!/bin/sh
printf 'GH %s\n' "$(printf '%s' "$*" | tr '\n' '~')" >> "$GH_LOG"
case "$1 $2" in
  "issue view")
    if [ "$5" = "comments" ]; then echo '{"comments":[]}'; else cat "$ISSUE_JSON"; fi ;;
  "issue edit")
    for a in "$@"; do last="$a"; done
    case "$*" in *"--body-file"*) cp "$last" "$GH_LOG.body-$3" ;; esac ;;
  *) echo "unexpected: $*" >&2; exit 3 ;;
esac
EOF
# Dummy $EDITOR: log the opened path, then replace the file with $BODY_SRC.
cat > "$tmp/bin/editor" <<'EOF'
#!/bin/sh
printf 'EDITOR %s\n' "$*" >> "$EDITOR_LOG"
cat "$BODY_SRC" > "$1"
EOF
chmod +x "$tmp/bin/gh" "$tmp/bin/editor"
export PATH="$tmp/bin:$PATH"

cat > "$tmp/issue42.json" <<'EOF'
{"number":42,"title":"unsettled","body":"# unsettled\n\n## 背景\n\nno required sections","state":"OPEN","labels":[{"name":"needs-review"},{"name":"risk:b"}]}
EOF
cat > "$tmp/issue43.json" <<'EOF'
{"number":43,"title":"still unsettled","body":"# still unsettled\n\n## 背景\n\nno required sections","state":"OPEN","labels":[{"name":"needs-review"},{"name":"risk:b"}]}
EOF
cat > "$tmp/issue44.json" <<'EOF'
{"number":44,"title":"no risk","body":"# no risk\n\n## 受入条件\n\n- [ ] `./test/run-tests.sh` が pass する\n\n## スコープ外\n\n- none\n\n## シナリオ\n\n1. `lead grill 44` → ready が付く","state":"OPEN","labels":[{"name":"needs-review"}]}
EOF
cat > "$tmp/valid.md" <<'EOF'
# unsettled

## 受入条件

- [ ] `./test/run-tests.sh` が pass する

## スコープ外

- none

## シナリオ

1. `lead grill 42` → ready が付く
EOF
cat > "$tmp/invalid.md" <<'EOF'
# still unsettled

## 背景

no required sections
EOF
# Same text as issue44's body: the editor changes nothing.
cat > "$tmp/issue44.md" <<'EOF'
# no risk

## 受入条件

- [ ] `./test/run-tests.sh` が pass する

## スコープ外

- none

## シナリオ

1. `lead grill 44` → ready が付く
EOF

CGO_ENABLED=0 go build -o "$tmp/lead" ./cmd/lead || fail "go build failed"

# --- 1. grill: saves the edited body, then readies + freezes -----------------
: > "$GH_LOG"; : > "$EDITOR_LOG"
out="$(cd "$repo" && ISSUE_JSON="$tmp/issue42.json" LEAD_TEST_TTY=1 BODY_SRC="$tmp/valid.md" \
  EDITOR="$tmp/bin/editor" "$tmp/lead" grill 42 2>&1)" || fail "lead grill exited non-zero: $out"
grep -q '^GH issue view 42 ' "$GH_LOG" || fail "grill did not read the issue: $(cat "$GH_LOG")"
[ -s "$EDITOR_LOG" ] || fail "grill did not open \$EDITOR"
grep -q '^GH issue edit 42 --title unsettled --body-file ' "$GH_LOG" \
  || fail "grill did not save the edited body: $(cat "$GH_LOG")"
grep -q '## シナリオ' "$GH_LOG.body-42" || fail "saved body lacks the edited シナリオ: $(cat "$GH_LOG.body-42")"
grep -qxF 'GH issue edit 42 --add-label risk:b' "$GH_LOG" || fail "risk:b not added: $(cat "$GH_LOG")"
grep -qxF 'GH issue edit 42 --add-label ready' "$GH_LOG" || fail "ready not added: $(cat "$GH_LOG")"
grep -qxF 'GH issue edit 42 --remove-label needs-review' "$GH_LOG" || fail "needs-review not removed: $(cat "$GH_LOG")"
[ -f "$tmp/state/scenarios/o_r/42.md" ] || fail "grill did not freeze the scenario"
grep -q '^## Steps$' "$tmp/state/scenarios/o_r/42.md" || fail "frozen scenario lacks ## Steps"
case "$out" in *"ready #42"*) ;; *) fail "output lacks ready line: $out";; esac

# --- 2. still unsettled: exit 1, no labels, no freeze -----------------------
: > "$GH_LOG"
set +e
out="$(cd "$repo" && ISSUE_JSON="$tmp/issue43.json" LEAD_TEST_TTY=1 BODY_SRC="$tmp/invalid.md" \
  EDITOR="$tmp/bin/editor" "$tmp/lead" grill 43 2>&1)"
status=$?
set -e
[ "$status" -eq 1 ] || fail "unsettled grill exit = $status, want 1: $out"
case "$out" in *"シナリオ"*) ;; *) fail "unsettled grill does not list シナリオ: $out";; esac
grep -q -- '--add-label ready' "$GH_LOG" && fail "unsettled grill must not add ready: $(cat "$GH_LOG")"
grep -q -- '--remove-label needs-review' "$GH_LOG" && fail "unsettled grill must not remove needs-review: $(cat "$GH_LOG")"
[ ! -e "$tmp/state/scenarios/o_r/43.md" ] || fail "unsettled grill must not freeze a scenario"

# --- 3. non-TTY: fail before starting $EDITOR -------------------------------
: > "$GH_LOG"; : > "$EDITOR_LOG"
set +e
out="$(cd "$repo" && ISSUE_JSON="$tmp/issue42.json" EDITOR="$tmp/bin/editor" "$tmp/lead" grill 42 </dev/null 2>&1)"
status=$?
set -e
[ "$status" -eq 1 ] || fail "non-TTY grill exit = $status, want 1: $out"
case "$out" in *"interactive terminal"*) ;; *) fail "non-TTY error lacks a hint: $out";; esac
[ -s "$EDITOR_LOG" ] && fail "non-TTY grill must not start \$EDITOR: $(cat "$EDITOR_LOG")"
grep -q '^GH issue edit' "$GH_LOG" && fail "non-TTY grill must not edit the issue: $(cat "$GH_LOG")"

# --- 4. no risk label: --risk (default b) classifies the issue --------------
: > "$GH_LOG"
out="$(cd "$repo" && ISSUE_JSON="$tmp/issue44.json" LEAD_TEST_TTY=1 BODY_SRC="$tmp/issue44.md" \
  EDITOR="$tmp/bin/editor" "$tmp/lead" grill 44 2>&1)" || fail "no-risk grill exited non-zero: $out"
grep -qxF 'GH issue edit 44 --add-label risk:b' "$GH_LOG" || fail "default --risk b not applied: $(cat "$GH_LOG")"
grep -qxF 'GH issue edit 44 --add-label ready' "$GH_LOG" || fail "no-risk grill did not add ready: $(cat "$GH_LOG")"
[ -f "$tmp/state/scenarios/o_r/44.md" ] || fail "no-risk grill did not freeze the scenario"

# --- 5. help documents grill, $EDITOR, and --risk ---------------------------
grill_help="$("$tmp/lead" grill --help)"
for needle in "grill" 'EDITOR' "--risk" "lead ready"; do
  case "$grill_help" in *"$needle"*) ;; *) fail "grill --help lacks $needle: $grill_help";; esac
done

echo "PASS: test_grill"
