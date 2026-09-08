# Sourced by every flake-update-cascade suite after ROOT is set. Provides
# run_cascade, the one way a suite invokes the tool.
#
#   CASCADE_UNDER_TEST  the program the suite exercises. Unset, the suite runs
#                       the Bash oracle at flake/flake-update-cascade.
#   CASCADE_PARITY=1    with CASCADE_UNDER_TEST set, every invocation runs the
#                       oracle first and the program under test second, on
#                       the same fixture, and fails unless stdout, stderr, exit
#                       status, the mock command log, and the fixture tree
#                       under $HOME/work come out identical. The suite then
#                       goes on to make its own assertions against the program
#                       under test.
#
# The fixture is restored between the two runs from a snapshot taken before
# the first, and the command log is rewound to its pre-run content, so each
# program sees the same starting state; a mutating run's effects on the tree
# are part of what is compared.

CASCADE_ORACLE="$ROOT/flake/flake-update-cascade"

cascade_invoke() {
  if [[ -z "${CASCADE_UNDER_TEST:-}" ]]; then
    bash "$CASCADE_ORACLE" "$@"
  else
    "$CASCADE_UNDER_TEST" "$@"
  fi
}

cascade_tree_manifest() {
  if [[ -d "$HOME/work" ]]; then
    (cd "$HOME" && find work -type f -print0 | sort -z | xargs -0 sha256sum)
    (cd "$HOME" && find work -type d | sort)
  fi
}

run_cascade() {
  if [[ -z "${CASCADE_PARITY:-}" || -z "${CASCADE_UNDER_TEST:-}" ]]; then
    cascade_invoke "$@"
    return
  fi

  local scratch snapshot log_before status_oracle status_test mismatch=""
  scratch=$(mktemp -d)
  snapshot="$scratch/work.tar"
  log_before="$scratch/log.before"
  tar -C "$HOME" -cf "$snapshot" work 2>/dev/null || : > "$snapshot"
  if [[ -n "${MOCK_LOG:-}" && -f "$MOCK_LOG" ]]; then
    cp "$MOCK_LOG" "$log_before"
  fi

  status_oracle=0
  bash "$CASCADE_ORACLE" "$@" >"$scratch/oracle.out" 2>"$scratch/oracle.err" \
    || status_oracle=$?
  [[ -n "${MOCK_LOG:-}" && -f "$MOCK_LOG" ]] && cp "$MOCK_LOG" "$scratch/oracle.log"
  cascade_tree_manifest > "$scratch/oracle.tree"

  rm -rf "$HOME/work"
  [[ -s "$snapshot" ]] && tar -C "$HOME" -xf "$snapshot"
  if [[ -n "${MOCK_LOG:-}" ]]; then
    if [[ -f "$log_before" ]]; then cp "$log_before" "$MOCK_LOG"; else rm -f "$MOCK_LOG"; fi
  fi

  status_test=0
  "$CASCADE_UNDER_TEST" "$@" >"$scratch/test.out" 2>"$scratch/test.err" \
    || status_test=$?
  [[ -n "${MOCK_LOG:-}" && -f "$MOCK_LOG" ]] && cp "$MOCK_LOG" "$scratch/test.log"
  cascade_tree_manifest > "$scratch/test.tree"

  local name
  for name in out err log tree; do
    if [[ -f "$scratch/oracle.$name" || -f "$scratch/test.$name" ]]; then
      if ! diff -u "$scratch/oracle.$name" "$scratch/test.$name" >"$scratch/$name.diff" 2>&1; then
        mismatch+=$'\n'"--- $name differs (oracle vs under test):"$'\n'"$(cat "$scratch/$name.diff")"
      fi
    fi
  done
  if [[ "$status_oracle" != "$status_test" ]]; then
    mismatch+=$'\n'"--- exit status differs: oracle $status_oracle, under test $status_test"
  fi

  if [[ -n "$mismatch" ]]; then
    printf 'PARITY MISMATCH for: flake-update-cascade %s%s\n' "$*" "$mismatch" >&2
    rm -rf "$scratch"
    return 99
  fi

  cat "$scratch/test.out"
  cat "$scratch/test.err" >&2
  rm -rf "$scratch"
  return "$status_test"
}
