## Native deployment validation

Run the Go suite and all six supported cross-builds:

```sh
go test ./...
go vet ./...
sh scripts/build.sh
python3 scripts/test-install-wizard.py dist/lbctl_linux_amd64
```

The PTY harness covers an available Docker daemon/Compose v2 fixture, absent
Docker, and non-TTY native defaults. It performs dry runs in temporary homes.
The Go tests cover release fallback, mirror persistence, native backup retention,
failed-health rollback, checksum rejection, self-update replacement, and log
rotation/follow cancellation.

`scripts/test-native-e2e.sh` first fails runtime preparation after recording
v4.11.7, then reruns install without `--version`. It verifies that the missing
venv and service are installed while preserving the deployment ID, recorded
version, and data. The Windows PowerShell harness covers the same recovery.
Both run in the three-platform native E2E CI matrix.
They also uninstall the original instance and install a replacement under a new
name on the same port, checking WebUI health and preservation of the old data.
Named lifecycle commands cover both standard and custom instance directories.

The shell harness checks WebUI HTTP 200, exercises
status/logs/start/stop/restart, upgrades to v4.11.8 while retaining a data marker,
and uninstalls while preserving data. Run inside a disposable
`debian:stable-slim` container after installing only `curl` and
`ca-certificates`, with the static CLI binary mounted as
`/usr/local/bin/lbctl`. Run as both root and an unprivileged user. Set `REGION=cn`
to exercise the mainland defaults.

`scripts/test-native-rollback.sh` uses a real v4.11.8 installation, then injects
a synthetic failing v4.11.9 runtime and a no-op uv fixture. The synthetic runtime
mutates a data marker and returns HTTP 503. The script verifies that upgrade
failure restores v4.11.8, the original marker, and WebUI HTTP 200. It also checks
follow-log cancellation and uninstalls. This is a failure-injection test, not a
test of a published v4.11.9 release. Use only a disposable container.

`scripts/test-native-review.sh` checks human lifecycle output, public webhook
preservation through restart and upgrade, runtime-port conflict rejection before
writing a second deployment, and native doctor capability warnings. Run in the
same disposable container setup. Remove `/.dockerenv` inside the test container
to simulate bare-metal runtime selection; do not remove it on production hosts.

## Integration requirements

- The landing download sync publishes all six pinned uv 0.9.11 assets beneath
  `download.langbot.app/tools/uv/0.9.11/`. Exact filenames and SHA-256 values are
  in `internal/deploy/native_runtime.go`. An unavailable mirror falls back to
  GitHub.
- Core 4.11.x detects `/.dockerenv` and chooses external WebSocket plugin/Box
  runtimes. Bare-metal native deployments launch both runtimes over stdio.
  Core 4.11.x fixes the embedded Box relay at 5410; lbctl rejects conflicts
  on 5401 and 5410 rather than starting a partly functional second instance.
  Container tests must simulate bare metal explicitly as described above;
  lbctl does not patch Python packages or remove container markers.
- Docker-created data can be owned by root, including migration backup
  directories. A non-root operator without read access cannot back up or upgrade
  that data. Preflight checks data readability and backup-directory writability
  before stopping Core and advises sudo or chown on permission failures;
  lbctl does not change ownership automatically.
- Cross-builds verify compilation, not actual launchd or Windows scheduled-task
  behavior. Validate those managers on their respective operating systems before
  release.
