#!/usr/bin/env bash
# issue #43: install.sh の振る舞いテスト。
# ダミーReleasesサーバ＋一時PREFIXで配置・権限・--help 終了状態を検証し、
# checksum不一致・対応外Archで非ゼロ＋配置物なしを検証する (grep検査なし)。
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

fail() { echo "FAIL: $*" >&2; exit 1; }

[ -f install.sh ] || fail "install.sh missing (Red: not implemented yet)"
command -v python3 >/dev/null || fail "python3 not available"

# --- dummy Releases server ---------------------------------------------
# layout: /releases/latest -> 302 /tag/<v>/
#         /releases/download/<v>/lead_<os>_<arch>.tar.gz + checksums.txt
SRV="$tmp/srv"
mkdir -p "$SRV/releases/download/v0.9.9"
PAYLOAD="$tmp/payload"
mkdir -p "$PAYLOAD"
cat > "$PAYLOAD/lead" <<'EOF'
#!/bin/sh
if [ "$1" = "--help" ]; then echo "Usage: lead (fixture)"; exit 0; fi
echo "lead version fixture (commit: none, built: unknown)"
EOF
chmod +x "$PAYLOAD/lead"
tar -czf "$SRV/releases/download/v0.9.9/lead_linux_amd64.tar.gz" -C "$PAYLOAD" lead
(cd "$SRV/releases/download/v0.9.9" && sha256sum lead_linux_amd64.tar.gz > checksums.txt)
# The redirect target must exist (real release pages return 200).
mkdir -p "$SRV/tag/v0.9.9"
echo "release v0.9.9" > "$SRV/tag/v0.9.9/index.html"
PORT=0
for _ in $(seq 1 20); do
  PORT=$((18080 + RANDOM % 2000))
  (python3 -c "import socket; socket.socket().bind(('127.0.0.1', $PORT))") 2>/dev/null && break
done
cat > "$tmp/server.py" <<EOF
import http.server, functools
class H(http.server.SimpleHTTPRequestHandler):
    def do_GET(self):
        if self.path == "/releases/latest":
            self.send_response(302)
            self.send_header("Location", "/tag/v0.9.9/")
            self.send_header("Content-Length", "0")
            self.end_headers()
        else:
            super().do_GET()
    def log_message(self, *a):
        pass
http.server.ThreadingHTTPServer(("127.0.0.1", $PORT),
    functools.partial(H, directory="$SRV")).serve_forever()
EOF
python3 "$tmp/server.py" & SRV_PID=$!
trap 'kill $SRV_PID 2>/dev/null; rm -rf "$tmp"' EXIT
for _ in $(seq 1 50); do
  curl -fsS "http://127.0.0.1:$PORT/releases/download/v0.9.9/checksums.txt" >/dev/null 2>&1 && break
  sleep 0.1
done
export LEAD_RELEASE_BASE="http://127.0.0.1:$PORT"

# Release-path tests use a copy outside the source checkout: ./install.sh in
# a checkout is the build-locally case (issue #256).
STANDALONE="$tmp/standalone"
mkdir -p "$STANDALONE"
cp install.sh "$STANDALONE/install.sh"

# --- 1. --help exits 0 with usage ---
sh "$STANDALONE/install.sh" --help | grep -q "Usage" || fail "--help missing usage"

# --- 2. default install (latest resolution) into temp PREFIX ---
PREFIX="$tmp/prefix"
sh "$STANDALONE/install.sh" --prefix "$PREFIX" || fail "install failed"
[ -x "$PREFIX/bin/lead" ] || fail "installed binary missing or not executable"
"$PREFIX/bin/lead" --help >/dev/null || fail "installed lead --help failed"

# --- 3. pinned version install ---
PREFIX2="$tmp/prefix2"
sh "$STANDALONE/install.sh" --version v0.9.9 --prefix "$PREFIX2" --no-modify-path >/dev/null \
  || fail "pinned install failed"
"$PREFIX2/bin/lead" --help >/dev/null || fail "pinned lead --help failed"

# --- 4. checksum mismatch: non-zero, nothing placed ---
(cd "$SRV/releases/download/v0.9.9" && echo "deadbeef  lead_linux_amd64.tar.gz" > checksums.txt)
PREFIX3="$tmp/prefix3"
sh "$STANDALONE/install.sh" --version v0.9.9 --prefix "$PREFIX3" >/dev/null 2>&1 \
  && fail "tampered install exited 0"
[ ! -e "$PREFIX3/bin/lead" ] || fail "tampered install left a binary"
(cd "$SRV/releases/download/v0.9.9" && sha256sum lead_linux_amd64.tar.gz > checksums.txt)

# --- 5. unsupported arch: non-zero, nothing placed ---
PREFIX4="$tmp/prefix4"
LEAD_UNAME_M=riscv64 sh "$STANDALONE/install.sh" --prefix "$PREFIX4" >/dev/null 2>&1 \
  && fail "unsupported-arch install exited 0"
[ ! -e "$PREFIX4/bin/lead" ] || fail "unsupported-arch install left a binary"

# --- 6. no downloader: non-zero ---
# A bin dir with every tool EXCEPT curl/wget proves the downloader check.
mkdir -p "$tmp/emptybin"
for tool in uname mktemp mkdir rm cp chmod tar awk sed tr head; do
  ln -s "$(command -v "$tool")" "$tmp/emptybin/$tool" 2>/dev/null || true
done
PATH="$tmp/emptybin" sh "$STANDALONE/install.sh" --prefix "$tmp/prefix5" >/dev/null 2>&1 \
  && fail "no-downloader install exited 0"

# --- 7. chain: install → setup → doctor → update --check (covers #44) ---
mkdir -p "$tmp/bin-gh"
cat > "$tmp/bin-gh/gh" <<'EOF'
#!/bin/sh
case "$1 $2" in
  "auth status") : ;;
  "api user") printf 'octocat' ;;
  *) if [ "$1" = "api" ]; then printf 'v0.9.9'; else echo "unexpected: $@" >&2; exit 3; fi ;;
esac
EOF
chmod +x "$tmp/bin-gh/gh"
CHAIN_HOME="$tmp/chain-home"; mkdir -p "$CHAIN_HOME"
CHAIN_PFX="$tmp/chain-prefix"
sh "$STANDALONE/install.sh" --version v0.9.9 --prefix "$CHAIN_PFX" --no-modify-path >/dev/null \
  || fail "chain: install failed"
"$CHAIN_PFX/bin/lead" --help >/dev/null || fail "chain: installed lead --help failed"
# Post-install flow with a real lead build + dummy gh (covers #44 chain).
CGO_ENABLED=0 go build -o "$tmp/lead-real" ./cmd/lead || fail "chain: real build failed"
CHAIN_PATH="$tmp/bin-gh:$PATH"
HOME="$CHAIN_HOME" PATH="$CHAIN_PATH" "$tmp/lead-real" setup --shell bash \
  --write --yes --no-keybinding >/dev/null || fail "chain: setup failed"
HOME="$CHAIN_HOME" PATH="$CHAIN_PATH" "$tmp/lead-real" doctor --offline >/dev/null \
  || fail "chain: doctor failed"
chain_up="$(HOME="$CHAIN_HOME" PATH="$CHAIN_PATH" "$tmp/lead-real" update --check 2>&1 || true)"
case "$chain_up" in *"Update available"*) ;; *) fail "chain: update --check missing display";; esac

# --- 8. no published release: clear error, no install -------------------
mkdir -p "$tmp/norel/releases"
echo "no releases" > "$tmp/norel/releases/index.html"
PORT2=0
for _ in $(seq 1 20); do
  PORT2=$((20000 + RANDOM % 2000))
  (python3 -c "import socket; socket.socket().bind(('127.0.0.1', $PORT2))") 2>/dev/null && break
done
cat > "$tmp/norel.py" <<'PYSRV'
import http.server, functools, sys
class H(http.server.SimpleHTTPRequestHandler):
    def do_GET(self):
        if self.path == "/releases/latest":
            self.send_response(302)
            self.send_header("Location", "/releases")
            self.send_header("Content-Length", "0")
            self.end_headers()
        else:
            super().do_GET()
    def log_message(self, *a):
        pass
http.server.ThreadingHTTPServer(("127.0.0.1", int(sys.argv[1])),
    functools.partial(H, directory=sys.argv[2])).serve_forever()
PYSRV
python3 "$tmp/norel.py" "$PORT2" "$tmp/norel" & NOREL_PID=$!
for _ in $(seq 1 50); do
  curl -fsS "http://127.0.0.1:$PORT2/releases" >/dev/null 2>&1 && break
  sleep 0.1
done
NOREL_OUT="$tmp/norel.out"
LEAD_RELEASE_BASE="http://127.0.0.1:$PORT2" \
  sh "$STANDALONE/install.sh" --prefix "$tmp/prefix6" --no-modify-path >"$NOREL_OUT" 2>&1 \
  && fail "no-release install exited 0"
grep -q "no published release" "$NOREL_OUT" || fail "no-release error lacks explanation: $(cat "$NOREL_OUT")"
grep -q "bin/install-local" "$NOREL_OUT" || fail "no-release error lacks local guidance: $(cat "$NOREL_OUT")"
[ ! -e "$tmp/prefix6/bin/lead" ] || fail "no-release install left a binary"
kill "$NOREL_PID" 2>/dev/null || true

# --- 9. source checkout: refuse the release path, name bin/install-local ---
CHECKOUT_OUT="$tmp/checkout.out"
LEAD_RELEASE_BASE="http://127.0.0.1:$PORT" \
  sh "$ROOT/install.sh" --prefix "$tmp/prefix7" --no-modify-path >"$CHECKOUT_OUT" 2>&1 \
  && fail "checkout install exited 0"
grep -q "bin/install-local" "$CHECKOUT_OUT" || fail "checkout error lacks local guidance: $(cat "$CHECKOUT_OUT")"
[ ! -e "$tmp/prefix7/bin/lead" ] || fail "checkout install left a binary"

echo "install.sh behavioral checks passed"
