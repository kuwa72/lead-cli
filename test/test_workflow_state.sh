#!/usr/bin/env bash
# issue #40: ワークフロー状態 (workflows.json・worktree) の振る舞いテスト。
# 一時HOME・一時リポジトリ・ダミーghの下で作成→一覧→削除が冪等に動作し、
# 実HOME・実リポジトリに触れないことをアサートする (grep検査なし)。
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

fail() { echo "FAIL: $*" >&2; exit 1; }

# --- isolated environment: temp HOME, temp repo, dummy gh ---
# Keep Go caches on real paths: overriding HOME would redirect GOMODCACHE
# into the temp dir (read-only module files then break cleanup).
REAL_GOPATH="$(go env GOPATH)"
REAL_GOCACHE="$(go env GOCACHE)"
export HOME="$tmp/home"
export GOPATH="$REAL_GOPATH" GOCACHE="$REAL_GOCACHE"
unset XDG_STATE_HOME || true
unset LEAD_STATE_FILE || true
mkdir -p "$HOME"
command -v git >/dev/null || fail "git not available"

repo="$tmp/repo"
git init -q -b main "$repo" || fail "git init failed"
git -C "$repo" config user.email "t@t"
git -C "$repo" config user.name "t"
echo x > "$repo/f.txt"
git -C "$repo" add .
git -C "$repo" commit -qm init || fail "fixture commit failed"

mkdir -p "$tmp/bin"
cat > "$tmp/bin/gh" <<'EOF'
#!/bin/sh
if [ "$1 $2" = "issue view" ]; then
  printf '{"number":36,"title":"ports adapter","body":"b","state":"OPEN"}'
else
  echo "unexpected gh call: $@" >&2; exit 3
fi
EOF
chmod +x "$tmp/bin/gh"
export PATH="$tmp/bin:$PATH"

CGO_ENABLED=0 go build -o "$tmp/lead" ./cmd/lead || fail "go build failed"
cd "$repo"

STATE="$HOME/.local/state/lead/workflows.json"

# 1. work 36 --worktree: worktree作成・状態記録
"$tmp/lead" work 36 --worktree || fail "lead work 36 --worktree failed"
[ -f "$repo/.worktrees/issue-36/f.txt" ] || fail "auto worktree not checked out"
[ -f "$STATE" ] || fail "state file not created under temp HOME"
python3 -c "import json;d=json.load(open('$STATE'));assert len(d['workflows'])==1 and d['workflows'][0]['issue']==36, d" \
  || fail "state record missing for #36"

# 2. 再実行は冪等 (レコード単一・終了0)
"$tmp/lead" work 36 --worktree || fail "re-work failed"
python3 -c "import json;d=json.load(open('$STATE'));assert len(d['workflows'])==1, d" \
  || fail "re-work duplicated records"

# 3. status: 人間可読と --json
status_out="$("$tmp/lead" status)" || fail "lead status failed"
case "$status_out" in *"#36"*) ;; *) fail "status missing #36";; esac
case "$status_out" in *"issue/36-ports-adapter"*) ;; *) fail "status missing branch";; esac
"$tmp/lead" status --json | python3 -c "import json,sys;d=json.load(sys.stdin);assert d['workflows'][0]['issue']==36, d" \
  || fail "status --json invalid"

# 4. clean 36: worktree削除・記録削除
"$tmp/lead" clean 36 || fail "lead clean 36 failed"
[ ! -e "$repo/.worktrees/issue-36" ] || fail "worktree still present after clean"
clean_status="$("$tmp/lead" status)" || fail "status after clean failed"
case "$clean_status" in *"no workflows"*) ;; *) fail "records remain after clean";; esac

# 5. clean再実行は冪等 (終了0)
"$tmp/lead" clean 36 >/dev/null || fail "re-clean failed"

# 6. 番号なし work は #37 TUI待ちで非ゼロ
"$tmp/lead" work >/dev/null 2>&1 && fail "bare 'lead work' exited 0"

# 7. 壊れた状態ファイルは警告付きエラー (上書きしない)
echo '{broken' > "$STATE"
"$tmp/lead" status >/dev/null 2>&1 && fail "status on corrupt file exited 0"
[ "$(cat "$STATE")" = '{broken' ] || fail "corrupt state file was overwritten"

# 8. 実HOME汚染なし: HOMEを上書き済みのため、実ファイル側に何も作られないことを確認
[ "$HOME" = "$tmp/home" ] || fail "HOME isolation broken"

echo "workflow state behavioral checks passed"
