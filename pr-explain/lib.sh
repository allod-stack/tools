# shellcheck shell=bash
# Shared library for the pr-explain tools (explain, validate-report).
# Sourced with `set -euo pipefail` already in effect in the caller.
#
# This file is a thin loader: it holds no functions of its own past resolving
# the library directory, and instead sources pr-explain/lib/*.sh in the
# dependency order those files require (common primitives first, the report
# driver last). Split by concern so a change to a report validator stops
# rendering as a change to asset emission; see each file's own header for
# what it holds.

PR_EXPLAIN_LIB_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"

# shellcheck source=pr-explain/lib/common.sh
source "$PR_EXPLAIN_LIB_DIR/lib/common.sh"
# shellcheck source=pr-explain/lib/assets.sh
source "$PR_EXPLAIN_LIB_DIR/lib/assets.sh"
# shellcheck source=pr-explain/lib/fragments.sh
source "$PR_EXPLAIN_LIB_DIR/lib/fragments.sh"
# shellcheck source=pr-explain/lib/report-validators.sh
source "$PR_EXPLAIN_LIB_DIR/lib/report-validators.sh"
# shellcheck source=pr-explain/lib/report.sh
source "$PR_EXPLAIN_LIB_DIR/lib/report.sh"
