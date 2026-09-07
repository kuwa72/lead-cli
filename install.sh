#!/bin/sh
# install.sh - lead installer (issue #43, RFC docs/rfc-26-distribution.md §5).
# A thin wrapper over GitHub Releases assets: fetch, sha256-verify, place.
# No builds, no conversions, and NEVER rewrites shell rc files
# (shell integration belongs to `lead setup`).
#
# Usage: install.sh [--version <vX.Y.Z|latest>] [--prefix <dir>]
#                    [--no-modify-path] [--help]
#
# Test seams (env): LEAD_RELEASE_BASE (default upstream releases),
# LEAD_UNAME_S / LEAD_UNAME_M (override uname -s/-m).
set -eu

BASE="${LEAD_RELEASE_BASE:-https://github.com/kuwa72/lead-cli}"
VERSION="latest"
PREFIX=""
NO_MODIFY_PATH=0

usage() {
  cat <<'EOF'
Usage: install.sh [--version <vX.Y.Z|latest>] [--prefix <dir>] [--no-modify-path] [--help]

  --version <vX.Y.Z|latest>  Release to install (default: latest).
  --prefix <dir>             Install to <dir>/bin/lead
                             (default: /usr/local, or ~/.local when unwritable).
  --no-modify-path           Suppress PATH guidance (for CI / non-interactive use).
  --help                     Show this message.

Recommended: download the script, inspect it, run --help, then install.
The script never edits shell rc files; run `lead setup` afterwards.
EOF
}

while [ $# -gt 0 ]; do
  case "$1" in
    --version) [ $# -ge 2 ] || { echo "error: --version needs a value" >&2; exit 1; }; VERSION="$2"; shift 2 ;;
    --prefix) [ $# -ge 2 ] || { echo "error: --prefix needs a value" >&2; exit 1; }; PREFIX="$2"; shift 2 ;;
    --no-modify-path) NO_MODIFY_PATH=1; shift ;;
    --help|-h) usage; exit 0 ;;
    *) echo "error: unknown argument: $1" >&2; usage >&2; exit 1 ;;
  esac
done

have() { command -v "$1" >/dev/null 2>&1; }

# --- platform -----------------------------------------------------------
OS_RAW="${LEAD_UNAME_S:-$(uname -s)}"
ARCH_RAW="${LEAD_UNAME_M:-$(uname -m)}"
case "$OS_RAW" in
  Linux) GOOS="linux" ;;
  Darwin) GOOS="darwin" ;;
  *) echo "error: unsupported OS: $OS_RAW (want Linux or Darwin)" >&2; exit 2 ;;
esac
case "$ARCH_RAW" in
  x86_64|amd64) GOARCH="amd64" ;;
  arm64|aarch64) GOARCH="arm64" ;;
  *) echo "error: unsupported arch: $ARCH_RAW (want amd64/x86_64 or arm64/aarch64)" >&2; exit 2 ;;
esac

# --- downloader ----------------------------------------------------------
if have curl; then
  DL="curl"
elif have wget; then
  DL="wget"
else
  echo "error: need curl or wget (HTTPS download)" >&2; exit 2
fi

download() { # url outfile
  if [ "$DL" = "curl" ]; then
    curl -fsSL "$1" -o "$2"
  else
    wget -qO "$2" "$1"
  fi
}

# --- version resolution ---------------------------------------------------
if [ "$VERSION" = "latest" ]; then
  if [ "$DL" = "curl" ]; then
    EFFECTIVE="$(curl -fsSL -o /dev/null -w '%{url_effective}' "$BASE/releases/latest")" \
      || { echo "error: cannot resolve latest release" >&2; exit 2; }
  else
    EFFECTIVE="$(wget -S --max-redirect=0 -O /dev/null "$BASE/releases/latest" 2>&1 \
      | awk '/[Ll]ocation:/ {print $2}' | tr -d '\r' | head -n 1)"
    [ -n "$EFFECTIVE" ] || { echo "error: cannot resolve latest release" >&2; exit 2; }
  fi
  VERSION="$(printf '%s' "$EFFECTIVE" | sed 's|.*/tag/||; s|/$||')"
  [ -n "$VERSION" ] || { echo "error: cannot parse tag from $EFFECTIVE" >&2; exit 2; }
  echo "Resolved latest: $VERSION"
fi

ASSET="lead_${GOOS}_${GOARCH}.tar.gz"
DLBASE="$BASE/releases/download/$VERSION"

# --- prefix ---------------------------------------------------------------
if [ -z "$PREFIX" ]; then
  if [ -w "/usr/local" ]; then
    PREFIX="/usr/local"
  else
    PREFIX="$HOME/.local"
  fi
fi
BINDIR="$PREFIX/bin"

# --- fetch + verify + place (temp dir cleaned on any exit) ----------------
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT INT TERM

echo "Downloading $ASSET ($VERSION)..."
download "$DLBASE/$ASSET" "$WORK/$ASSET" \
  || { echo "error: download failed: $DLBASE/$ASSET" >&2; exit 2; }
download "$DLBASE/checksums.txt" "$WORK/checksums.txt" \
  || { echo "error: download failed: $DLBASE/checksums.txt" >&2; exit 2; }

WANT="$(awk -v asset="$ASSET" '$2 == asset {print $1}' "$WORK/checksums.txt")"
[ -n "$WANT" ] || { echo "error: checksums.txt has no entry for $ASSET" >&2; exit 2; }

if have sha256sum; then
  printf '%s  %s\n' "$WANT" "$WORK/$ASSET" | (cd "$WORK" && sha256sum -c -) >/dev/null \
    || { echo "error: sha256 mismatch for $ASSET (refusing to install)" >&2; exit 2; }
elif have shasum; then
  printf '%s  %s\n' "$WANT" "$WORK/$ASSET" | (cd "$WORK" && shasum -a 256 -c -) >/dev/null \
    || { echo "error: sha256 mismatch for $ASSET (refusing to install)" >&2; exit 2; }
else
  echo "error: need sha256sum or shasum for verification" >&2; exit 2
fi
echo "Checksum OK."

tar -xzf "$WORK/$ASSET" -C "$WORK" \
  || { echo "error: cannot extract $ASSET" >&2; exit 2; }
[ -f "$WORK/lead" ] || { echo "error: archive has no lead binary" >&2; exit 2; }

mkdir -p "$BINDIR" || { echo "error: cannot create $BINDIR" >&2; exit 2; }
cp "$WORK/lead" "$BINDIR/lead" || { echo "error: cannot write $BINDIR/lead" >&2; exit 2; }
chmod 755 "$BINDIR/lead"

"$BINDIR/lead" --help >/dev/null 2>&1 \
  || { echo "error: installed binary fails --help smoke test" >&2; rm -f "$BINDIR/lead"; exit 2; }

echo "Installed lead $VERSION to $BINDIR/lead"
if [ "$NO_MODIFY_PATH" -eq 0 ]; then
  case ":$PATH:" in
    *":$BINDIR:"*) ;;
    *) echo "Next: add $BINDIR to PATH, then run \`lead setup\`." ;;
  esac
fi
