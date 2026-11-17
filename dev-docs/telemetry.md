# Lifecycle telemetry

`lbctl install`, `upgrade`, `uninstall`, `start`, `stop`, `restart`, `update`
(including `self-update`), and `autostart status|enable|disable` send a `started` event and a `succeeded`, `failed`, or `interrupted`
event directly to Space. Help and unrelated commands do not send events.
Autostart command names are reported as `autostart.status`, `autostart.enable`, and `autostart.disable`.
Validation failures and dry-run/check executions are included.

Set `LANGBOT_TELEMETRY=0` or `DO_NOT_TRACK=1` to disable collection, including
creation of the anonymous installation ID. The ID is a random UUID stored in
`~/.langbot/telemetry-id`, independent of deployment IDs and `LANGBOT_HOME`.

The default endpoint is `https://space.langbot.app/api/v1/telemetry`.
`LBCTL_TELEMETRY_ENDPOINT` overrides it for local testing. Sending is asynchronous,
with a two-second request deadline, no redirects and no retries. Before exit,
the process waits at most one second, then cancels outstanding requests.
Network failures are silent and never change the operation's exit code.
Delivery is best-effort: an unreachable endpoint or forced process termination
can lose an event. Events are not queued on disk.

Payloads contain the command, outcome, step, error code, duration, lbctl version,
OS/architecture, driver, service manager, region, Core version, locale, and
installation source. Environment fields are populated when resolved; early
failures may leave them empty. Each invocation has an `attempt_id` shared by its
started and terminal events. `previous_failures` is the count of earlier failed
or interrupted invocations of the same command on this installation, persisted
under `~/.langbot/telemetry-failures.json`; successful runs do not reset it.

Failed and interrupted events optionally include `details`:
- `process`: command basename, redacted arguments, exit code, and separate
  stdout/stderr tails (60 lines and 8 KiB per stream).
- `log`: filename and tail (80 lines, 8 KiB). `health_check` and `start_service`
  use `logs/langbot.log`; other steps use `logs/install.log`, if present.
- `environment`: OS version, container/WSL/admin flags, shell version, CPU count,
  total memory and available instance-disk bytes, proxy presence booleans,
  and available Docker/Compose/uv/Python versions. Version queries have a shared
  two-second deadline. No proxy values or environment dumps are collected.
- `network`: one HEAD request per actual attempted source during `download_uv`,
  `install_python`, `install_package`, or `pull_image`, recording the sanitized
  source URL, HTTP status or error type, and elapsed milliseconds. Queries and
  userinfo are removed before probing. Probes do not follow redirects, each has
  a three-second deadline, and all diagnostic collection shares five seconds.

URLs retain scheme, host and path; credentials, fragments and query values are
removed. User-home paths retain the filename with only the username segment
replaced by `<user>`. Private, loopback and link-local IPs are retained; public
IPs, tokens, authorization values, passwords, API keys and JWTs are redacted.
Sensitive object/configuration keys are redacted recursively. Errors retain at
most 8 KiB of UTF-8 text, keeping complete tail lines when possible. The encoded
event is at most 32 KiB: large diagnostic fields are shortened to fit, without
dropping the failure itself. Space repeats redaction and size bounding.

Native dependency execution failures use `error_code=dependency`. Cancellation
and signal termination use `interrupted`. Registration conflicts use
`step=register`, distinct from occupied ports (`check_ports`).

`LBCTL_INSTALL_SOURCE=install.sh` or `install.ps1` identifies the bootstrap
script; other values become `manual`. The repository bootstrap scripts set this
environment variable for child processes. The separately published one-click
scripts at `langbot.app/install.sh` and `langbot.app/install.ps1` must set it
before invoking lbctl as part of their release.
Their source files are `public/install.sh` and `public/install.ps1` in
`langbot-app/langbot-landing-page`: add `export LBCTL_INSTALL_SOURCE=install.sh`
and `$env:LBCTL_INSTALL_SOURCE = "install.ps1"`, respectively. That repository
is outside this change's two-repository scope.

The first-run localized terms notice states that failures include redacted
diagnostic information. Opt-out disables diagnostics, probes and failure-counter
writes as well as transmission and installation-ID creation.
`LBCTL_LANG`, `LC_ALL`, `LC_MESSAGES`, then `LANG` supply the current locale hint.

## Local verification

Docker source failures additionally emit `command=image_pull`, `step=image_pull`,
`outcome=failed`, `error_code=image_pull` even if a later source succeeds. These
attempt events do not change the enclosing install/upgrade outcome and contain
no repository URL or Docker output. See [Docker image sources](docker-image-sources.md).

```sh
go test ./internal/telemetry ./internal/command ./internal/app ./internal/deploy
go build -o lbctl ./cmd/lbctl
```

The Space integration test accepts `LBCTL_TEST_BINARY` and exercises this binary
against a local HTTP server and PostgreSQL database. It occupies an ephemeral
port, runs native install with an explicit Core version and region, and asserts
exit code 6 plus a stored `failed` event with `step=check_ports` and a redacted
error. Optional extended Linux scenarios use `LBCTL_TEST_UV` (a real uv binary)
and `LBCTL_TEST_PYTHON` (an installed uv-managed CPython directory) to exercise an
unreachable PyPI index and a real health-check timeout against an intentionally
unresponsive temporary process. They do not alter installed services.
`LBCTL_DIAGNOSTICS_ARTIFACT` saves the three persisted Space events as JSON.
