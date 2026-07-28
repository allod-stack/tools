WORK_DIR="${WORK_DIR:-${HOME}/work}"

workspace_is_repo_root() {
  local dir="$1" top
  [[ -e "$dir/.git" ]] || return 1
  top=$(git -C "$dir" rev-parse --show-toplevel 2>/dev/null) || return 1
  [[ "$top" == "$dir" ]]
}

workspace_collect_repos() {
  local work_dir="${1%/}"
  local dir="${2:-$work_dir}"
  dir="${dir%/}"

  if workspace_is_repo_root "$dir"; then
    printf '%s\n' "${dir#"${work_dir}/"}"
    return
  fi

  local sub base
  for sub in "$dir"/*/ "$dir"/.[!.]*/ "$dir"/..?*/; do
    [[ -d "$sub" ]] || continue
    sub="${sub%/}"
    base="${sub##*/}"
    [[ "$base" == ".git" ]] && continue
    if [[ "$base" == .* ]] && ! workspace_is_repo_root "$sub"; then
      continue
    fi
    workspace_collect_repos "$work_dir" "$sub"
  done
}

# Print each linked worktree of a repo as "<absolute-path><TAB><branch>", with
# an empty branch for a detached HEAD. Enumeration comes from git rather than a
# filesystem glob, so it stays correct wherever worktrees are sited; the first
# record `worktree list --porcelain` emits is the main checkout itself and is
# skipped. Worktrees whose directory is gone (prunable) are skipped too.
#
# This is deliberately separate from workspace_collect_repos, whose output stays
# exactly the registry checkouts: pull-all, flake-status, and
# flake-update-cascade all mutate based on that output.
workspace_collect_worktrees() {
  local repo_dir="${1%/}"
  local line path='' branch='' record=0

  # The trailing printf guarantees a terminating blank line for the last record
  # even if git ever stops emitting one.
  while IFS= read -r line; do
    case "$line" in
      'worktree '*)
        path="${line#worktree }"
        branch=''
        record=$((record + 1))
        ;;
      'branch refs/heads/'*)
        branch="${line#branch refs/heads/}"
        ;;
      '')
        if (( record > 1 )) && [[ -n "$path" ]] && [[ -d "$path" ]]; then
          printf '%s\t%s\n' "$path" "$branch"
        fi
        path=''
        branch=''
        ;;
    esac
  done < <(git -C "$repo_dir" worktree list --porcelain 2>/dev/null; printf '\n')
}

workspace_repo_default_branch() {
  local dir="$1"
  git -C "$dir" symbolic-ref refs/remotes/origin/HEAD 2>/dev/null \
    | sed 's|refs/remotes/origin/||' \
    || echo "master"
}
