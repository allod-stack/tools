# wake-agent

`wake-agent` leaves one bounded watcher behind, waits for one stable external event, and gives a written brief to one fresh headless Pi session. Its status, sentinel, and log make every normal exit inspectable without the originating transcript; the optional Pi extension shows `wake-agent: running` in every open session while the watcher or continuation is active.

## Run one continuation

```console
$ wake-agent --cap 12h --timeout 90m --retry \
    'ssh buildbox test -f /var/tmp/build.done' ./continuation.md
Running as unit: wake-agent.service
status: /home/alice/.local/state/wake-agent/status
log: /home/alice/.local/state/wake-agent/wake-agent.log
```

The foreground command resolves Pi, util-linux `script`, and the brief, starts a collected transient user unit named `wake-agent.service`, and returns after systemd's `Type=exec` handoff. That handoff proves the worker executable started; the status and sentinel appear when it writes `waiting` immediately afterward. A second watcher is refused while that unit exists. The unit inherits the caller's working directory, but its environment is the user manager's environment rather than the invoking shell's. Put environment setup in the probe command or user manager; do not put secrets directly in the probe string, because that string is visible in unit and process metadata. The watcher runs the probe through `/bin/sh -c` and interprets its exit code as:

| Exit | Meaning |
|---:|---|
| 0 | The event was observed. The probe must return 0 again after `--grace` before Pi starts. |
| 1 | The event is not ready; wait `--interval` and try again. |
| 2 | The watched target is gone; stop with stage `dead-target`. |
| other | The probe is broken; stop with stage `probe-failed`. |

Defaults are a five-minute poll interval, a 30-second confirmation grace, a 24-hour total cap, a 90-minute Pi timeout, and no retry. Pi runs with its configured default provider and model; a thinking model can sit silent for minutes before its first token, so size `--timeout` for the model and not only the task, or the run ends at `agent-timeout` with an empty log. `--retry` permits exactly one additional Pi attempt after a non-zero exit or timeout. A cap or timeout terminates the whole child process group and kills any survivor within two seconds. The Pi build used here hangs in print mode without a terminal, so the worker gives it a pseudo-terminal through `script -qec ... /dev/null`; `-e` propagates Pi's exit status. The Nix package supplies `script`; a source-built binary requires it on `PATH`. Every child argument is shell-quoted, and the command inside that wrapper remains exactly:

```console
pi --no-session -p "$(cat continuation.md)"
```

The brief must be a readable regular file smaller than 128 KiB with no NUL byte, and its shell-quoted Pi command must also fit Linux's 128 KiB single-argument limit. A quote-heavy brief can therefore hit the limit sooner. It is checked before detaching and read again after the event is confirmed. `wake-agent` does not interpret or supplement it, so it must name the task, relevant context paths, stop conditions, and where the fresh agent should write its report.

The transient service survives terminal closure. Whether it survives the last login session closing depends on the user manager: machines that stop the user manager at logout need lingering enabled for this account.

## Inspect a run

The default state directory is `$XDG_STATE_HOME/wake-agent`, or `$HOME/.local/state/wake-agent` when `XDG_STATE_HOME` is unset. `WAKE_AGENT_STATE_DIR` overrides it for an isolated run. Files are mode 0600:

- `status` is the last atomic state snapshot.
- `wake-agent.log` records stage transitions and Pi output.

The live sentinel is `$XDG_RUNTIME_DIR/wake-agent/sentinel`; `WAKE_AGENT_RUNTIME_DIR` overrides that path, and the state directory is the fallback when no runtime directory exists. Runtime storage clears it across reboot. The footer also verifies that `wake-agent.service` is active and removes a stale live sentinel, so SIGKILL or a failed terminal-state write cannot leave a running badge behind.

Both state files use newline-delimited `key=value` fields. `stage` is one of the live stages (`waiting`, `confirming`, `running`, `retrying`) or a terminal stage such as `complete`, `capped`, `dead-target`, `probe-failed`, `brief-failed`, `agent-timeout`, or `agent-failed`. Every snapshot includes the absolute log path and update time.

```console
$ cat ~/.local/state/wake-agent/status
stage=complete
log=/home/alice/.local/state/wake-agent/wake-agent.log
updated=2026-10-22T12:00:00Z
attempt=1
```

## Install the Pi indicator

Pi auto-loads TypeScript files in `~/.pi/agent/extensions/`. Link the extension shipped with the package:

```console
$ mkdir -p ~/.pi/agent/extensions
$ ln -s /path/to/wake-agent/share/wake-agent/wake-agent.ts \
    ~/.pi/agent/extensions/wake-agent.ts
```

The extension polls the sentinel and unit state every two seconds. It only adds the persistent `wake-agent: running` status; it does not start or stop the watcher. Deployment-specific installation belongs in the deployment repository.
