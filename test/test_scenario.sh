#!/usr/bin/env bash
# issue #276: シナリオを worktree 外に保存し、PR head で使い捨てテストを
# 生成・実行する。一時 HOME・一時リポジトリ・ダミー agy の下で
#   - `lead scenario add` が <state-dir>/scenarios/<owner>_<repo>/<issue>.md に
#     保存し、リポジトリ内に何も作らない
#   - `lead scenario list` が保存済み Issue を表示する
#   - `lead scenario run` が生成物を worktree にコピーして実行し、1/2 を返す
#   - 実行後に worktree に生成物が残らない（git status --porcelain が空）
#   - シナリオ未設定の Issue は 0/0 で成功する
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
unset XDG_STATE_HOME || true
export LEAD_STATE_FILE="$tmp/state/workflows.json"
mkdir -p "$HOME"

repo="$tmp/repo"
git init -q -b main "$repo" || fail "git init failed"
git -C "$repo" config user.email "t@t"
git -C "$repo" config user.name "t"
git -C "$repo" remote add origin git@github.com:o/r.git
echo x > "$repo/f.txt"
git -C "$repo" add . && git -C "$repo" commit -qm init

export AGY_LOG="$tmp/agy.log"
export SCENARIO_RUN_MARKER="$tmp/run-marker"
: > "$AGY_LOG"
mkdir -p "$tmp/bin"
cat > "$tmp/bin/agy" <<'EOF'
#!/bin/sh
printf 'AGY %s\n' "$*" >> "$AGY_LOG"
for last; do :; done
prompt="${last##*file: }"
out="$(sed -n 's/^- 出力先: //p' "$prompt" | head -n1)"
[ -n "$out" ] || { echo "no output dir in prompt" >&2; exit 3; }
mkdir -p "$out"
cat > "$out/run.sh" <<'RUN'
#!/bin/sh
echo executed > "$SCENARIO_RUN_MARKER"
echo "SCENARIO 1: pass"
echo "SCENARIO 2: fail"
RUN
cat > "$out/generated.txt" <<'GEN'
throwaway
GEN
echo "agent finished (no verdicts)"
EOF
chmod +x "$tmp/bin/agy"
export PATH="$tmp/bin:$PATH"

CGO_ENABLED=0 go build -o "$tmp/lead" ./cmd/lead || fail "go build failed"
cd "$repo"

# --- 1. add stores the scenario outside the worktree ------------------------
scenario_file="$tmp/state/scenarios/o_r/276.md"
out="$(printf '## Steps\n\n1. first\n2. second\n' | "$tmp/lead" scenario add 276)" \
  || fail "lead scenario add failed: $out"
[ -f "$scenario_file" ] || fail "scenario not stored at $scenario_file"
[ ! -e "$repo/scenarios" ] || fail "scenario was created inside the worktree"
[ -z "$(git status --porcelain)" ] || fail "scenario add dirtied the worktree: $(git status --porcelain)"

# --- 2. list shows the saved issue -----------------------------------------
out="$("$tmp/lead" scenario list)" || fail "lead scenario list failed"
case "$out" in *"#276"*) ;; *) fail "scenario list lacks #276: $out";; esac

# --- 3. run generates, executes, reports 1/2, and cleans up ----------------
: > "$AGY_LOG"
rm -f "$SCENARIO_RUN_MARKER"
set +e
out="$("$tmp/lead" scenario run 276 --worktree "$repo" 2>&1)"
code=$?
set -e
[ "$code" -eq 1 ] || fail "scenario run with a failing step exited $code, want 1: $out"
[ -f "$SCENARIO_RUN_MARKER" ] || fail "generated run.sh was not executed in the worktree"
case "$out" in *"scenarios: 1/2"*) ;; *) fail "run did not report 1/2: $out";; esac
case "$out" in *"SCENARIO 1: pass"*) ;; *) fail "run did not report step 1 pass: $out";; esac
case "$out" in *"SCENARIO 2: fail"*) ;; *) fail "run did not report step 2 fail: $out";; esac
[ -s "$AGY_LOG" ] || fail "the generator agent was not invoked"
[ ! -e "$repo/.lead-scenario" ] || fail "generated tests were left in the worktree"
[ -z "$(git status --porcelain)" ] || fail "generated files dirtied the worktree: $(git status --porcelain)"

# --- 4. an issue without a scenario reports 0/0 and succeeds ---------------
# Even when the unrelated inbox config is corrupt (the 0/0 path must not
# depend on agent settings).
mkdir -p "$tmp/state"
printf '{"stall_timeout":"not-a-duration"}\n' > "$tmp/state/inbox-config.json"
: > "$AGY_LOG"
set +e
out="$("$tmp/lead" scenario run 999 --worktree "$repo" 2>&1)"
code=$?
set -e
[ "$code" -eq 0 ] || fail "scenario run without a scenario exited $code, want 0: $out"
case "$out" in *"scenarios: 0/0"*) ;; *) fail "run without a scenario did not report 0/0: $out";; esac
[ -s "$AGY_LOG" ] && fail "the agent ran for an issue without a scenario: $(cat "$AGY_LOG")"

echo "scenario behavioral checks passed"
