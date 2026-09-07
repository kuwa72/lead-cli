#!/usr/bin/env bash
# issue #39: CLI骨格 (cobra採否確定含む) の振る舞いテスト。
# ソースのgrepではなく、ビルドしたバイナリの実行・終了状態・出力をアサートする。
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

fail() { echo "FAIL: $*" >&2; exit 1; }

CGO_ENABLED=0 go build -o "$tmp/lead" ./cmd/lead || fail "go build ./cmd/lead failed"
CGO_ENABLED=0 go build \
  -ldflags "-X main.Version=test-9.9.9 -X main.Commit=abc1234 -X main.Date=2026-09-07" \
  -o "$tmp/lead-stamped" ./cmd/lead || fail "stamped build failed"

# 1. 各コマンド --help が終了状態 0 で用法を表示すること
for cmd in version work setup completion doctor update install; do
  out="$("$tmp/lead" "$cmd" --help)" || fail "lead $cmd --help exited non-zero"
  case "$out" in *"lead $cmd"*) ;; *) fail "lead $cmd --help missing usage header";; esac
done
root_help="$("$tmp/lead" --help)" || fail "lead --help exited non-zero"
for cmd in work setup completion doctor update install version; do
  case "$root_help" in *"$cmd"*) ;; *) fail "lead --help missing command $cmd";; esac
done

# 2. 未知サブコマンドは非ゼロ終了すること
"$tmp/lead" no-such-command >/dev/null 2>&1 && fail "unknown subcommand exited 0"
"$tmp/lead" work --bogus-flag >/dev/null 2>&1 && fail "unknown flag exited 0"

# 3. version は注入された版数情報を表示すること
stamped_out="$("$tmp/lead-stamped" version)" || fail "stamped lead version failed"
case "$stamped_out" in *test-9.9.9*) ;; *) fail "stamped version missing";; esac
case "$stamped_out" in *abc1234*) ;; *) fail "stamped commit missing";; esac

# 4. completion は実補完スクリプトを生成すること (bash出力は bash -n で構文検証)
bash_out="$("$tmp/lead" completion bash)" || fail "lead completion bash failed"
[ -n "$bash_out" ] || fail "completion bash output empty"
echo "$bash_out" | bash -n || fail "generated bash completion fails syntax check"
"$tmp/lead" completion csh >/dev/null 2>&1 && fail "unsupported shell exited 0"

# 5. 対話系の未確定部分は非ゼロ終了すること。
# bare `lead work` は実 picker を開くため、非TTY環境では
# TTY エラーで非ゼロ終了する (正常系は test_tui_picker.sh で検証)。
"$tmp/lead" work >/dev/null 2>&1 && fail "bare 'lead work' exited 0"

echo "cli skeleton behavioral checks passed"
