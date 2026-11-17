# Lifecycle telemetry

`lbctl install`, `upgrade`, `uninstall`, `start`, `stop`, `restart`, `update`
(including `self-update`), and `autostart status|enable|disable` send a `started` event and a `succeeded` or `failed`
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
failures may leave them empty. No command arguments, hostnames, deployment paths,
Core credentials, or command output streams are collected. Error messages redact
home directories, known usernames, absolute user paths, URLs, IP addresses and
common token/API-key forms, and retain at most 1,024 UTF-8 bytes from the tail,
keeping complete lines when possible. Space repeats
redaction and accepts only the defined metadata fields.

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

`telemetry.Options.OnFirstRun` is the integration hook for the upcoming localized
privacy/terms notice. It runs when an enabled lifecycle command first creates
the installation ID. No notice text is introduced here; the i18n work owns it.
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
error. It does not download Core or alter installed services.
