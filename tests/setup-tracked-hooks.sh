#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
script="$repo_root/setup-tracked-hooks"

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

export HOME="$tmp/home"
mkdir -p "$HOME/work/allod/inventory/scripts"

counter_file="$tmp/test_count"
printf '0' > "$counter_file"

pass() {
  local n=$(( $(<"$counter_file") + 1 ))
  printf '%d' "$n" > "$counter_file"
  printf 'PASS %d - %s\n' "$n" "$1"
}

fail() {
  local n=$(( $(<"$counter_file") + 1 ))
  printf '%d' "$n" > "$counter_file"
  printf 'FAIL %d - %s\n' "$n" "$1" >&2
  shift
  printf '%s\n' "$@" >&2
  exit 1
}

make_repo() {
  local dir="$1"
  mkdir -p "$dir"
  git -C "$dir" init >/dev/null 2>&1
  git -C "$dir" config user.name "Test"
  git -C "$dir" config user.email "test@example.invalid"
  git -C "$dir" commit --allow-empty -m initial >/dev/null 2>&1
}

write_registry() {
  cat > "$HOME/work/allod/inventory/scripts/repositories.json"
}

# --- Test 1: creates .hookspath for repo with hookspath field ---

write_registry <<'REG'
{
  "repositories": {
    "cdk-upstream": {
      "source": "git",
      "remote": "https://github.com/cashubtc/cdk.git",
      "checkout": "cdk",
      "hookspath": "misc/git-hooks"
    }
  }
}
REG

make_repo "$HOME/work/cdk"
bash "$script" >/dev/null

if [[ -f "$HOME/work/cdk/.hookspath" ]] && [[ "$(cat "$HOME/work/cdk/.hookspath")" == "misc/git-hooks" ]]; then
  pass "creates .hookspath with correct content"
else
  fail "creates .hookspath with correct content"
fi

# --- Test 2: adds .hookspath to .git/info/exclude ---

if grep -qxF '.hookspath' "$HOME/work/cdk/.git/info/exclude"; then
  pass ".hookspath added to .git/info/exclude"
else
  fail ".hookspath added to .git/info/exclude"
fi

# --- Test 3: idempotent - running again doesn't duplicate exclude entry ---

bash "$script" >/dev/null
count=$(grep -cxF '.hookspath' "$HOME/work/cdk/.git/info/exclude")
if [[ "$count" -eq 1 ]]; then
  pass "idempotent: no duplicate exclude entry"
else
  fail "idempotent: no duplicate exclude entry" "found $count entries"
fi

# --- Test 4: updates stale .hookspath ---

write_registry <<'REG'
{
  "repositories": {
    "cdk-upstream": {
      "source": "git",
      "remote": "https://github.com/cashubtc/cdk.git",
      "checkout": "cdk",
      "hookspath": ".hooks"
    }
  }
}
REG

bash "$script" >/dev/null
if [[ "$(cat "$HOME/work/cdk/.hookspath")" == ".hooks" ]]; then
  pass "updates stale .hookspath"
else
  fail "updates stale .hookspath"
fi

# --- Test 5: skips repos without hookspath field ---

write_registry <<'REG'
{
  "repositories": {
    "profiles": {
      "source": "forge",
      "remote": "profiles",
      "checkout": "allod/profiles"
    }
  }
}
REG

make_repo "$HOME/work/allod/profiles"
bash "$script" >/dev/null
if [[ ! -f "$HOME/work/allod/profiles/.hookspath" ]]; then
  pass "skips repos without hookspath field"
else
  fail "skips repos without hookspath field"
fi

# --- Test 6: skips repos not cloned ---

write_registry <<'REG'
{
  "repositories": {
    "missing": {
      "source": "forge",
      "remote": "missing",
      "checkout": "missing-repo",
      "hookspath": ".hooks"
    }
  }
}
REG

bash "$script" >/dev/null
if [[ ! -f "$HOME/work/missing-repo/.hookspath" ]]; then
  pass "skips repos not cloned"
else
  fail "skips repos not cloned"
fi

# --- Test 7: exits cleanly when registry is missing ---

rm "$HOME/work/allod/inventory/scripts/repositories.json"
if bash "$script" 2>/dev/null; then
  pass "exits cleanly when registry is missing"
else
  fail "exits cleanly when registry is missing"
fi

echo "$(<"$counter_file") setup-tracked-hooks tests passed."
