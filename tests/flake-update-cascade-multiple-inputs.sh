#!/usr/bin/env bash
set -euo pipefail

ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT

export HOME="$TMP/home"
export MOCK_LOG="$TMP/commands.log"
REPO="$HOME/work/app"
mkdir -p "$HOME/work/.git" "$REPO/.git" "$HOME/.config/git" "$TMP/bin"
: > "$REPO/.git/HEAD"
: > "$HOME/.config/git/active-pr-branches"
: > "$HOME/.config/git/protected-branches"
: > "$MOCK_LOG"

cat > "$REPO/flake.lock" <<'EOF'
{
  "nodes": {
    "root": {
      "inputs": {
        "allod-tools": "allod-tools",
        "vm": "vm",
        "nixpkgs": ["vm", "nixpkgs"]
      }
    },
    "allod-tools": {
      "locked": {"rev": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}
    },
    "vm": {
      "inputs": {"nixpkgs": "nixpkgs"},
      "locked": {"rev": "cccccccccccccccccccccccccccccccccccccccc"}
    },
    "nixpkgs": {
      "locked": {"rev": "dddddddddddddddddddddddddddddddddddddddd"}
    }
  }
}
EOF

cat > "$TMP/bin/git" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail

[[ "$1" == "-C" ]] || { echo "unexpected git invocation: $*" >&2; exit 1; }
dir="$2"
shift 2
printf 'git\t%s\n' "$*" >> "$MOCK_LOG"

case "$*" in
  "rev-parse --show-toplevel")
    [[ -f "$dir/.git/HEAD" ]] || exit 1
    printf '%s\n' "$dir"
    ;;
  "symbolic-ref refs/remotes/origin/HEAD")
    printf 'refs/remotes/origin/master\n'
    ;;
  "branch --show-current")
    [[ "${MOCK_MODE:-dry-run}" == preflight-fail ]] \
      && printf 'feature\n' \
      || printf 'master\n'
    ;;
  "diff --quiet"|"diff --cached --quiet")
    exit 0
    ;;
  "diff --quiet -- flake.lock")
    [[ "${MOCK_MODE:-dry-run}" == direct ]] && exit 1 || exit 0
    ;;
  "rev-parse @{u}")
    printf 'origin/master\n'
    ;;
  "rev-list HEAD...@{u} --count")
    printf '0\n'
    ;;
  "pull")
    exit 0
    ;;
  "add flake.lock"|"commit -m flake.lock: update nixpkgs, allod-tools"|"push")
    exit 0
    ;;
  *)
    echo "unexpected git invocation: $*" >&2
    exit 1
    ;;
esac
EOF

cat > "$TMP/bin/nix" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
printf 'nix\t%s\n' "$*" >> "$MOCK_LOG"

case "$1 $2" in
  "flake update")
    shift 2
    inputs=()
    flake=""
    output=""
    while [[ $# -gt 0 ]]; do
      case "$1" in
        --flake) flake="$2"; shift 2 ;;
        --output-lock-file) output="$2"; shift 2 ;;
        *) inputs+=("$1"); shift ;;
      esac
    done
    [[ "${inputs[*]}" == "allod-tools vm/nixpkgs" ]] || {
      echo "unexpected combined inputs: ${inputs[*]}" >&2
      exit 1
    }
    target="${output:-$flake/flake.lock}"
    jq '
      .nodes["allod-tools"].locked.rev = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
      | .nodes.nixpkgs.locked.rev = "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"
    ' "$flake/flake.lock" > "$target.tmp"
    mv "$target.tmp" "$target"
    ;;
  "flake metadata")
    exit 0
    ;;
  *)
    echo "unexpected nix invocation: $*" >&2
    exit 1
    ;;
esac
EOF

chmod +x "$TMP/bin/git" "$TMP/bin/nix"
export PATH="$TMP/bin:$PATH"

fail() {
  echo "FAIL: $*" >&2
  exit 1
}

before=$(sha256sum "$REPO/flake.lock")
output=$(bash "$ROOT/flake-update-cascade" nixpkgs allod-tools --dry-run)
after=$(sha256sum "$REPO/flake.lock")

[[ "$before" == "$after" ]] || fail "dry-run changed flake.lock"
[[ "$output" == *"allod-tools: aaaaaaa → bbbbbbb"* ]] ||
  fail "direct root input change missing"
[[ "$output" == *"vm/nixpkgs: ddddddd → eeeeeee"* ]] ||
  fail "nested input change missing"
[[ "$(grep -c $'^nix\tflake update' "$MOCK_LOG")" == 1 ]] ||
  fail "expected exactly one combined Nix update"
grep -Fq \
  $'nix\tflake update allod-tools vm/nixpkgs --flake '"$REPO" \
  "$MOCK_LOG" || fail "combined update paths were not passed to Nix"

: > "$MOCK_LOG"
export MOCK_MODE=direct
output=$(bash "$ROOT/flake-update-cascade" nixpkgs allod-tools)
[[ "$output" == *"committed and pushed"* ]] ||
  fail "direct update did not complete"
[[ "$(grep -c $'^nix\tflake update' "$MOCK_LOG")" == 1 ]] ||
  fail "direct mode did not use exactly one combined Nix update"
grep -Fq $'git\tcommit -m flake.lock: update nixpkgs, allod-tools' "$MOCK_LOG" ||
  fail "direct mode did not create one combined commit"

: > "$MOCK_LOG"
export MOCK_MODE=preflight-fail
if bash "$ROOT/flake-update-cascade" nixpkgs allod-tools --dry-run >/dev/null 2>&1; then
  fail "pre-flight failure returned success"
fi
[[ "$(grep -c $'^nix\t' "$MOCK_LOG" || true)" == 0 ]] ||
  fail "pre-flight failure invoked Nix"

echo "flake-update-cascade multiple-input tests passed"
