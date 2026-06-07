#!/usr/bin/env bash
set -euo pipefail

ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT

export HOME="$TMP/home"
export MOCK_LOG="$TMP/git.log"
mkdir -p "$HOME/work/group/nested" "$TMP/bin"

for repo in clean dirty local unpushed switched pull-fail group/nested/repo; do
  mkdir -p "$HOME/work/$repo/.git"
done

cat > "$TMP/bin/git" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail

[[ "$1" == "-C" ]] || { echo "expected git -C" >&2; exit 1; }
repo=$(basename "$2")
shift 2
printf '%s\t%s\n' "$repo" "$*" >> "$MOCK_LOG"
command="$*"

case "$command" in
  "symbolic-ref refs/remotes/origin/HEAD")
    if [[ "$repo" == local ]]; then
      exit 1
    fi
    printf 'refs/remotes/origin/master\n'
    ;;
  "branch --show-current")
    case "$repo" in
      local|unpushed|switched) printf 'feature\n' ;;
      *) printf 'master\n' ;;
    esac
    ;;
  "diff --quiet")
    if [[ "$repo" == dirty ]]; then
      exit 1
    fi
    ;;
  "diff --cached --quiet")
    ;;
  "rev-parse --abbrev-ref @{u}")
    if [[ "$repo" == local ]]; then
      exit 1
    fi
    printf 'origin/feature\n'
    ;;
  "rev-list HEAD...@{u} --count")
    if [[ "$repo" == unpushed ]]; then
      printf '2\n'
    else
      printf '0\n'
    fi
    ;;
  "checkout master")
    ;;
  "pull")
    if [[ "$repo" == pull-fail ]]; then
      echo "network unavailable"
      exit 1
    elif [[ "$repo" == switched || "$repo" == repo ]]; then
      echo "Updating 1111111..2222222"
    else
      echo "Already up to date."
    fi
    ;;
  *)
    echo "unexpected git invocation for $repo: $*" >&2
    exit 1
    ;;
esac
EOF
chmod +x "$TMP/bin/git"
export PATH="$TMP/bin:$PATH"

output=$("$ROOT/pull-all")

contains() {
  [[ "$output" == *"$1"* ]] || {
    echo "FAIL: output missing: $1" >&2
    printf '%s\n' "$output" >&2
    exit 1
  }
}

contains "clean                     up to date [master]"
contains "dirty                     skipped    [dirty working tree]"
contains "local                     skipped    [on feature — no remote tracking branch]"
contains "unpushed                  skipped    [on feature — 2 unpushed commits]"
contains "switched                  pulled     [master]"
contains "pull-fail                 error      [pull failed: network unavailable]"
contains "group/nested/repo         pulled     [master]"

grep -Fxq $'switched\tcheckout master' "$MOCK_LOG"
grep -Fxq $'switched\tpull' "$MOCK_LOG"
grep -Fxq $'repo\tpull' "$MOCK_LOG"

if grep -Eq $'^(dirty|local|unpushed)\tpull$' "$MOCK_LOG"; then
  echo "FAIL: skipped repository was pulled" >&2
  exit 1
fi

echo "pull-all tests passed"
