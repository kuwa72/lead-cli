#!/usr/bin/env bash
# issue #302: `lead say --settled` は検証済み Issue を ready で起票し、
# シナリオを worktree 外に凍結する。一時 HOME・一時リポジトリ・ダミー gh・
# ダミーエージェント（claude）の下で
#   - --settled: 本文を lead new/ready と同じ規則で検証し、risk:b + ready で
#     起票、<state-dir>/scenarios/<owner>_<repo>/<issue>.md を凍結する
#   - 既定（--settled なし）: 従来どおり needs-review で起票し、凍結しない
#   - 検証に落ちる本文は 1 件も起票しない（exit 1・不足項目を表示）
#   - --dry-run: gh を変更せず、凍結もしない（ラベルは risk:b,ready）
#   - help が --settled を説明する
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
unset XDG_STATE_HOME LEAD_SPEC_AGENT || true
export LEAD_STATE_FILE="$tmp/state/workflows.json"
mkdir -p "$HOME"

repo="$tmp/repo"
git init -q -b main "$repo" || fail "git init failed"
git -C "$repo" config user.email "t@t"
git -C "$repo" config user.name "t"
git -C "$repo" remote add origin git@github.com:o/r.git
printf '# rules\nTDD required\n' > "$repo/AGENTS.md"
git -C "$repo" add . && git -C "$repo" commit -qm init

export GH_LOG="$tmp/gh.log" AGENT_LOG="$tmp/agent.log" AGENT_OUT="$tmp/agent.out" COUNTER="$tmp/counter"
: > "$GH_LOG"; : > "$AGENT_LOG"; echo 200 > "$COUNTER"

mkdir -p "$tmp/bin"
# Dummy gh: one line per call, canned answers.
cat > "$tmp/bin/gh" <<'EOF'
#!/bin/sh
printf 'GH %s\n' "$(printf '%s' "$*" | tr '\n' '~')" >> "$GH_LOG"
case "$1 $2" in
  "issue create")
    n=$(cat "$COUNTER"); echo $((n+1)) > "$COUNTER"
    echo "https://github.com/o/r/issues/$n" ;;
  "issue list") echo '[]' ;;
  "issue view") printf '{"number":%s,"title":"x","body":"x","state":"OPEN","labels":[]}' "$3" ;;
  *) echo "unexpected: $*" >&2; exit 3 ;;
esac
EOF
# Dummy agent: log argv, then print the canned payload.
cat > "$tmp/bin/claude" <<'EOF'
#!/bin/sh
printf '<%s>\n' "$@" >> "$AGENT_LOG"
cat "$AGENT_OUT"
EOF
chmod +x "$tmp/bin/gh" "$tmp/bin/claude"
export PATH="$tmp/bin:$PATH"

CGO_ENABLED=0 go build -o "$tmp/lead" ./cmd/lead || fail "go build failed"

# A body that satisfies lead new / lead ready: 受入条件 / スコープ外 / シナリオ.
cat > "$AGENT_OUT" <<'EOF'
[{"title":"feat(cli): settled issue","body":"# feat(cli): settled issue\n\n## 受入条件\n\n- [ ] `./test/run-tests.sh` が pass する\n\n## スコープ外\n\n- none\n\n## シナリオ\n\n1. `lead say --settled` → ready で起票される"}]
EOF

# --- 1. --settled: risk:b + ready で起票し、シナリオを凍結する ---------------
out="$(cd "$repo" && "$tmp/lead" say "確定済みの依頼" --settled --agent claude 2>&1)" \
  || fail "say --settled exited non-zero: $out"
grep -q '^GH issue create --title feat(cli): settled issue --body .* --label risk:b --label ready$' "$GH_LOG" \
  || fail "settled create lacks risk:b + ready: $(cat "$GH_LOG")"
grep -q 'needs-review' "$GH_LOG" && fail "--settled must not add needs-review: $(cat "$GH_LOG")"
[ -f "$tmp/state/scenarios/o_r/200.md" ] || fail "--settled did not freeze the scenario outside the worktree"
grep -q '^## Steps$' "$tmp/state/scenarios/o_r/200.md" || fail "frozen scenario lacks ## Steps"
grep -q 'lead say --settled' "$tmp/state/scenarios/o_r/200.md" || fail "frozen scenario lost the step text"
case "$out" in *"created #200"*) ;; *) fail "output lacks created line: $out";; esac

# --- 2. default say: needs-review and no scenario freeze ---------------------
: > "$GH_LOG"
out="$(cd "$repo" && "$tmp/lead" say "未確定の依頼" --agent claude 2>&1)" \
  || fail "default say exited non-zero: $out"
grep -q '^GH issue create --title feat(cli): settled issue --body .* --label needs-review$' "$GH_LOG" \
  || fail "default create lacks needs-review: $(cat "$GH_LOG")"
grep -q 'risk:b' "$GH_LOG" && fail "default say must not add a risk label: $(cat "$GH_LOG")"
[ ! -e "$tmp/state/scenarios/o_r/201.md" ] || fail "default say must not freeze a scenario"

# --- 3. --settled with an incomplete body: nothing is created ---------------
cat > "$AGENT_OUT" <<'EOF'
[{"title":"feat(cli): unsettled issue","body":"# feat(cli): unsettled issue\n\n## 背景\n\nno required sections"}]
EOF
: > "$GH_LOG"
set +e
out="$(cd "$repo" && "$tmp/lead" say "未確定" --settled --agent claude 2>&1)"
status=$?
set -e
[ "$status" -eq 1 ] || fail "invalid settled exit = $status, want 1: $out"
case "$out" in *"受入条件"*) ;; *) fail "missing 受入条件 not reported: $out";; esac
case "$out" in *"シナリオ"*) ;; *) fail "missing シナリオ not reported: $out";; esac
grep -q '^GH issue create' "$GH_LOG" && fail "invalid settled body must not create an issue: $(cat "$GH_LOG")"

# --- 4. --settled --dry-run: no gh mutation, no freeze, labels shown ---------
cat > "$AGENT_OUT" <<'EOF'
[{"title":"feat(cli): settled issue","body":"# feat(cli): settled issue\n\n## 受入条件\n\n- [ ] `./test/run-tests.sh` が pass する\n\n## スコープ外\n\n- none\n\n## シナリオ\n\n1. `lead say --settled` → ready で起票される"}]
EOF
: > "$GH_LOG"
out="$(cd "$repo" && "$tmp/lead" say "dry" --settled --agent claude --dry-run 2>&1)" \
  || fail "settled dry-run exited non-zero: $out"
case "$out" in *"labels: risk:b,ready"*) ;; *) fail "settled dry-run labels unexpected: $out";; esac
if grep -Eq '^GH (issue create|issue edit|issue comment)' "$GH_LOG"; then
  fail "settled dry-run mutated gh: $(cat "$GH_LOG")"
fi
[ ! -e "$tmp/state/scenarios/o_r/202.md" ] || fail "settled dry-run must not freeze a scenario"

# --- 5. help documents --settled ---------------------------------------------
say_help="$("$tmp/lead" say --help)"
case "$say_help" in *"--settled"*) ;; *) fail "say --help lacks --settled: $say_help";; esac

echo "PASS: test_say_settled"
