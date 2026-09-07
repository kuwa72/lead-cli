#!/usr/bin/env bash
# issue #44: setup・completion・doctor・update の振る舞いテスト。
# 一時HOME＋実シェル読込で補完・バインド・冪等・除去を検証し、
# ダミーgh/herdr/agentのPATH注入で doctor/update の3系を検証する (grep検査なし)。
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
unset LEAD_STATE_FILE || true
mkdir -p "$HOME"

# --- dummy gh/herdr/agent (deterministic via GH_AUTH/GH_LATEST) ---
mkdir -p "$tmp/bin"
cat > "$tmp/bin/gh" <<'EOF'
#!/bin/sh
echo "GHLOG $@" >> "$GH_ARGS_LOG"
case "$1 $2" in
  "auth status")
    [ "${GH_AUTH:-ok}" = "ok" ] || { echo "not logged in" >&2; exit 1; } ;;
  "api user")
    printf 'octocat' ;;
  *)
    if [ "$1" = "api" ]; then
      case "$2" in
        *releases/latest) printf '%s' "${GH_LATEST:-v9.9.9}" ;;
        *) printf 'true' ;;
      esac
    else
      echo "unexpected gh call: $@" >&2; exit 3
    fi ;;
esac
EOF
cat > "$tmp/bin/herdr" <<'EOF'
#!/bin/sh
echo "HERDRLOG $@" >> "$GH_ARGS_LOG"
EOF
cat > "$tmp/bin/agy" <<'EOF'
#!/bin/sh
echo "AGYLOG $@" >> "$GH_ARGS_LOG"
EOF
chmod +x "$tmp/bin/"*
export PATH="$tmp/bin:$PATH"
export GH_ARGS_LOG="$tmp/gh-args.log"
: > "$GH_ARGS_LOG"

CGO_ENABLED=0 go build -o "$tmp/lead" ./cmd/lead || fail "go build failed"
export PATH="$tmp:$PATH" # fish completion shells out to `lead`

# --- bash: setup → real-shell load → idempotency → keybinding → uninstall ---
"$tmp/lead" setup --shell bash --write --yes --no-keybinding || fail "bash setup failed"
RC="$HOME/.bashrc"
COMP="$HOME/.local/share/bash-completion/completions/lead"
[ -f "$RC" ] && [ -f "$COMP" ] || fail "bash managed files missing"
bash -n "$RC" || fail "rc fails bash syntax check"
bash -c "source '$RC' && complete -p lead" | grep -q "__start_lead" \
  || fail "lead completion not registered in real bash"
# keybinding is opt-in: absent by default, and no -x bindings registered.
echo "source '$RC'; bind -X" | bash -i 2>/dev/null | grep -q "C-g" \
  && fail "keybinding enabled by default"
# idempotent re-run: single block, no changes.
"$tmp/lead" setup --shell bash --write --yes --no-keybinding | grep -q "no changes" \
  || fail "re-setup not idempotent"
[ "$(grep -c "lead >>>" "$RC")" = "1" ] || fail "rc block duplicated"
# opt-in run on a second HOME enables Ctrl-G in a real interactive bash.
HOME2="$tmp/home2"; mkdir -p "$HOME2"
printf 'y\ny\n' | HOME="$HOME2" "$tmp/lead" setup --shell bash --write >/dev/null \
  || fail "opt-in setup failed"
echo "source '$HOME2/.bashrc'; bind -X" | bash -i 2>/dev/null | grep -q '"\\C-g"' \
  || fail "opt-in Ctrl-G not registered in real bash"
# uninstall removes the block and completion; rc still sources cleanly.
"$tmp/lead" setup --shell bash --uninstall || fail "uninstall failed"
grep -q "lead >>>" "$RC" && fail "rc block remains after uninstall"
[ ! -e "$COMP" ] || fail "completion remains after uninstall"
bash -c "source '$RC'" || fail "rc broken after uninstall"

# --- fish: setup → real-fish completion candidates (when fish exists) ---
if command -v fish >/dev/null 2>&1; then
  "$tmp/lead" setup --shell fish --write --yes --no-keybinding || fail "fish setup failed"
  HOME="$HOME" fish -c 'complete -C"lead "' | grep -q "^work" \
    || fail "fish completion candidates missing 'work'"
else
  echo "(fish absent: skipping real-fish completion check)"
fi

# --- doctor: success / auth-mismatch / (brew系は update 側で検証) ---
GH_AUTH=ok "$tmp/lead" doctor --offline || fail "healthy doctor failed"
GH_AUTH=fail "$tmp/lead" doctor --offline >/dev/null 2>&1 \
  && fail "doctor with bad auth exited 0"
doc_out="$(GH_AUTH=fail "$tmp/lead" doctor --offline 2>&1 || true)"
case "$doc_out" in *"gh auth login"*) ;; *) fail "doctor missing login guidance";; esac
GH_AUTH=ok "$tmp/lead" doctor --offline --json | python3 -c \
  "import json,sys; d=json.load(sys.stdin); assert any(c['name']=='gh auth' and c['required'] for c in d['checks']), d" \
  || fail "doctor --json shape invalid"

# --- update --check: available vs up-to-date (stamped binary) ---
# NOTE: --check exits 1 when an update is available, so capture with || true.
GH_LATEST=v9.9.9 "$tmp/lead" update --check >/dev/null 2>&1 \
  && fail "update --check with newer tag exited 0"
avail_out="$(GH_LATEST=v9.9.9 "$tmp/lead" update --check 2>&1 || true)"
case "$avail_out" in *"Update available"*) ;; *) fail "update --check missing availability display";; esac
CGO_ENABLED=0 go build -ldflags "-X main.Version=v9.9.9" -o "$tmp/lead-stamped" ./cmd/lead \
  || fail "stamped build failed"
GH_LATEST=v9.9.9 "$tmp/lead-stamped" update --check | grep -q "up to date" \
  || fail "stamped update --check not up-to-date"

# --- update under brew management: guidance, no replacement ---
mkdir -p "$tmp/fakebrew/bin"
cat > "$tmp/fakebrew/bin/brew" <<'EOF'
#!/bin/sh
if [ "$1" = "--prefix" ]; then echo "$FAKEBREW"; fi
EOF
chmod +x "$tmp/fakebrew/bin/brew"
export FAKEBREW="$tmp/fakebrew"
FAKECELLAR="$tmp/fakebrew/Cellar/lead/0.1.0/bin"
mkdir -p "$FAKECELLAR"
cp "$tmp/lead" "$FAKECELLAR/lead"
PATH="$tmp/fakebrew/bin:$PATH" "$FAKECELLAR/lead" update 2>&1 | grep -q "brew upgrade" \
  || fail "brew-managed update missing guidance"

[ "$HOME" = "$tmp/home" ] || fail "HOME isolation broken"

echo "setup/doctor/update behavioral checks passed"
