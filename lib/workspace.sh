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

  local sub
  for sub in "$dir"/*/; do
    [[ -d "$sub" ]] || continue
    workspace_collect_repos "$work_dir" "$sub"
  done
}

workspace_repo_default_branch() {
  local dir="$1"
  git -C "$dir" symbolic-ref refs/remotes/origin/HEAD 2>/dev/null \
    | sed 's|refs/remotes/origin/||' \
    || echo "master"
}
