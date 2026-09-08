#!/usr/bin/env bash
# An update that collapses a pinned input into a `follows` must still report,
# and must not end the run before the new lock is committed.
#
# Update paths are discovered from the pre-update lock, so a nested input that
# the update turns into a redirect is still asked about afterwards. In a lock a
# pinned edge is the node's name as a string and a `follows` is an absolute path
# from root as an array, so a walker that assumes the string shape dies on the
# array with "Cannot index object with array" — after Nix has written the lock
# and before the commit, which is exactly the worst place to stop.
set -euo pipefail

ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
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

# The shape allod/archetypes carried before it redirected its nexus input's vm:
# two distinct vm nodes, one reached through nexus.
cat > "$REPO/flake.lock" <<'EOF'
{
  "nodes": {
    "root": {
      "inputs": {
        "archetypes": "archetypes"
      }
    },
    "archetypes": {
      "inputs": {"nexus": "nexus", "vm": "vm"},
      "locked": {"rev": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}
    },
    "nexus": {
      "inputs": {"vm": "vm_2"},
      "locked": {"rev": "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}
    },
    "vm": {
      "locked": {"rev": "cccccccccccccccccccccccccccccccccccccccc"}
    },
    "vm_2": {
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
    printf 'master\n'
    ;;
  "diff --quiet"|"diff --cached --quiet"|"diff --quiet -- flake.lock")
    exit 0
    ;;
  "remote get-url origin")
    printf 'ssh://git@forge.anarch.diy:2222/acme/app.git\n'
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
  *)
    echo "unexpected git invocation: $*" >&2
    exit 1
    ;;
esac
EOF

# The update collapses nexus's own vm onto the root-level one, exactly as
# `nexus.inputs.vm.follows = "vm"` does: the vm_2 node disappears and the edge
# that pointed at it becomes the array ["archetypes","vm"].
cat > "$TMP/bin/nix" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
printf 'nix\t%s\n' "$*" >> "$MOCK_LOG"

case "$1 $2" in
  "flake update")
    shift 2
    flake=""
    output=""
    while [[ $# -gt 0 ]]; do
      case "$1" in
        --flake) flake="$2"; shift 2 ;;
        --output-lock-file) output="$2"; shift 2 ;;
        *) shift ;;
      esac
    done
    target="${output:-$flake/flake.lock}"
    jq '
      .nodes.nexus.inputs.vm = ["archetypes", "vm"]
      | del(.nodes.vm_2)
      | .nodes.vm.locked.rev = "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"
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

status=0
output=$(bash "$ROOT/flake/flake-update-cascade" vm --dry-run 2>&1) || status=$?

[[ "$status" == 0 ]] ||
  fail "the run exited ${status} on a lock whose input collapsed to a follows"
[[ "$output" != *"Cannot index object with array"* ]] ||
  fail "the rev walker indexed .nodes with a follows array"

# The still-pinned path reports normally.
[[ "$output" == *"archetypes/vm: ccccccc → eeeeeee"* ]] ||
  fail "the root-level vm change was not reported: $output"

# The collapsed path resolves through the follows to the node it now redirects
# to, rather than ending the run.
[[ "$output" == *"archetypes/nexus/vm: ddddddd → eeeeeee"* ]] ||
  fail "the collapsed nexus vm path did not resolve through its follows: $output"

echo "flake-update-cascade follows-collapse tests passed"
