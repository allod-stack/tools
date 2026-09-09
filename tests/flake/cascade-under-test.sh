# Sourced by every flake-update-cascade suite after ROOT and TMP are set.
# Provides run_cascade, the one way a suite invokes the tool.
#
#   CASCADE_UNDER_TEST  the program the suite exercises. nix flake check sets
#                       it to the packaged program. Unset, the suite builds
#                       cmd/flake-update-cascade once into TMP with the Go
#                       toolchain on PATH, so the suite's own cleanup removes
#                       it. VCS stamping is off because the suites put a mock
#                       git on PATH before the first run.

if [[ -z "${CASCADE_UNDER_TEST:-}" ]]; then
  CASCADE_UNDER_TEST="${TMP:?cascade-under-test.sh: set TMP before sourcing}/flake-update-cascade"
  if ! (cd "$ROOT" && go build -buildvcs=false -o "$CASCADE_UNDER_TEST" ./cmd/flake-update-cascade); then
    echo "cascade-under-test.sh: go build failed; set CASCADE_UNDER_TEST to a built program" >&2
    exit 1
  fi
fi
export CASCADE_UNDER_TEST

run_cascade() {
  "$CASCADE_UNDER_TEST" "$@"
}
