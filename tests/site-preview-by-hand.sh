#!/usr/bin/env bash
# 'allod site preview' against real Nix and a real systemd user manager, which
# the flake's checks have neither of, so this is run by hand (or by an agent) in
# a dev VM and is not wired into 'nix flake check'. The binary comes from
# ALLOD_UNDER_TEST, as in the other scripts here.
#
# Every fixture is built under a temporary directory and every unit it starts is
# stopped on the way out, including after a failing step. The fixture preview
# apps are plain shell and pull in no flake inputs, so nothing is downloaded.
set -euo pipefail

ALLOD="${ALLOD_UNDER_TEST:-$(command -v allod || true)}"
TMP=$(mktemp -d)
PORT_SERVES=18611
PORT_FAILS=18612
PORT_APPLESS=18613
UNIT_SERVES=allod-preview-fixture-preview-serves
UNIT_FAILS=allod-preview-fixture-preview-fails
UNIT_APPLESS=allod-preview-fixture-preview-appless
PAGE_MARK="fixture page $$"
LOG_MARK="fixture preview app refused to start $$"
FAILURES=0
STEPS=0
OUTPUT=""
STATUS=0

cleanup() {
  local unit
  for unit in "$UNIT_SERVES" "$UNIT_FAILS" "$UNIT_APPLESS"; do
    systemctl --user stop "$unit" >/dev/null 2>&1 || true
    systemctl --user reset-failed "$unit" >/dev/null 2>&1 || true
  done
  rm -rf "$TMP"
}
trap cleanup EXIT

for tool in nix systemctl systemd-run journalctl curl nc ss; do
  if ! command -v "$tool" >/dev/null 2>&1; then
    printf 'this script needs %s on PATH, and it is not there\n' "$tool" >&2
    exit 2
  fi
done
if [ ! -x "$ALLOD" ]; then
  printf 'set ALLOD_UNDER_TEST to the allod binary to test\n' >&2
  exit 2
fi

# --- Fixtures ---

SYSTEM=$(nix eval --impure --raw --expr builtins.currentSystem)
export WORK_DIR="$TMP/work"
export INVENTORY="$TMP/inventory"
mkdir -p "$INVENTORY/scripts"

new_site() {
  local root="$WORK_DIR/sites/$1"
  mkdir -p "$root"
  printf 'domain = "example.invalid"\n' > "$root/site.toml"
  printf '%s' "$root"
}

# shell_app_flake writes a flake whose only output is the preview app, run from
# the serve.sh its caller writes beside it.
shell_app_flake() {
  cat > "$1/flake.nix" <<EOF
{
  outputs = { self }: {
    apps.$SYSTEM.preview = { type = "app"; program = "\${self}/serve.sh"; };
  };
}
EOF
}

ROOT_SERVES=$(new_site serves)
shell_app_flake "$ROOT_SERVES"
printf '<h1>%s</h1>\n' "$PAGE_MARK" > "$ROOT_SERVES/index.html"
# One page on one port, one connection at a time. The delay before the first
# listen stands in for the build a real preview app does first, and is what
# makes 'start serves the fixture page' notice a start that reported success
# before anything was listening.
cat > "$ROOT_SERVES/serve.sh" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
page=$(cat "${0%/*}/index.html")
printf 'fixture preview app waiting before it listens\n'
sleep 3
printf 'fixture preview app listening on %s:%s\n' "$ALLOD_PREVIEW_INTERFACE" "$ALLOD_PREVIEW_PORT"
while true; do
  printf 'HTTP/1.1 200 OK\r\nContent-Type: text/html\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s' \
    "${#page}" "$page" | nc -l "$ALLOD_PREVIEW_INTERFACE" "$ALLOD_PREVIEW_PORT" >/dev/null
done
EOF
chmod +x "$ROOT_SERVES/serve.sh"

ROOT_FAILS=$(new_site fails)
shell_app_flake "$ROOT_FAILS"
printf '#!/usr/bin/env bash\nprintf "%%s\\n" "%s" >&2\nexit 1\n' "$LOG_MARK" > "$ROOT_FAILS/serve.sh"
chmod +x "$ROOT_FAILS/serve.sh"

ROOT_APPLESS=$(new_site appless)
printf '{\n  outputs = { self }: { };\n}\n' > "$ROOT_APPLESS/flake.nix"

cat > "$INVENTORY/scripts/repositories.json" <<EOF
{
  "repositories": {
    "fixture/preview-serves":  { "checkout": "sites/serves",  "preview_port": $PORT_SERVES },
    "fixture/preview-fails":   { "checkout": "sites/fails",   "preview_port": $PORT_FAILS },
    "fixture/preview-appless": { "checkout": "sites/appless", "preview_port": $PORT_APPLESS }
  }
}
EOF

# --- Harness ---

# step runs one named step in a subshell that sets '-e' itself and reads the
# status as a plain statement. Neither is decoration: bash turns '-e' off for
# the whole of an 'if' condition, the body of every function called from one
# included, so 'if step_x; then PASS' would enforce only the step's last line.
step() {
  local name="$1"
  shift
  set +e
  ( set -e; "$@" )
  status=$?
  set -e
  STEPS=$((STEPS + 1))
  if [ "$status" -eq 0 ]; then
    printf 'PASS %s\n' "$name"
  else
    printf 'FAIL %s\n' "$name" >&2
    FAILURES=$((FAILURES + 1))
  fi
}

# fail_step ends the step's subshell, and only that subshell.
fail_step() {
  printf '     %s\n' "$@" >&2
  exit 1
}

assert_status() {
  if [ "$1" != "$2" ]; then
    fail_step "$3: expected exit $1, got $2" "output:" "$OUTPUT"
  fi
}

assert_contains() {
  case "$1" in
    *"$2"*) ;;
    *) fail_step "$3: expected to contain: $2" "got:" "$1" ;;
  esac
}

assert_absent() {
  case "$1" in
    *"$2"*) fail_step "$3: expected not to contain: $2" "got:" "$1" ;;
  esac
}

preview() {
  set +e
  OUTPUT=$("$ALLOD" site preview "$@" 2>&1)
  STATUS=$?
  set -e
}

unit_state() {
  systemctl --user is-active "$1" 2>/dev/null || true
}

unit_main_pid() {
  systemctl --user show --property MainPID --value "$1" 2>/dev/null || printf '0'
}

listening_on() {
  ss --listening --tcp --numeric --no-header "sport = :$1" 2>/dev/null || true
}

# --- Steps ---

start_serves_a_page() {
  preview fixture/preview-serves
  assert_status 0 "$STATUS" "start"
  assert_contains "$OUTPUT" "http://127.0.0.1:$PORT_SERVES" "start prints the address"
  # Two retries, about two seconds of grace: enough for the fixture server to be
  # back between connections, and less than the three it waits before its first
  # one, so a start that printed the address without waiting leaves nothing to
  # fetch here.
  page=$(curl --silent --show-error --retry 2 --retry-connrefused \
    --max-time 5 "http://127.0.0.1:$PORT_SERVES/")
  assert_contains "$page" "$PAGE_MARK" "the page served is the fixture's own"
}

start_again_starts_no_second_server() {
  before=$(unit_main_pid "$UNIT_SERVES")
  if [ "$before" = "0" ]; then
    fail_step "the unit has no main process, so this step would prove nothing"
  fi
  preview fixture/preview-serves
  assert_status 0 "$STATUS" "second start"
  after=$(unit_main_pid "$UNIT_SERVES")
  if [ "$before" != "$after" ]; then
    fail_step "the second start replaced the server: main process $before became $after"
  fi
}

stop_frees_the_port() {
  preview --stop fixture/preview-serves
  assert_status 0 "$STATUS" "stop"
  state=$(unit_state "$UNIT_SERVES")
  if [ "$state" = "active" ]; then
    fail_step "the unit is still active after the stop: $state"
  fi
  left=$(listening_on "$PORT_SERVES")
  if [ -n "$left" ]; then
    fail_step "something is still listening on $PORT_SERVES after the stop" "$left"
  fi
  nc -l 127.0.0.1 "$PORT_SERVES" </dev/null >/dev/null 2>&1 &
  binder=$!
  sleep 1
  bound=$(listening_on "$PORT_SERVES")
  kill "$binder" >/dev/null 2>&1 || true
  wait "$binder" >/dev/null 2>&1 || true
  if [ -z "$bound" ]; then
    fail_step "$PORT_SERVES did not bind again after the stop"
  fi
}

stop_again_is_not_an_error() {
  preview --stop fixture/preview-serves
  assert_status 0 "$STATUS" "second stop"
  assert_contains "$OUTPUT" "is not running" "the second stop says the preview is not running"
}

an_app_that_exits_is_reported_as_failed() {
  preview fixture/preview-fails
  assert_status 1 "$STATUS" "start of an app that exits at once"
  assert_contains "$OUTPUT" "$LOG_MARK" "the failure carries the unit's own log"
  assert_absent "$OUTPUT" "http://127.0.0.1:$PORT_FAILS" "a failed start prints no address"
}

a_flake_without_the_app_is_refused() {
  preview fixture/preview-appless
  assert_status 1 "$STATUS" "start of a flake with no preview app"
  assert_contains "$OUTPUT" "apps.$SYSTEM.preview" "the refusal names the missing app"
  listed=$(systemctl --user list-units --all --no-legend "$UNIT_APPLESS.service" 2>/dev/null || true)
  if [ -n "$listed" ]; then
    fail_step "a refused start left a unit behind" "$listed"
  fi
}

step 'start serves the fixture page' start_serves_a_page
step 'start again starts no second server' start_again_starts_no_second_server
step 'stop leaves no process and frees the port' stop_frees_the_port
step 'stop again says not running and exits 0' stop_again_is_not_an_error
step 'an app that exits at once is reported as failed' an_app_that_exits_is_reported_as_failed
step 'a flake without the app is refused' a_flake_without_the_app_is_refused

if [ "$FAILURES" -ne 0 ]; then
  printf '%d of %d steps failed\n' "$FAILURES" "$STEPS" >&2
  exit 1
fi
printf 'all %d steps passed\n' "$STEPS"
