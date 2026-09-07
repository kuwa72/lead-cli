#!/usr/bin/env bash
# issue #42: リリースパイプラインの振る舞いテスト。
# `goreleaser release --snapshot --clean` で全マトリクスをビルドし、
# 成果物の存在・checksums 検証・各linuxアセットの版数表示をアサートする。
# (ソースgrepなし。goreleaser自体は未導入時にGOBIN隔離で導入する)
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

fail() { echo "FAIL: $*" >&2; exit 1; }

REAL_GOPATH="$(go env GOPATH)"
REAL_GOCACHE="$(go env GOCACHE)"
export GOPATH="$REAL_GOPATH" GOCACHE="$REAL_GOCACHE" GOBIN="$tmp/gobin"
mkdir -p "$GOBIN"
export PATH="$GOBIN:$PATH"

if ! command -v goreleaser >/dev/null 2>&1; then
  echo "installing goreleaser into isolated GOBIN..."
  go install github.com/goreleaser/goreleaser/v2@latest || fail "goreleaser install failed"
fi
command -v goreleaser >/dev/null || fail "goreleaser unavailable"
goreleaser --version >/dev/null || fail "goreleaser --version failed"

# snapshot build of the full matrix (uses .goreleaser.yaml)
goreleaser release --snapshot --clean || fail "goreleaser snapshot build failed"

# 1. all four tarballs + checksums exist (update contract: unversioned names)
for asset in lead_linux_amd64.tar.gz lead_linux_arm64.tar.gz \
             lead_darwin_amd64.tar.gz lead_darwin_arm64.tar.gz checksums.txt; do
  [ -s "dist/$asset" ] || fail "dist/$asset missing or empty"
done

# 2. checksums.txt verifies (run inside dist so relative paths resolve)
(cd dist && sha256sum -c checksums.txt) || fail "checksums.txt verification failed"

# 3. linux/amd64 asset runs and stamps the version
mkdir -p "$tmp/x-amd64"
tar -xzf dist/lead_linux_amd64.tar.gz -C "$tmp/x-amd64" || fail "extract amd64 failed"
ver_out="$("$tmp/x-amd64/lead" version)" || fail "linux/amd64 lead version failed"
case "$ver_out" in *"lead version"*) ;; *) fail "version output malformed: $ver_out";; esac
[ -n "$ver_out" ] || fail "version output empty"

# 4. other tarballs contain a lead binary each
for asset in lead_linux_arm64.tar.gz lead_darwin_amd64.tar.gz lead_darwin_arm64.tar.gz; do
  tar -tzf "dist/$asset" | grep -qE "(^|/)lead$" || fail "$asset missing lead binary"
done

echo "release pipeline behavioral checks passed"
