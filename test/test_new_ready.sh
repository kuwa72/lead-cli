#!/usr/bin/env bash
# issue #254: `lead new` / `lead ready` で Issue 受け入れを一本化する。
# 一時HOME・一時リポジトリ・ダミー gh・ダミー $EDITOR の下で
#   - `lead new` が雛形を $EDITOR で開き、必須項目を満たす本文なら
#     needs-review + risk:b で作成する
#   - 必須項目が欠けた本文は作成せず exit 1 で不足項目を表示する
#   - `lead new --ready` は needs-review を付けず ready + risk:* を付ける
#   - 非 TTY かつ --body-file なしはエラー（$EDITOR を起動しない）
#   - `lead new --body-file` は非対話で動作し、見出しからタイトルを補完する
#   - `lead ready <n>` は不正な本文を拒否してラベルを変えない
#   - `lead ready <n>` は正当な本文なら ready を付けて needs-review を外す
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
echo x > "$repo/f.txt"
git -C "$repo" add . && git -C "$repo" commit -qm init

export GH_LOG="$tmp/gh.log" EDITOR_LOG="$tmp/editor.log" COUNTER="$tmp/counter"
: > "$GH_LOG"; : > "$EDITOR_LOG"; echo 100 > "$COUNTER"

mkdir -p "$tmp/bin"
cat > "$tmp/bin/gh" <<'EOF'
#!/bin/sh
printf 'GH %s\n' "$(printf '%s' "$*" | tr '\n' '~')" >> "$GH_LOG"
case "$1 $2" in
  "issue create")
    n=$(cat "$COUNTER"); echo $((n+1)) > "$COUNTER"
    echo "https://github.com/o/r/issues/$n" ;;
  "issue view")
    case "$3" in
      42) cat <<'JSON'
{"number":42,"title":"valid","body":"# valid\n\n## 受入条件\n\n- [ ] `./test/run-tests.sh` が pass する\n\n## スコープ外\n\n- none\n\n## シナリオ\n\n1. `lead ready 42` → ready が付く","state":"OPEN","labels":[{"name":"needs-review"},{"name":"risk:b"}]}
JSON
        ;;
      43) cat <<'JSON'
{"number":43,"title":"broken","body":"# broken\n\n## 背景\n\nno required sections","state":"OPEN","labels":[{"name":"needs-review"},{"name":"risk:b"}]}
JSON
        ;;
      44) cat <<'JSON'
{"number":44,"title":"no risk","body":"# no risk\n\n## 受入条件\n\n- [ ] `./test/run-tests.sh` が pass する\n\n## スコープ外\n\n- none\n\n## シナリオ\n\n1. `lead ready 44` → ready が付く","state":"OPEN","labels":[{"name":"needs-review"}]}
JSON
        ;;
      45) cat <<'JSON'
{"number":45,"title":"english","body":"# english\n\n## Acceptance criteria\n\n- [ ] `./test/run-tests.sh` passes\n\n## Scope\n\n**変更対象**\n- preview cache\n\n**対象外**\n- list cache\n\n## Scenarios\n\n1. `lead ready 45` → ready is added","state":"OPEN","labels":[{"name":"needs-review"}]}
JSON
        ;;
      46) cat <<'JSON'
{"number":46,"title":"no scenario","body":"# no scenario\n\n## 受入条件\n\n- [ ] `./test/run-tests.sh` が pass する\n\n## スコープ外\n\n- none","state":"OPEN","labels":[{"name":"needs-review"},{"name":"risk:b"}]}
JSON
        ;;
      *) echo "unexpected issue view: $*" >&2; exit 3 ;;
    esac ;;
  "issue edit") ;;
  *) echo "unexpected gh call: $*" >&2; exit 3 ;;
esac
EOF
# Dummy $EDITOR: prove the template has the required sections, then replace
# the file with $BODY_SRC.
cat > "$tmp/bin/editor" <<'EOF'
#!/bin/sh
printf 'EDITOR %s\n' "$*" >> "$EDITOR_LOG"
target="$1"
if grep -q '## 受入条件' "$target" && grep -q '## スコープ外' "$target" && grep -q '## シナリオ' "$target"; then
  echo "template-ok" >> "$EDITOR_LOG"
fi
cat "$BODY_SRC" > "$target"
EOF
chmod +x "$tmp/bin/gh" "$tmp/bin/editor"
export PATH="$tmp/bin:$PATH"

cat > "$tmp/valid.md" <<'EOF'
# feat(cli): add lead new

## 受入条件

- [ ] `./test/run-tests.sh` が pass する

## スコープ外

- none

## シナリオ

1. `lead new --body-file valid.md` → Issue が作られる
EOF
cat > "$tmp/no_scenario.md" <<'EOF'
# broken

## 受入条件

- [ ] `./test/run-tests.sh` が pass する

## スコープ外

- none
EOF
cat > "$tmp/invalid.md" <<'EOF'
# broken

## 背景

no required sections
EOF

CGO_ENABLED=0 go build -o "$tmp/lead" ./cmd/lead || fail "go build failed"
cd "$repo"

# --- 1. lead new opens the template and files needs-review + risk:b ----------
: > "$GH_LOG"; : > "$EDITOR_LOG"
out="$(LEAD_TEST_TTY=1 BODY_SRC="$tmp/valid.md" EDITOR="$tmp/bin/editor" "$tmp/lead" new 2>&1)" \
  || fail "lead new exited non-zero: $out"
grep -qxF 'template-ok' "$EDITOR_LOG" || fail "editor did not open the required template: $(cat "$EDITOR_LOG")"
grep -q '^GH issue create --title feat(cli): add lead new --body .* --label risk:b --label needs-review$' "$GH_LOG" \
  || fail "create with needs-review + risk:b missing: $(cat "$GH_LOG")"
case "$out" in *"created #100 https://github.com/o/r/issues/100"*) ;; *) fail "output lacks created line: $out";; esac

# --- 2. missing sections: no create, exit 1, missing items listed -------------
: > "$GH_LOG"; : > "$EDITOR_LOG"
set +e
out="$(LEAD_TEST_TTY=1 BODY_SRC="$tmp/invalid.md" EDITOR="$tmp/bin/editor" "$tmp/lead" new 2>&1)"
status=$?
set -e
[ "$status" -eq 1 ] || fail "invalid body exit = $status, want 1: $out"
case "$out" in *"受入条件"*) ;; *) fail "missing 受入条件 not reported: $out";; esac
case "$out" in *"スコープ外"*) ;; *) fail "missing スコープ外 not reported: $out";; esac
grep -q '^GH issue create' "$GH_LOG" && fail "invalid body must not create an issue: $(cat "$GH_LOG")"

# --- 3. missing scenario: no create, exit 1, シナリオ reported ----------------
: > "$GH_LOG"
set +e
out="$("$tmp/lead" new --body-file "$tmp/no_scenario.md" 2>&1)"
status=$?
set -e
[ "$status" -eq 1 ] || fail "no-scenario exit = $status, want 1: $out"
case "$out" in *"シナリオ"*) ;; *) fail "missing シナリオ not reported: $out";; esac
grep -q '^GH issue create' "$GH_LOG" && fail "no-scenario body must not create an issue: $(cat "$GH_LOG")"

# --- 4. non-TTY without --body-file is an error before $EDITOR ----------------
: > "$GH_LOG"; : > "$EDITOR_LOG"
set +e
out="$(EDITOR="$tmp/bin/editor" "$tmp/lead" new </dev/null 2>&1)"
status=$?
set -e
[ "$status" -eq 1 ] || fail "non-TTY exit = $status, want 1: $out"
case "$out" in *"interactive terminal"*|*"--body-file"*) ;; *) fail "non-TTY error lacks a hint: $out";; esac
[ -s "$EDITOR_LOG" ] && fail "non-TTY run must not start \$EDITOR: $(cat "$EDITOR_LOG")"
grep -q '^GH issue create' "$GH_LOG" && fail "non-TTY run must not create an issue"

# --- 4. --ready: ready instead of needs-review, risk:a from --risk -----------
: > "$GH_LOG"; : > "$EDITOR_LOG"
out="$(LEAD_TEST_TTY=1 BODY_SRC="$tmp/valid.md" EDITOR="$tmp/bin/editor" "$tmp/lead" new --ready --risk a 2>&1)" \
  || fail "lead new --ready exited non-zero: $out"
grep -q '^GH issue create --title feat(cli): add lead new --body .* --label risk:a --label ready$' "$GH_LOG" \
  || fail "ready create with risk:a missing: $(cat "$GH_LOG")"
grep -q 'needs-review' "$GH_LOG" && fail "--ready must not add needs-review: $(cat "$GH_LOG")"

# --- 5. --body-file is non-interactive and derives the title from the heading -
: > "$GH_LOG"; : > "$EDITOR_LOG"
out="$("$tmp/lead" new --body-file "$tmp/valid.md" 2>&1)" || fail "lead new --body-file exited non-zero: $out"
grep -q '^GH issue create --title feat(cli): add lead new --body .* --label risk:b --label needs-review$' "$GH_LOG" \
  || fail "body-file create missing: $(cat "$GH_LOG")"
[ -s "$EDITOR_LOG" ] && fail "--body-file must not start \$EDITOR: $(cat "$EDITOR_LOG")"

# --- 6. lead ready <n>: valid body adds risk + ready, removes needs-review ---
: > "$GH_LOG"
out="$("$tmp/lead" ready 42 2>&1)" || fail "lead ready 42 exited non-zero: $out"
grep -qxF 'GH issue edit 42 --add-label risk:b' "$GH_LOG" || fail "risk:b not added: $(cat "$GH_LOG")"
grep -qxF 'GH issue edit 42 --add-label ready' "$GH_LOG" || fail "ready not added: $(cat "$GH_LOG")"
grep -qxF 'GH issue edit 42 --remove-label needs-review' "$GH_LOG" || fail "needs-review not removed: $(cat "$GH_LOG")"

# --- 7. lead ready <n>: invalid body changes no labels ------------------------
: > "$GH_LOG"
set +e
out="$("$tmp/lead" ready 43 2>&1)"
status=$?
set -e
[ "$status" -eq 1 ] || fail "invalid ready exit = $status, want 1: $out"
case "$out" in *"受入条件"*) ;; *) fail "invalid ready does not list 受入条件: $out";; esac
grep -q '^GH issue edit' "$GH_LOG" && fail "invalid ready must not change labels: $(cat "$GH_LOG")"

# --- 8. lead ready <n>: a missing risk label is rejected ----------------------
: > "$GH_LOG"
set +e
out="$("$tmp/lead" ready 44 2>&1)"
status=$?
set -e
[ "$status" -eq 1 ] || fail "no-risk ready exit = $status, want 1: $out"
case "$out" in *"risk:a"*"risk:b"*) ;; *) fail "no-risk ready does not list risk labels: $out";; esac
grep -q '^GH issue edit' "$GH_LOG" && fail "no-risk ready must not change labels: $(cat "$GH_LOG")"

# --- 9. help documents the required sections and validation rules ------------
out="$("$tmp/lead" new --help 2>&1)" || fail "lead new --help failed"
for needle in "## 受入条件" "## スコープ外" "## シナリオ" "risk:a" "risk:b" "バッククォート" "--body-file" "--ready" "--risk"; do
  case "$out" in *"$needle"*) ;; *) fail "lead new --help lacks $needle: $out";; esac
done
out="$("$tmp/lead" ready --help 2>&1)" || fail "lead ready --help failed"
case "$out" in *"lead ready"*) ;; *) fail "lead ready --help lacks command name: $out";; esac
case "$out" in *"--risk"*) ;; *) fail "lead ready --help lacks --risk: $out";; esac
case "$out" in *"Acceptance criteria"*) ;; *) fail "lead ready --help lacks the English heading: $out";; esac

# --- 10. English headings + **対象外**: --risk applies the classification ----
: > "$GH_LOG"
out="$("$tmp/lead" ready 45 --risk b 2>&1)" || fail "lead ready 45 --risk b exited non-zero: $out"
grep -qxF 'GH issue edit 45 --add-label risk:b' "$GH_LOG" || fail "risk:b not added for english body: $(cat "$GH_LOG")"
grep -qxF 'GH issue edit 45 --add-label ready' "$GH_LOG" || fail "ready not added for english body: $(cat "$GH_LOG")"
grep -qxF 'GH issue edit 45 --remove-label needs-review' "$GH_LOG" || fail "needs-review not removed for english body: $(cat "$GH_LOG")"

# --- 11. English body with no risk label and no --risk is still rejected -----
: > "$GH_LOG"
set +e
out="$("$tmp/lead" ready 45 2>&1)"
status=$?
set -e
[ "$status" -eq 1 ] || fail "no-risk english ready exit = $status, want 1: $out"
grep -q '^GH issue edit' "$GH_LOG" && fail "no-risk english ready must not change labels: $(cat "$GH_LOG")"

# --- 12. lead ready <n>: a missing scenario is rejected ----------------------
: > "$GH_LOG"
set +e
out="$("$tmp/lead" ready 46 2>&1)"
status=$?
set -e
[ "$status" -eq 1 ] || fail "no-scenario ready exit = $status, want 1: $out"
case "$out" in *"シナリオ"*) ;; *) fail "no-scenario ready does not list シナリオ: $out";; esac
grep -q '^GH issue edit' "$GH_LOG" && fail "no-scenario ready must not change labels: $(cat "$GH_LOG")"

echo "new/ready tests passed"
