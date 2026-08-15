#!/usr/bin/env bash
# shellcheck source=/dev/null
source "$(dirname "${BASH_SOURCE[0]}")/testlib.sh"

new_case help
capture "$ALLOD" pr explain --help
assert_success "shows PR explain help"
assert_contains "$CAPTURE_OUTPUT" "--codex" "documents Codex as an explicit consent choice"
assert_contains "$CAPTURE_OUTPUT" "--claude" "documents Claude as an explicit consent choice"
assert_contains "$CAPTURE_OUTPUT" "--output" "documents the required output path"
assert_contains "$CAPTURE_OUTPUT" "--no-repair" "documents the single-call option"
assert_contains "$CAPTURE_OUTPUT" "at most two provider calls" \
  "documents the bounded cost of a repair pass"

new_case requires-output
capture_explain "$TEST_TMP/checkout" 7 --codex -R acme/widget
assert_failure "requires an explicit output path"
assert_contains "$CAPTURE_OUTPUT" "--output" "diagnoses the missing output path"
assert_no_runner "rejects a missing output path before provider invocation"

new_case requires-provider
capture_explain "$TEST_TMP/checkout" 7 -R acme/widget --output "$CASE_DIR/output/report.html"
assert_failure "requires an explicit provider disclosure choice"
assert_contains "$CAPTURE_OUTPUT" "--codex" "diagnoses the missing provider choice"
assert_no_runner "rejects a missing provider before resolving source for it"

new_case exclusive-provider
capture_explain "$TEST_TMP/checkout" 7 --codex --claude -R acme/widget \
  --output "$CASE_DIR/output/report.html"
assert_failure "rejects multiple provider choices"
assert_no_runner "does not choose a provider on the operator's behalf"

# Seed both API-style credentials/overrides and subscription-CLI state. The
# former must not cross the runner boundary; each runner retains only its own
# subscription-login controls and must not receive the other provider's state.
export OPENAI_API_KEY="openai-api-key-must-not-leak"
export ANTHROPIC_API_KEY="anthropic-api-key-must-not-leak"
export OPENAI_BASE_URL="https://override.example.invalid"
export ANTHROPIC_BASE_URL="https://override.example.invalid"
export ANTHROPIC_AUTH_TOKEN="anthropic-token-must-not-leak"
export CLAUDE_CODE_USE_BEDROCK=1
export CLAUDE_CODE_USE_VERTEX=1
export CLAUDE_CODE_USE_FOUNDRY=1
export FORGEJO_TOKEN="forge-token-must-not-leak"
export FORGE_TOKEN_FILE="$TEST_TMP/real-forge-token"
export CODEX_HOME="$TEST_TMP/codex-subscription"
export CODEX_ACCESS_TOKEN="codex-subscription-token"
export ANTHROPIC_CONFIG_DIR="$TEST_TMP/claude-subscription"
export CLAUDE_CODE_OAUTH_TOKEN="claude-subscription-token"
export CLAUDE_CODE_OAUTH_REFRESH_TOKEN="claude-subscription-refresh-token"
mkdir -p "$CODEX_HOME" "$ANTHROPIC_CONFIG_DIR"

deny_names=(
  OPENAI_API_KEY OPENAI_BASE_URL OPENAI_API_BASE OPENAI_ORGANIZATION OPENAI_PROJECT
  CODEX_API_KEY CODEX_OSS_BASE_URL CODEX_URL CODEX_REFRESH_TOKEN_URL_OVERRIDE
  CODEX_CONNECTORS_TOKEN
  CODEX_REVOKE_TOKEN_URL_OVERRIDE ANTHROPIC_API_KEY ANTHROPIC_AUTH_TOKEN
  ANTHROPIC_BASE_URL ANTHROPIC_CUSTOM_HEADERS ANTHROPIC_MODEL ANTHROPIC_IDENTITY_TOKEN
  ANTHROPIC_DEFAULT_HAIKU_MODEL ANTHROPIC_DEFAULT_SONNET_MODEL
  ANTHROPIC_DEFAULT_OPUS_MODEL ANTHROPIC_SMALL_FAST_MODEL CLAUDE_CODE_USE_BEDROCK
  CLAUDE_CODE_USE_VERTEX CLAUDE_CODE_USE_FOUNDRY CLAUDE_CODE_USE_ANTHROPIC_AWS
  CLAUDE_CODE_USE_ANTHROPIC_GOOGLE_CLOUD CLAUDE_CODE_USE_GATEWAY
  CLAUDE_CODE_API_BASE_URL ANTHROPIC_BEDROCK_BASE_URL ANTHROPIC_AWS_API_KEY
  ANTHROPIC_AWS_BASE_URL AWS_BEARER_TOKEN_BEDROCK AWS_ACCESS_KEY_ID
  AWS_SECRET_ACCESS_KEY AWS_SESSION_TOKEN AWS_PROFILE AWS_REGION AWS_DEFAULT_REGION AWS_CONFIG_FILE
  ANTHROPIC_VERTEX_BASE_URL ANTHROPIC_VERTEX_PROJECT_ID
  ANTHROPIC_GOOGLE_CLOUD_BASE_URL ANTHROPIC_GOOGLE_CLOUD_PROJECT
  ANTHROPIC_GOOGLE_CLOUD_LOCATION GOOGLE_APPLICATION_CREDENTIALS GOOGLE_CLOUD_PROJECT
  ANTHROPIC_FOUNDRY_API_KEY ANTHROPIC_FOUNDRY_AUTH_TOKEN ANTHROPIC_FOUNDRY_BASE_URL
  ANTHROPIC_FOUNDRY_RESOURCE AZURE_CLIENT_ID AZURE_CLIENT_SECRET AZURE_TENANT_ID
  AZURE_FEDERATED_TOKEN_FILE
  FORGEJO_TOKEN GH_TOKEN GITHUB_TOKEN GITLAB_TOKEN
)
for deny_name in "${deny_names[@]}"; do
  printf -v "$deny_name" '%s' "${deny_name}-must-not-leak"
  export "${deny_name?}"
done

new_case same-codex
write_valid_body codex
same_output="$CASE_DIR/output/report.html"
capture_explain "$TEST_TMP/checkout" 7 --codex -R acme/widget \
  --output "$same_output" --model codex-test-model --effort medium
assert_success "generates a same-repository report with Codex"
assert_file_exists "$same_output" "atomically installs the validated report"
assert_contains "$CAPTURE_OUTPUT" "acme/widget" "confirms the repository before provider invocation"
assert_contains "$CAPTURE_OUTPUT" "#7" "confirms the pull request before provider invocation"
assert_contains "$CAPTURE_OUTPUT" "$BASE_SHA" "confirms the immutable base object ID"
assert_contains "$CAPTURE_OUTPUT" "$SAME_HEAD_SHA" "confirms the immutable head object ID"
assert_contains "$CAPTURE_OUTPUT" "codex" "confirms the selected runner"
assert_contains "$(cat "$MOCK_FORGE_LOG")" $'\tpr\tsnapshot\t7' "reads the stable snapshot interface"
assert_contains "$(cat "$MOCK_FORGE_LOG")" $'\tpr\tview\t7' "shows the human-readable PR context"
assert_equal "$(cat "$MOCK_RUNNER_DIR/codex.head")" "$SAME_HEAD_SHA" \
  "runs Codex at the snapshotted same-repository head"
assert_equal "$(cat "$MOCK_RUNNER_DIR/codex.diff")" "changed" \
  "makes the non-empty base-to-head change available to the runner"
assert_equal "$(cat "$MOCK_RUNNER_DIR/codex.status")" "" \
  "gives the runner a clean detached worktree"
for project_file in .codex/config.toml AGENTS.md .claude/settings.json CLAUDE.md; do
  assert_contains "$(cat "$MOCK_RUNNER_DIR/codex.project-config")" "$project_file" \
    "the Codex cage contains the untrusted project fixture $project_file"
done

declare -a codex_args=()
read_runner_args codex codex_args
assert_equal "${#codex_args[@]}" "18" "uses the exact hardened Codex exec argument count with a model override"
assert_equal "${codex_args[0]}" "exec" "uses codex exec"
assert_equal "${codex_args[1]}" "--ephemeral" "makes the Codex run ephemeral"
assert_equal "${codex_args[2]}" "--ignore-user-config" "ignores Codex configuration from the project"
assert_equal "${codex_args[3]}" "--ignore-rules" "ignores untrusted project-authored Codex rules"
assert_equal "${codex_args[4]}" "--dangerously-bypass-approvals-and-sandbox" \
  "uses the VM cage instead of a nested Codex sandbox"
assert_equal "${codex_args[5]}" "-C" "passes the detached checkout with Codex's directory flag"
assert_equal "${codex_args[7]}" "--add-dir" "grants Codex the private job directory"
assert_equal "${codex_args[9]}" "-c" "sets the project-document byte limit through Codex configuration"
assert_equal "${codex_args[10]}" "project_doc_max_bytes=0" "disables project document loading"
assert_equal "${codex_args[11]}" "-c" "sets the provider through Codex configuration"
assert_equal "${codex_args[12]}" 'model_provider="openai"' "pins Codex to the subscription provider"
assert_equal "${codex_args[13]}" "-c" "sets reasoning effort through Codex configuration"
assert_equal "${codex_args[14]}" 'model_reasoning_effort="medium"' "passes the requested Codex effort"
assert_equal "${codex_args[15]}" "--model" "uses Codex's model override flag"
assert_equal "${codex_args[16]}" "codex-test-model" "passes the requested Codex model"
assert_equal "${codex_args[17]}" "-" "supplies the common prompt on stdin"
assert_contains "$(cat "$MOCK_RUNNER_DIR/codex.stdin")" "complete diff" \
  "the Codex prompt requires investigation of the complete diff"
assert_contains "$(cat "$MOCK_RUNNER_DIR/codex.stdin")" "human" \
  "the Codex prompt frames the report as information transfer to a human"

codex_env="$MOCK_RUNNER_DIR/codex.env"
for deny_name in "${deny_names[@]}"; do
  assert_env_absent "$codex_env" "$deny_name" "removes $deny_name from the runner environment"
done
assert_env_value "$codex_env" FORGE_TOKEN_FILE /dev/null "blocks fallback to the default Forge token file"
assert_env_value "$codex_env" CODEX_HOME "$CODEX_HOME" "retains Codex subscription state"
assert_env_value "$codex_env" CODEX_ACCESS_TOKEN "$CODEX_ACCESS_TOKEN" "retains Codex subscription authentication"
assert_env_absent "$codex_env" ANTHROPIC_CONFIG_DIR "does not disclose Claude subscription state to Codex"
assert_env_absent "$codex_env" CLAUDE_CODE_OAUTH_TOKEN "does not disclose Claude authentication to Codex"
assert_env_absent "$codex_env" CLAUDE_CODE_OAUTH_REFRESH_TOKEN "does not disclose Claude refresh authentication to Codex"
assert_equal "$(cat "$MOCK_RUNNER_DIR/codex.job-mode")" "700" "runs Codex with a mode-0700 job directory"
if compgen -G "$CASE_DIR/output/.allod-pr-explain.*" >/dev/null; then
  fail "cleans the private job directory after success" "leftover paths:" \
    "$(compgen -G "$CASE_DIR/output/.allod-pr-explain.*")"
else
  pass "cleans the private job directory after success"
fi

new_case fork-claude
export MOCK_SCENARIO=fork
write_valid_body claude
fork_output="$CASE_DIR/output/report.html"
capture_explain "$TEST_TMP/checkout" 7 --claude -R acme/widget \
  --checkout "$TEST_TMP/checkout" --output "$fork_output" \
  --model claude-test-model --effort xhigh
assert_success "generates a fork-head report with Claude"
assert_file_exists "$fork_output" "installs the fork-head report"
assert_equal "$(cat "$MOCK_RUNNER_DIR/claude.head")" "$FORK_HEAD_SHA" \
  "fetches and cages the independent fork head"
assert_equal "$(cat "$MOCK_RUNNER_DIR/claude.diff")" "changed" \
  "diffs the fork head against the base repository commit"
for project_file in .codex/config.toml AGENTS.md .claude/settings.json CLAUDE.md; do
  assert_contains "$(cat "$MOCK_RUNNER_DIR/claude.project-config")" "$project_file" \
    "the Claude cage contains the untrusted project fixture $project_file"
done

declare -a claude_args=()
read_runner_args claude claude_args
assert_equal "${#claude_args[@]}" "14" "uses the exact hardened Claude argument count with a model override"
assert_equal "${claude_args[0]}" "-p" "uses Claude print mode"
assert_equal "${claude_args[1]}" "--safe-mode" "ignores untrusted project-authored Claude settings"
assert_equal "${claude_args[2]}" "--dangerously-skip-permissions" \
  "uses the VM cage instead of nested Claude permissions"
assert_equal "${claude_args[3]}" "--permission-mode" "sets Claude's permission mode explicitly"
assert_equal "${claude_args[4]}" "bypassPermissions" "selects Claude's bypass permission mode"
assert_equal "${claude_args[5]}" "--no-session-persistence" "does not retain a Claude session"
assert_equal "${claude_args[6]}" "--output-format" "sets Claude's noninteractive output format"
assert_equal "${claude_args[7]}" "text" "requests Claude text output"
assert_equal "${claude_args[8]}" "--add-dir" "grants Claude the private job directory"
assert_equal "${claude_args[10]}" "--effort" "uses Claude's effort flag"
assert_equal "${claude_args[11]}" "xhigh" "passes the requested Claude effort"
assert_equal "${claude_args[12]}" "--model" "uses Claude's model override flag"
assert_equal "${claude_args[13]}" "claude-test-model" "passes the requested Claude model"
if [[ "$(cat "$MOCK_RUNNER_DIR/claude.pwd")" != "$TEST_TMP/checkout" ]]; then
  pass "starts Claude outside the operator checkout"
else
  fail "starts Claude outside the operator checkout" "Claude ran in: $TEST_TMP/checkout"
fi
assert_equal "$(cat "$MOCK_RUNNER_DIR/claude.job-mode")" "700" "runs Claude with a mode-0700 job directory"
assert_contains "$(cat "$MOCK_RUNNER_DIR/claude.stdin")" "complete diff" \
  "supplies the same investigation prompt to Claude"
claude_env="$MOCK_RUNNER_DIR/claude.env"
for deny_name in "${deny_names[@]}"; do
  assert_env_absent "$claude_env" "$deny_name" "removes $deny_name from the Claude runner environment"
done
assert_env_value "$claude_env" FORGE_TOKEN_FILE /dev/null "blocks Forge token-file fallback for Claude"
assert_env_absent "$claude_env" CODEX_HOME "does not disclose Codex subscription state to Claude"
assert_env_absent "$claude_env" CODEX_ACCESS_TOKEN "does not disclose Codex authentication to Claude"
assert_env_value "$claude_env" ANTHROPIC_CONFIG_DIR "$ANTHROPIC_CONFIG_DIR" "retains Claude subscription state"
assert_env_value "$claude_env" CLAUDE_CODE_OAUTH_TOKEN "$CLAUDE_CODE_OAUTH_TOKEN" "retains Claude subscription authentication"
assert_env_value "$claude_env" CLAUDE_CODE_OAUTH_REFRESH_TOKEN "$CLAUDE_CODE_OAUTH_REFRESH_TOKEN" \
  "retains Claude subscription refresh authentication"

new_case dirty
export MOCK_SCENARIO=dirty
write_valid_body codex
printf 'operator work in progress\n' >> "$TEST_TMP/checkout/example.txt"
capture_explain "$TEST_TMP/checkout" 7 --codex --output "$CASE_DIR/output/report.html"
assert_success "allows a dirty source checkout because analysis uses an isolated worktree"
assert_contains "$(git -C "$TEST_TMP/checkout" status --porcelain)" "example.txt" \
  "leaves the operator's dirty checkout untouched"
declare -a default_codex_args=()
read_runner_args codex default_codex_args
assert_equal "${#default_codex_args[@]}" "16" "inherits the runner's built-in model default"
assert_equal "${default_codex_args[14]}" 'model_reasoning_effort="high"' \
  "defaults explanation generation to high reasoning effort"
assert_equal "${default_codex_args[15]}" "-" "keeps the stdin prompt terminator without a model override"
git -C "$TEST_TMP/checkout" restore example.txt

new_case moved-head
export MOCK_SCENARIO=moved
capture_explain "$TEST_TMP/checkout" 7 --codex -R acme/widget --output "$CASE_DIR/output/report.html"
assert_failure "fails when the remote head ref moved away from the snapshot"
assert_contains "$CAPTURE_OUTPUT" "$MOVED_OLD_SHA" "reports the snapshotted object ID on movement"
assert_contains "$CAPTURE_OUTPUT" "$MOVED_NEW_SHA" "reports the fetched remote object ID on movement"
assert_no_runner "does not disclose source to a runner after SHA movement"
assert_file_absent "$CASE_DIR/output/report.html" "does not write a report after SHA movement"

new_case wrong-checkout
capture_explain "$TEST_TMP/wrong-checkout" 7 --codex -R acme/widget \
  --checkout "$TEST_TMP/wrong-checkout" --output "$CASE_DIR/output/report.html"
assert_failure "rejects a checkout whose origin does not match the snapshotted base repository"
assert_contains "$CAPTURE_OUTPUT" "wrong checkout" "identifies the checkout mismatch"
assert_contains "$CAPTURE_OUTPUT" "acme/widget" "names the required base repository for the checkout"
assert_no_runner "does not invoke a runner for the wrong checkout"

new_case query-bearing-fork
export MOCK_SCENARIO=query-fork
capture_explain "$TEST_TMP/checkout" 7 --codex -R acme/widget \
  --output "$CASE_DIR/output/report.html"
assert_failure "rejects a query-bearing fork clone URL in the snapshot"
assert_contains "$CAPTURE_OUTPUT" "invalid PR snapshot" \
  "diagnoses unsafe fork transport metadata without fetching it"
assert_not_contains "$CAPTURE_OUTPUT" "credential-material" \
  "does not echo query-carried credential material"
assert_no_runner "does not disclose an unsafe snapshot to a runner"

new_case misdirected-fork
export MOCK_SCENARIO=outside-fork
capture_explain "$TEST_TMP/checkout" 7 --codex -R acme/widget \
  --output "$CASE_DIR/output/report.html"
assert_failure "rejects a fork clone URL that contradicts snapshot repository identity"
assert_contains "$CAPTURE_OUTPUT" "invalid PR snapshot" \
  "diagnoses a cross-host or misbound fork before any fetch"
assert_no_runner "does not disclose or fetch from a misdirected fork snapshot"

new_case credential-bearing-origin
git -C "$TEST_TMP/checkout" remote set-url origin \
  'http://user:credential-material@forge.example/acme/widget.git'
capture_explain "$TEST_TMP/checkout" 7 --codex -R acme/widget \
  --output "$CASE_DIR/output/report.html"
assert_failure "rejects credentials embedded in an HTTP checkout origin"
assert_contains "$CAPTURE_OUTPUT" "must not embed credentials" \
  "diagnoses a credential-bearing checkout origin"
assert_not_contains "$CAPTURE_OUTPUT" "credential-material" \
  "does not echo checkout credentials in the diagnostic"
assert_no_runner "does not invoke a runner for a credential-bearing checkout"
git -C "$TEST_TMP/checkout" remote set-url origin https://forge.example/acme/widget.git

new_case empty-diff
export MOCK_SCENARIO=empty
capture_explain "$TEST_TMP/checkout" 7 --codex -R acme/widget --output "$CASE_DIR/output/report.html"
assert_failure "refuses a pull request with an empty base-to-head diff"
assert_contains "$CAPTURE_OUTPUT" "empty" "explains why an empty diff cannot produce a report"
assert_no_runner "does not spend runner tokens on an empty diff"

new_case overwrite
write_valid_body codex
overwrite_output="$CASE_DIR/output/report.html"
printf 'operator-owned old report\n' > "$overwrite_output"
capture_explain "$TEST_TMP/checkout" 7 --codex -R acme/widget --output "$overwrite_output"
assert_failure "refuses to overwrite without --replace"
assert_contains "$CAPTURE_OUTPUT" "--replace" "names the explicit overwrite consent flag"
assert_equal "$(cat "$overwrite_output")" "operator-owned old report" "preserves the existing output on overwrite refusal"
assert_no_runner "checks overwrite consent before invoking a runner"

new_case dry-run
write_valid_body codex
dry_output="$CASE_DIR/output/report.html"
capture_explain "$TEST_TMP/checkout" 7 --codex -R acme/widget --output "$dry_output" --dry-run
assert_success "dry-run completes after resolving and confirming immutable inputs"
assert_file_absent "$dry_output" "dry-run writes no report"
assert_no_runner "dry-run never invokes the selected provider"

new_case runner-failure
write_valid_body codex
export MOCK_RUNNER_MODE=fail
failed_output="$CASE_DIR/output/report.html"
capture_explain "$TEST_TMP/checkout" 7 --codex -R acme/widget --output "$failed_output"
assert_failure "propagates runner failure"
assert_file_absent "$failed_output" "does not install output after runner failure"
failed_job=$(diagnostics_path)
[[ -n "$failed_job" ]] || fail "reports the preserved diagnostic directory" "output:" "$CAPTURE_OUTPUT"
assert_file_exists "$failed_job/prompt.md" "preserves the prompt for runner-failure diagnosis"
assert_equal "$(stat -c '%a' "$failed_job")" "700" "creates the failure job directory with mode 0700"

new_case runner-no-output
write_valid_body claude
export MOCK_RUNNER_MODE=no-output
capture_explain "$TEST_TMP/checkout" 7 --claude -R acme/widget --output "$CASE_DIR/output/report.html"
assert_failure "fails when a successful runner writes no report body"
assert_contains "$CAPTURE_OUTPUT" "report" "diagnoses the missing runner report"
assert_file_absent "$CASE_DIR/output/report.html" "does not install a missing report"
assert_file_exists "$(diagnostics_path)/prompt.md" "preserves diagnostics for a no-output runner"

new_case atomic-validation
write_valid_body codex
sed -i 's#</footer>#<p>AKIAABCDEFGHIJKLMNOP</p></footer>#' "$MOCK_BODY_FILE"
atomic_output="$CASE_DIR/output/report.html"
printf 'known-good previous report\n' > "$atomic_output"
capture_explain "$TEST_TMP/checkout" 7 --codex -R acme/widget \
  --output "$atomic_output" --replace
assert_failure "fails closed when the generated staging report does not validate"
assert_equal "$(cat "$atomic_output")" "known-good previous report" \
  "preserves the old report when validation fails"
assert_contains "$CAPTURE_OUTPUT" "secret" "reports the blocking secret-content validation"
assert_file_exists "$(diagnostics_path)/report-body.html" "preserves the rejected staging body for diagnosis"

# Bounded repair: the tool spends at most one extra provider call to let the
# same consented provider fix a body its own validator rejected. The contracts
# are unchanged — a repaired body is validated exactly as strictly, and every
# cage postcondition is re-checked after the repair pass.

new_case repair-succeeds
write_invalid_body codex
export MOCK_BODY_FILE_2="$MOCK_VALID_BODY_FILE"
repair_output="$CASE_DIR/output/report.html"
capture_explain "$TEST_TMP/checkout" 7 --codex -R acme/widget --output "$repair_output"
assert_success "publishes after one repair pass fixes the rejected body"
assert_file_exists "$repair_output" "installs the repaired report"
assert_contains "$CAPTURE_OUTPUT" "validation error [E8" \
  "shows the validator diagnostics that triggered the repair"
assert_contains "$CAPTURE_OUTPUT" "validation failed; requesting one repair pass" \
  "announces the repair pass before spending the second provider call"
assert_contains "$CAPTURE_OUTPUT" "repair pass accepted" "confirms the repaired report validated"
assert_contains "$CAPTURE_OUTPUT" "provider calls: at most 2" \
  "discloses the two-call ceiling before any source is sent"
assert_runner_calls codex 2 "spends exactly two provider calls when the first body fails"
assert_not_contains "$(cat "$repair_output")" '<ol class="rx-timeline">' \
  "publishes the repaired body, not the rejected one"

repair_prompt="$(cat "$MOCK_RUNNER_DIR/codex.2.stdin")"
assert_contains "$repair_prompt" "E8: every diagram component must be contained by an rx-figure" \
  "hands the exact validator diagnostics to the repair pass"
assert_contains "$repair_prompt" "BEGIN VALIDATOR DIAGNOSTICS" \
  "delimits the diagnostics as quoted data"
assert_contains "$repair_prompt" "the only file you may change" \
  "names the canonical body path as the only writable file"
assert_contains "$repair_prompt" "minimum structural correction" \
  "asks for the minimum structural correction"
assert_contains "$repair_prompt" "Preserve the semantic content" \
  "requires the repair to preserve semantic content"
assert_not_contains "$repair_prompt" "must-not-leak" \
  "never resends credentials or provider environment in the repair prompt"
if cmp -s "$MOCK_RUNNER_DIR/codex.1.stdin" "$MOCK_RUNNER_DIR/codex.2.stdin"; then
  fail "the repair pass gets its own prompt" "repair prompt is identical to the initial prompt"
else
  pass "the repair pass gets its own prompt"
fi

declare -a repair_pass_one=() repair_pass_two=()
read_runner_args codex.1 repair_pass_one
read_runner_args codex.2 repair_pass_two
assert_equal "${repair_pass_two[*]}" "${repair_pass_one[*]}" \
  "the repair pass reuses the initial hardened argument vector exactly"
repair_env_one="$MOCK_RUNNER_DIR/codex.1.env"
repair_env_two="$MOCK_RUNNER_DIR/codex.2.env"
for deny_name in "${deny_names[@]}"; do
  assert_env_absent "$repair_env_two" "$deny_name" "keeps $deny_name out of the repair environment"
done
assert_env_value "$repair_env_two" FORGE_TOKEN_FILE /dev/null "blocks Forge token fallback on repair"
assert_env_value "$repair_env_two" CODEX_HOME "$CODEX_HOME" "keeps the same provider on repair"
assert_env_absent "$repair_env_two" ANTHROPIC_CONFIG_DIR "cannot switch provider on repair"
assert_env_value "$repair_env_two" ALLOD_PR_EXPLAIN_REPORT_BODY \
  "$(sed -n 's/^ALLOD_PR_EXPLAIN_REPORT_BODY=//p' "$repair_env_one")" \
  "repairs the same staged body path"

new_case repair-diagnostics-are-not-argv
write_invalid_body claude \
  '<p><a href="#--dangerously-inject-argv">A link with no target</a></p>'
export MOCK_BODY_FILE_2="$MOCK_VALID_BODY_FILE"
capture_explain "$TEST_TMP/checkout" 7 --claude -R acme/widget \
  --output "$CASE_DIR/output/report.html"
assert_success "repairs a body whose diagnostics quote option-shaped body content"
assert_contains "$(cat "$MOCK_RUNNER_DIR/claude.2.stdin")" "--dangerously-inject-argv" \
  "quotes the option-shaped diagnostic as prompt data"
declare -a injection_args=()
read_runner_args claude.2 injection_args
for injection_arg in "${injection_args[@]}"; do
  if [[ "$injection_arg" == *"--dangerously-inject-argv"* ]]; then
    fail "diagnostics cannot reach the runner argument vector" "argument: $injection_arg"
  fi
done
pass "diagnostics cannot reach the runner argument vector"

new_case repair-fails-twice
write_invalid_body codex
# A genuinely different second body that still breaks the same contract: the
# repair pass did work, and the work still does not validate.
still_invalid="$CASE_DIR/still-invalid-body.html"
sed 's/Report assembled/Report reassembled/' "$MOCK_BODY_FILE" > "$still_invalid"
export MOCK_BODY_FILE_2="$still_invalid"
twice_output="$CASE_DIR/output/report.html"
printf 'known-good previous report\n' > "$twice_output"
capture_explain "$TEST_TMP/checkout" 7 --codex -R acme/widget --output "$twice_output" --replace
assert_failure "fails when the repair pass does not clear validation"
assert_contains "$CAPTURE_OUTPUT" "failed validation after one repair pass" \
  "says the bounded repair budget is spent"
assert_equal "$(cat "$twice_output")" "known-good previous report" \
  "preserves the old report after a failed repair"
assert_runner_calls codex 2 "never invokes the provider more than twice"
twice_job=$(diagnostics_path)
assert_file_exists "$twice_job/validation.diagnostics.txt" "preserves the first-pass diagnostics"
assert_file_exists "$twice_job/validation.repair.diagnostics.txt" "preserves the repair-pass diagnostics"
assert_file_exists "$twice_job/repair-prompt.md" "preserves the repair prompt"
assert_file_exists "$twice_job/report-body.captured.html" "preserves the first rejected body"
assert_file_exists "$twice_job/report-body.repair.captured.html" "preserves the repaired body"
assert_contains "$(cat "$twice_job/validation.repair.diagnostics.txt")" "E8:" \
  "records the stable diagnostic code that survived the repair"

new_case repair-runner-failure
write_invalid_body codex
export MOCK_RUNNER_MODE_2=fail
capture_explain "$TEST_TMP/checkout" 7 --codex -R acme/widget --output "$CASE_DIR/output/report.html"
assert_failure "propagates a repair-pass runner failure"
assert_contains "$CAPTURE_OUTPUT" "repair pass failed with status 23" \
  "names the failing pass and its status"
assert_file_absent "$CASE_DIR/output/report.html" "publishes nothing after a failed repair pass"
failed_repair_job=$(diagnostics_path)
assert_file_exists "$failed_repair_job/runner.repair.stderr" "keeps the repair runner log separate"
assert_contains "$(cat "$failed_repair_job/runner.repair.stderr")" "runner failed deliberately" \
  "records the repair pass's own runner output"
assert_equal "$(cat "$failed_repair_job/runner.stderr")" "" \
  "leaves the initial runner log untouched by the repair pass"

new_case repair-runner-no-output
write_invalid_body codex
export MOCK_RUNNER_MODE_2=no-output
capture_explain "$TEST_TMP/checkout" 7 --codex -R acme/widget --output "$CASE_DIR/output/report.html"
assert_failure "fails when the repair pass writes nothing"
assert_contains "$CAPTURE_OUTPUT" "repair pass left the staged report body unchanged" \
  "diagnoses a repair pass that produced no new body"
assert_file_absent "$CASE_DIR/output/report.html" "publishes nothing after an inert repair pass"

new_case repair-tampers-with-snapshot
write_invalid_body codex
export MOCK_BODY_FILE_2="$MOCK_VALID_BODY_FILE"
export MOCK_RUNNER_MODE_2=tamper-snapshot
capture_explain "$TEST_TMP/checkout" 7 --codex -R acme/widget --output "$CASE_DIR/output/report.html"
assert_failure "fails closed when the repair pass modifies the immutable PR snapshot"
assert_contains "$CAPTURE_OUTPUT" "repair pass modified the immutable PR snapshot" \
  "attributes the snapshot tampering to the repair pass"
assert_file_absent "$CASE_DIR/output/report.html" "publishes nothing after repair-pass snapshot tampering"

new_case repair-moves-job-checkout
write_invalid_body codex
export MOCK_BODY_FILE_2="$MOCK_VALID_BODY_FILE"
export MOCK_RUNNER_MODE_2=move-head
capture_explain "$TEST_TMP/checkout" 7 --codex -R acme/widget --output "$CASE_DIR/output/report.html"
assert_failure "fails closed when the repair pass moves the detached job checkout"
assert_contains "$CAPTURE_OUTPUT" "repair pass moved the detached job checkout" \
  "attributes the moved checkout to the repair pass"
assert_file_absent "$CASE_DIR/output/report.html" "publishes nothing after a repair-pass checkout move"

new_case repair-dirties-job-checkout
write_invalid_body codex
export MOCK_BODY_FILE_2="$MOCK_VALID_BODY_FILE"
export MOCK_RUNNER_MODE_2=dirty-worktree
capture_explain "$TEST_TMP/checkout" 7 --codex -R acme/widget --output "$CASE_DIR/output/report.html"
assert_failure "fails closed when the repair pass leaves the job checkout dirty"
assert_contains "$CAPTURE_OUTPUT" "repair pass modified the detached job checkout" \
  "attributes the dirty checkout to the repair pass"
assert_file_absent "$CASE_DIR/output/report.html" "publishes nothing after a repair-pass worktree change"

new_case repair-symlinks-body
write_invalid_body codex
export MOCK_BODY_FILE_2="$MOCK_VALID_BODY_FILE"
export MOCK_RUNNER_MODE_2=body-symlink
capture_explain "$TEST_TMP/checkout" 7 --codex -R acme/widget --output "$CASE_DIR/output/report.html"
assert_failure "fails closed when the repair pass redirects the staged body"
assert_contains "$CAPTURE_OUTPUT" "repair pass replaced the staged report with a non-regular file" \
  "attributes the redirected body to the repair pass"
assert_file_absent "$CASE_DIR/output/report.html" "publishes nothing after a repair-pass body symlink"

new_case no-repair-single-call
write_invalid_body codex
export MOCK_BODY_FILE_2="$MOCK_VALID_BODY_FILE"
no_repair_output="$CASE_DIR/output/report.html"
printf 'known-good previous report\n' > "$no_repair_output"
capture_explain "$TEST_TMP/checkout" 7 --codex -R acme/widget \
  --output "$no_repair_output" --replace --no-repair
assert_failure "--no-repair fails on the first validation failure"
assert_contains "$CAPTURE_OUTPUT" "--no-repair kept this run to one provider call" \
  "explains that no repair pass was attempted"
assert_contains "$CAPTURE_OUTPUT" "provider calls: 1 (--no-repair)" \
  "discloses the single-call ceiling up front"
assert_runner_calls codex 1 "--no-repair spends exactly one provider call"
assert_equal "$(cat "$no_repair_output")" "known-good previous report" \
  "--no-repair preserves the old report"
assert_file_exists "$(diagnostics_path)/validation.diagnostics.txt" \
  "--no-repair still preserves the validator diagnostics"

new_case no-repair-valid-body
write_valid_body codex
no_repair_valid_output="$CASE_DIR/output/report.html"
capture_explain "$TEST_TMP/checkout" 7 --codex -R acme/widget \
  --output "$no_repair_valid_output" --no-repair
assert_success "--no-repair publishes a body that validates the first time"
assert_runner_calls codex 1 "a passing first body costs one provider call"

new_case no-repair-repeated
capture_explain "$TEST_TMP/checkout" 7 --codex -R acme/widget \
  --output "$CASE_DIR/output/report.html" --no-repair --no-repair
assert_failure "rejects a repeated --no-repair"
assert_contains "$CAPTURE_OUTPUT" "--no-repair may be specified only once" \
  "diagnoses the repeated flag"
assert_no_runner "rejects malformed options before any provider call"

new_case dry-run-repair-disclosure
write_valid_body codex
capture_explain "$TEST_TMP/checkout" 7 --codex -R acme/widget \
  --output "$CASE_DIR/output/report.html" --dry-run
assert_success "dry-run resolves without a provider call"
assert_contains "$CAPTURE_OUTPUT" "provider calls: 0 (--dry-run)" \
  "dry-run discloses that it spends no provider call"
assert_runner_calls codex 0 "dry-run invokes the provider zero times"

new_case moved-base
export MOCK_SCENARIO=moved-base
capture_explain "$TEST_TMP/checkout" 7 --codex -R acme/widget --output "$CASE_DIR/output/report.html"
assert_failure "fails when the remote base ref moved away from the snapshot"
assert_contains "$CAPTURE_OUTPUT" "$BASE_SHA" "reports the snapshotted base object ID on movement"
assert_contains "$CAPTURE_OUTPUT" "$MOVED_BASE_NEW_SHA" "reports the fetched remote base object ID on movement"
assert_no_runner "does not disclose source to a runner after base SHA movement"
assert_file_absent "$CASE_DIR/output/report.html" "does not write a report after base SHA movement"
assert_file_exists "$(diagnostics_path)/snapshot.json" "preserves the snapshot for a moved-base diagnosis"

new_case moved-fork-head
export MOCK_SCENARIO=moved-fork-head
capture_explain "$TEST_TMP/checkout" 7 --codex -R acme/widget --output "$CASE_DIR/output/report.html"
assert_failure "fails when the fork's remote head ref moved away from the snapshot"
assert_contains "$CAPTURE_OUTPUT" "$FORK_HEAD_SHA" "reports the snapshotted fork head object ID on movement"
assert_contains "$CAPTURE_OUTPUT" "$MOVED_FORK_NEW_SHA" "reports the fetched fork remote head object ID on movement"
assert_no_runner "does not disclose source to a runner after fork head SHA movement"
assert_file_absent "$CASE_DIR/output/report.html" "does not write a report after fork head SHA movement"
assert_file_exists "$(diagnostics_path)/snapshot.json" "preserves the snapshot for a moved-fork-head diagnosis"

new_case body-symlink
write_valid_body codex
export MOCK_RUNNER_MODE=body-symlink
capture_explain "$TEST_TMP/checkout" 7 --codex -R acme/widget --output "$CASE_DIR/output/report.html"
assert_failure "fails closed when the runner replaces the staged body with a symlink"
assert_contains "$CAPTURE_OUTPUT" "non-regular file" "diagnoses the symlinked body"
assert_file_absent "$CASE_DIR/output/report.html" "does not publish after a symlinked body"
assert_file_exists "$(diagnostics_path)/prompt.md" "preserves diagnostics for a symlinked body"

new_case body-atomic-replace
write_valid_body codex
export MOCK_RUNNER_MODE=body-replace
atomic_replace_output="$CASE_DIR/output/report.html"
capture_explain "$TEST_TMP/checkout" 7 --codex -R acme/widget --output "$atomic_replace_output"
assert_success "accepts a runner that atomically replaces the staged body with a new regular file at the same path"
assert_file_exists "$atomic_replace_output" "publishes the report after an atomic body replacement"
assert_contains "$(cat "$atomic_replace_output")" "The change makes the teaching path explicit" \
  "publishes the atomically replaced body content"

new_case snapshot-tampered
write_valid_body codex
export MOCK_RUNNER_MODE=tamper-snapshot
capture_explain "$TEST_TMP/checkout" 7 --codex -R acme/widget --output "$CASE_DIR/output/report.html"
assert_failure "fails closed when the runner modifies the immutable PR snapshot"
assert_contains "$CAPTURE_OUTPUT" "modified the immutable PR snapshot" "diagnoses the tampered snapshot"
assert_file_absent "$CASE_DIR/output/report.html" "does not publish after a tampered snapshot"
assert_file_exists "$(diagnostics_path)/prompt.md" "preserves diagnostics for a tampered snapshot"

new_case job-checkout-head-moved
write_valid_body codex
export MOCK_RUNNER_MODE=move-head
capture_explain "$TEST_TMP/checkout" 7 --codex -R acme/widget --output "$CASE_DIR/output/report.html"
assert_failure "fails closed when the runner moves the detached job checkout away from the snapshotted head"
assert_contains "$CAPTURE_OUTPUT" "moved the detached job checkout" "diagnoses the moved job checkout"
assert_file_absent "$CASE_DIR/output/report.html" "does not publish after the job checkout moved"
assert_file_exists "$(diagnostics_path)/prompt.md" "preserves diagnostics for a moved job checkout"

new_case job-checkout-left-dirty
write_valid_body codex
export MOCK_RUNNER_MODE=dirty-worktree
capture_explain "$TEST_TMP/checkout" 7 --codex -R acme/widget --output "$CASE_DIR/output/report.html"
assert_failure "fails closed when the runner leaves the detached job checkout dirty"
assert_contains "$CAPTURE_OUTPUT" "modified the detached job checkout" "diagnoses the dirty job checkout"
assert_file_absent "$CASE_DIR/output/report.html" "does not publish after the job checkout is left dirty"
assert_file_exists "$(diagnostics_path)/prompt.md" "preserves diagnostics for a dirty job checkout"

new_case publication-race-no-clobber
write_valid_body codex
race_output="$CASE_DIR/output/report.html"
export MOCK_RUNNER_MODE=publish-race
export MOCK_RACE_OUTPUT="$race_output"
export MOCK_RACE_CONTENT="intruder content that appeared mid-run"
capture_explain "$TEST_TMP/checkout" 7 --codex -R acme/widget --output "$race_output"
assert_failure "refuses to publish when the output path appeared during generation"
assert_contains "$CAPTURE_OUTPUT" "appeared during generation" "diagnoses the publication race"
assert_equal "$(cat "$race_output")" "intruder content that appeared mid-run" \
  "preserves the file that raced into the no-clobber output path"
assert_file_exists "$(diagnostics_path)/prompt.md" "preserves diagnostics for a no-clobber publication race"
unset MOCK_RACE_OUTPUT MOCK_RACE_CONTENT

new_case publication-race-replace
write_valid_body codex
race_output="$CASE_DIR/output/report.html"
export MOCK_RUNNER_MODE=publish-race
export MOCK_RACE_OUTPUT="$race_output"
export MOCK_RACE_CONTENT="intruder content that appeared mid-run"
capture_explain "$TEST_TMP/checkout" 7 --codex -R acme/widget --output "$race_output" --replace
assert_success "--replace publishes despite a mid-run race, per its documented overwrite consent"
assert_not_contains "$(cat "$race_output")" "intruder content" \
  "overwrites the file that raced in, since --replace already authorized overwriting whatever is there"
unset MOCK_RACE_OUTPUT MOCK_RACE_CONTENT

# forge companion resolution: pr-explain must use the `forge` shipped beside
# the resolved allod tools root over anything installed earlier on PATH, per
# the exact bug this guards against — a stale PATH `forge` shadowing a source
# checkout's own compatible companion.

new_case forge-resolution-source-checkout-beats-stale-path
fake_checkout="$CASE_DIR/tools-root"
make_fake_source_checkout "$fake_checkout"
stale_marker="$CASE_DIR/stale-forge-invoked"
install_stale_forge "$CASE_DIR/stale-path/forge" "$stale_marker"

saved_tools_dir="${ALLOD_TOOLS_DIR:-}"
saved_forge_override="${ALLOD_PR_EXPLAIN_FORGE:-}"
saved_path="$PATH"
unset ALLOD_TOOLS_DIR ALLOD_PR_EXPLAIN_FORGE
export PATH="$CASE_DIR/stale-path:$PATH"
capture_explain_with "$fake_checkout/allod" "$TEST_TMP/checkout" 7 --codex -R acme/widget \
  --output "$CASE_DIR/output/report.html" --dry-run
export ALLOD_TOOLS_DIR="$saved_tools_dir"
export ALLOD_PR_EXPLAIN_FORGE="$saved_forge_override"
export PATH="$saved_path"

assert_success "a source checkout's own ./allod resolves the PR through its own companion forge"
assert_file_absent "$stale_marker" "never falls back to a stale forge earlier on PATH when the checkout ships its own"
assert_file_exists "$fake_checkout/.invoked" "uses the forge shipped beside the resolved tools root"

new_case forge-resolution-explicit-override-wins
fake_checkout="$CASE_DIR/tools-root"
make_fake_source_checkout "$fake_checkout"
override_forge="$CASE_DIR/override-forge/forge"
mkdir -p "$(dirname -- "$override_forge")"
write_forge_mock_script "$override_forge"

saved_tools_dir="${ALLOD_TOOLS_DIR:-}"
saved_forge_override="${ALLOD_PR_EXPLAIN_FORGE:-}"
unset ALLOD_TOOLS_DIR
export ALLOD_PR_EXPLAIN_FORGE="$override_forge"
capture_explain_with "$fake_checkout/allod" "$TEST_TMP/checkout" 7 --codex -R acme/widget \
  --output "$CASE_DIR/output/report.html" --dry-run
export ALLOD_TOOLS_DIR="$saved_tools_dir"
export ALLOD_PR_EXPLAIN_FORGE="$saved_forge_override"

assert_success "an explicit ALLOD_PR_EXPLAIN_FORGE override resolves the PR"
assert_file_exists "$(dirname -- "$override_forge")/.invoked" \
  "the explicit override is used even though the checkout ships its own compatible forge"
assert_file_absent "$fake_checkout/.invoked" \
  "the tools-root companion is not consulted once an explicit override is set"

new_case forge-override-rejects-nonexecutable
export ALLOD_PR_EXPLAIN_FORGE="$CASE_DIR/not-a-forge"
printf 'not a script\n' > "$ALLOD_PR_EXPLAIN_FORGE"
capture_explain "$TEST_TMP/checkout" 7 --codex -R acme/widget --output "$CASE_DIR/output/report.html"
assert_failure "rejects a non-executable ALLOD_PR_EXPLAIN_FORGE override"
assert_contains "$CAPTURE_OUTPUT" "ALLOD_PR_EXPLAIN_FORGE must name an executable file" \
  "names the malformed override clearly"
assert_no_runner "never invokes a provider with a malformed forge override"
export ALLOD_PR_EXPLAIN_FORGE="$TEST_TMP/bin/forge"

new_case forge-override-rejects-missing
export ALLOD_PR_EXPLAIN_FORGE="$CASE_DIR/does-not-exist"
capture_explain "$TEST_TMP/checkout" 7 --codex -R acme/widget --output "$CASE_DIR/output/report.html"
assert_failure "rejects an ALLOD_PR_EXPLAIN_FORGE override that does not exist"
assert_contains "$CAPTURE_OUTPUT" "ALLOD_PR_EXPLAIN_FORGE must name an executable file" \
  "names the missing override clearly"
assert_no_runner "never invokes a provider with a missing forge override"
export ALLOD_PR_EXPLAIN_FORGE="$TEST_TMP/bin/forge"

finish_tests "allod pr explain command"
