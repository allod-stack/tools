#!/usr/bin/env bash
# shellcheck source=/dev/null
source "$(dirname "${BASH_SOURCE[0]}")/testlib.sh"

new_case help
capture "$ALLOD" pr explain --help
assert_success "shows PR explain help"
assert_contains "$CAPTURE_OUTPUT" "--codex" "documents Codex as an explicit consent choice"
assert_contains "$CAPTURE_OUTPUT" "--claude" "documents Claude as an explicit consent choice"
assert_contains "$CAPTURE_OUTPUT" "--output" "documents the required output path"

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

new_case body-inode-replaced
write_valid_body codex
export MOCK_RUNNER_MODE=body-replace
capture_explain "$TEST_TMP/checkout" 7 --codex -R acme/widget --output "$CASE_DIR/output/report.html"
assert_failure "fails closed when the runner replaces the staged body file instead of populating it in place"
assert_contains "$CAPTURE_OUTPUT" "instead of populating it in place" "diagnoses the inode replacement"
assert_file_absent "$CASE_DIR/output/report.html" "does not publish after an inode-replaced body"
assert_file_exists "$(diagnostics_path)/prompt.md" "preserves diagnostics for an inode-replaced body"

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

finish_tests "allod pr explain command"
