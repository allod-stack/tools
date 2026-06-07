#!/usr/bin/env bash
set -euo pipefail

ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT

export HOME="$TMP/home"
mkdir -p "$HOME/work/group" "$TMP/bin"

write_lock() {
  local dir="$1" rev="$2" include_demo="$3"
  mkdir -p "$dir/.git"
  if [[ "$include_demo" == true ]]; then
    cat > "$dir/flake.lock" <<EOF
{
  "nodes": {
    "root": {"inputs": {"demo": "demo-node", "followed": ["demo", "nixpkgs"]}},
    "demo-node": {
      "locked": {"rev": "$rev", "lastModified": 0},
      "original": {"type": "github", "owner": "acme", "repo": "demo"}
    }
  }
}
EOF
  else
    cat > "$dir/flake.lock" <<'EOF'
{"nodes":{"root":{"inputs":{"followed":["other","nixpkgs"]}}}}
EOF
  fi
}

REV_A=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
REV_B=bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb
write_lock "$HOME/work/alpha" "$REV_A" true
write_lock "$HOME/work/beta" "$REV_B" true
write_lock "$HOME/work/group/delta" "$REV_A" true
write_lock "$HOME/work/gamma" "$REV_A" false
mkdir -p "$HOME/work/no-lock/.git"

cat > "$TMP/bin/git" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail

if [[ "$1" == "ls-remote" ]]; then
  printf 'cccccccccccccccccccccccccccccccccccccccc\tHEAD\n'
  exit 0
fi

[[ "$1" == "-C" ]] || { echo "unexpected git invocation: $*" >&2; exit 1; }
repo=$(basename "$2")
shift 2
command="$*"

case "$command" in
  "symbolic-ref refs/remotes/origin/HEAD")
    printf 'refs/remotes/origin/master\n'
    ;;
  "branch --show-current")
    [[ "$repo" == beta ]] && printf 'feature\n' || printf 'master\n'
    ;;
  "diff --cached --quiet")
    exit 0
    ;;
  "diff --quiet")
    [[ "$repo" == beta ]] && exit 1 || exit 0
    ;;
  "rev-list HEAD...@{u} --count")
    [[ "$repo" == beta ]] && printf '2\n' || printf '0\n'
    ;;
  *)
    echo "unexpected git invocation for $repo: $command" >&2
    exit 1
    ;;
esac
EOF
chmod +x "$TMP/bin/git"
export PATH="$TMP/bin:$PATH"

fail() {
  echo "FAIL: $*" >&2
  exit 1
}

output=$(bash "$ROOT/flake-status" demo)
[[ "$output" == *"demo — INCONSISTENT (1 repo differs)"* ]] || fail "inconsistent header missing"
[[ "$output" == *"alpha                 aaaaaaa  1970-01-01"* ]] || fail "alpha pin missing"
[[ "$output" == *"beta                  bbbbbbb  1970-01-01"* ]] || fail "beta pin missing"
[[ "$output" == *"[on branch feature, not master]"* ]] || fail "branch warning missing"
[[ "$output" == *"[dirty: unstaged changes]"* ]] || fail "dirty warning missing"
[[ "$output" == *"[2 unpushed commits]"* ]] || fail "unpushed warning missing"
[[ "$output" == *"← stale"* ]] || fail "stale marker missing"
[[ "$output" == *"gamma                 (not an input)"* ]] || fail "not-input row missing"
[[ "$output" != *"no-lock"* ]] || fail "repo without lock should be omitted"

upstream=$(bash "$ROOT/flake-status" demo --check-upstream)
[[ "$upstream" == *"upstream: ccccccc (local pins are behind)"* ]] || fail "upstream comparison missing"

all=$(bash "$ROOT/flake-status")
[[ "$all" == *"==> alpha"* ]] || fail "all-input alpha heading missing"
[[ "$all" == *"demo                  aaaaaaa  1970-01-01"* ]] || fail "all-input row missing"
[[ "$all" != *"followed"* ]] || fail "follows input should be omitted"

help=$(bash "$ROOT/flake-status" --help)
[[ "$help" == *"Usage: flake-status"* ]] || fail "help missing"

if output=$(bash "$ROOT/flake-status" --invalid 2>&1); then
  fail "unknown option succeeded"
fi
[[ "$output" == *"unknown option: --invalid"* ]] || fail "unknown option message missing"

if output=$(bash "$ROOT/flake-status" one two 2>&1); then
  fail "extra argument succeeded"
fi
[[ "$output" == *"unexpected argument: two"* ]] || fail "extra argument message missing"

echo "flake-status tests passed"
