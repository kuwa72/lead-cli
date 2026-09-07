#!/usr/bin/env bash
# issue #75: bin/install-local の振る舞いテスト。
# 作業ツリーからビルドし、指定ディレクトリ (既定 ~/local/bin) へ lead を
# 配置することを、終了ステータス・生成物・version 出力でアサートする。
# ソースのgrepではなく実行結果を検証する。
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

fail() { echo "FAIL: $*" >&2; exit 1; }

# 1. LEAD_LOCAL_DIR 差し替えで実行すると、その先へ lead が生成されること
LEAD_LOCAL_DIR="$tmp/bin" sh bin/install-local >/dev/null || fail "install-local exited non-zero"
[ -x "$tmp/bin/lead" ] || fail "$tmp/bin/lead not installed or not executable"

# 2. 配置されたバイナリが version を実行できること (smoke test)
out="$("$tmp/bin/lead" version)" || fail "installed lead version exited non-zero"
[ -n "$out" ] || fail "version output empty"

# 3. dev 版として版数情報が注入されていること
case "$out" in *dev*) ;; *) fail "version output missing dev marker: $out";; esac

# 4. git 環境ではコミットハッシュが埋め込まれること
want_commit="$(git rev-parse --short HEAD 2>/dev/null || true)"
if [ -n "$want_commit" ]; then
  case "$out" in *"$want_commit"*) ;; *) fail "version missing commit $want_commit: $out";; esac
fi

# 5. 再実行が冪等であること (上書きインストールで失敗しない)
LEAD_LOCAL_DIR="$tmp/bin" sh bin/install-local >/dev/null || fail "reinstall exited non-zero"
[ -x "$tmp/bin/lead" ] || fail "lead missing after reinstall"

# 6. 既定配置先は $HOME/.local/bin (LEAD_LOCAL_DIR 未指定時, issue #77)。
#    一時 HOME で検証。Go キャッシュは実パスを維持する。
REAL_GOPATH="$(go env GOPATH)"
REAL_GOCACHE="$(go env GOCACHE)"
(
  export HOME="$tmp/home" GOPATH="$REAL_GOPATH" GOCACHE="$REAL_GOCACHE"
  unset LEAD_LOCAL_DIR || true
  mkdir -p "$HOME"
  sh "$ROOT/bin/install-local" >/dev/null
) || fail "default-dir install exited non-zero"
[ -x "$tmp/home/.local/bin/lead" ] || fail "default install did not place lead in \$HOME/.local/bin"
[ ! -e "$tmp/home/local/bin/lead" ] || fail "lead installed to \$HOME/local/bin (non-dot dir)"

echo "install-local behavioral checks passed"
