#!/usr/bin/env bash
# issue #197: `lead enable` はブランチ保護の状態も報告し、不足時は
# `--check` を失敗させること (dispatchのPreflightが要求するため)。
# 振る舞いのみ検証: 終了ステータス、出力行、gh argv記録 (ソースgrepなし)。
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
fail() { echo "FAIL: $*" >&2; exit 1; }

CGO_ENABLED=0 go build -o "$tmp/lead" ./cmd/lead || fail "go build failed"

export HOME="$tmp/home"
mkdir -p "$HOME"

# --- stateful dummy gh: labels from $GH_LABEL_STATE; protection from $PROT_FLAG ---
mkdir -p "$tmp/bin"
cat > "$tmp/bin/gh" <<'EOF'
#!/usr/bin/env bash
printf '<%s>\n' "$@" >> "$GH_ARG_LOG"
if [ "$1 $2" = "label list" ]; then
  first=1
  printf '['
  while IFS= read -r name; do
    [ -n "$name" ] || continue
    [ "$first" = 1 ] || printf ','
    first=0
    printf '{"name":"%s"}' "$name"
  done < "$GH_LABEL_STATE"
  printf ']'
  exit 0
fi
if [ "$1 $2" = "label create" ]; then
  printf '%s\n' "$3" >> "$GH_LABEL_STATE"
  exit 0
fi
if [ "$1 $2" = "api repos/o/r" ]; then
  case "$*" in
    *"--jq .default_branch"*) printf 'main' ;;
    *"--jq .allow_auto_merge"*) printf 'true' ;;
    *) echo "unexpected gh api: $@" >&2; exit 3 ;;
  esac
  exit 0
fi
if [ "$1 $2" = "api repos/o/r/branches/main/protection" ]; then
  if [ -f "$PROT_FLAG" ]; then
    printf '{"required_status_checks":{"contexts":["test"]},"required_pull_request_reviews":{}}'
  else
    echo "gh: Branch not protected (HTTP 404)" >&2
    exit 1
  fi
  exit 0
fi
if [ "$1 $2" = "api repos/o/r/rules/branches/main" ]; then
  printf '[]'
  exit 0
fi
echo "unexpected gh call: $@" >&2
exit 3
EOF
chmod +x "$tmp/bin/gh"
export PATH="$tmp/bin:$PATH"
export GH_ARG_LOG="$tmp/gh-args.log"
export GH_LABEL_STATE="$tmp/gh-labels.txt"
export PROT_FLAG="$tmp/protected"
: > "$GH_ARG_LOG"
printf 'needs-review\nready\nblocked\n' > "$GH_LABEL_STATE"

proj="$tmp/project"
mkdir -p "$proj"
cd "$proj"
git init >/dev/null || fail "git init failed"
git config user.email "test@example.com"
git config user.name "Test"
git remote add origin "https://github.com/o/r.git"

# --- 1. dry-run reports missing protection with guidance ---
out="$("$tmp/lead" enable --dry-run)" || fail "enable dry-run failed"
case "$out" in *"protection/branch-protection: missing"*) ;; *) fail "dry-run missing protection line: $out";; esac
case "$out" in *"protection/required-checks: missing"*) ;; *) fail "dry-run missing required-checks line: $out";; esac
case "$out" in *"Next:"*"protect"*) ;; *) fail "dry-run missing protection guidance: $out";; esac

# --- 2. install locally, then --check fails on protection ---
out="$("$tmp/lead" enable --yes)" || fail "enable apply failed: $out"
out="$("$tmp/lead" enable --check 2>&1)" && fail "enable --check succeeded without protection"
case "$out" in *"protection/branch-protection: missing"*) ;; *) fail "check missing protection line: $out";; esac

# --- 3. protected repo: ok lines and --check passes ---
: > "$PROT_FLAG"
: > "$GH_ARG_LOG"
out="$("$tmp/lead" enable --dry-run)" || fail "enable dry-run failed on protected repo: $out"
case "$out" in *"protection/branch-protection: ok"*) ;; *) fail "protected repo missing ok line: $out";; esac
case "$out" in *"protection/required-checks: ok"*) ;; *) fail "protected repo missing checks ok line: $out";; esac
grep -q 'branches/main/protection' "$GH_ARG_LOG" || fail "protection api not called: $(cat "$GH_ARG_LOG")"
out="$("$tmp/lead" enable --check)" || fail "enable --check failed on protected repo: $out"

# --- 4. no origin remote: protection skipped, gh api never called ---
git remote remove origin || fail "git remote remove failed"
: > "$GH_ARG_LOG"
out="$("$tmp/lead" enable --check)" || fail "enable --check without remote failed: $out"
case "$out" in *"protection: skip"*) ;; *) fail "no-remote run missing protection skip: $out";; esac
grep -q 'branches/main/protection' "$GH_ARG_LOG" && fail "protection api called without a remote"

echo "enable protection checks passed"
