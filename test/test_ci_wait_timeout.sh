#!/usr/bin/env bash
# issue #61: bin/ci-wait の gh 停滞対策の振る舞いテスト。
# 応答しないダミーghに対し、待機が有界時間で明示終了することを検証する。
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

fail() { echo "FAIL: $*" >&2; exit 1; }

command -v timeout >/dev/null || fail "coreutils timeout not available"

# --- stalled dummy gh: sleeps 30s on every call (no response) ---
mkdir -p "$tmp/bin"
cat > "$tmp/bin/gh" <<'EOF'
#!/bin/sh
sleep 30
echo "stalled gh should have been killed" >&2
exit 1
EOF
chmod +x "$tmp/bin/gh"

# --- PR auto-detect with a stalled gh must fail fast, not hang ---
start=$(date +%s)
set +e
CI_WAIT_GH_TIMEOUT=2 PATH="$tmp/bin:$PATH" ./bin/ci-wait >/dev/null 2>&1
code=$?
set -e
end=$(date +%s)
elapsed=$((end - start))
[ "$code" -eq 2 ] || fail "stalled auto-detect exit = $code, want 2"
[ "$elapsed" -lt 20 ] || fail "stalled auto-detect took ${elapsed}s, want bounded (<20s)"

echo "ci-wait timeout behavioral checks passed"
