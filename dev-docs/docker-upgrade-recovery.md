## Docker upgrade backup and recovery

Docker upgrades inspect and copy data using a short-lived container from the
current Compose image, with networking disabled. The source data mount is
read-only. Root-owned files never require host-side sudo or chown. Backup copies
are owned by the user owning the backup directory; the existing manifest verifies
their contents before the Compose switch. The source data ownership is unchanged.

`upgrade --name NAME` and `upgrade --dir DIR` select the same local instance as
start, stop and uninstall. Failure guidance distinguishes target selection, image
pull, stopping, backup, switching and health checks. After a recorded interrupted
upgrade, inspect `upgrade-state.yaml` and preserve the verified backup before any
manual recovery; the CLI does not automatically retry database migrations.

`RestoreDockerUpgradeBackup` verifies the manifest and restores data in a temporary
container using the backed-up Compose image. Stop all instance services before
calling it. It replaces the data tree, including removal of files created by the
failed version. The backup stays read-only; host access to the live data is not
required. Compose assets and the deployment record remain separate recovery steps.

Backups intentionally normalize file modes to 0600 and directory modes to 0700;
restored data is owned by root, matching the default container runtime user.
Original permission bits and ownership are not preserved. If custom data includes
executable scripts or files requiring other permissions, reapply those permissions
inside the container before restarting the instance.

Run `scripts/test-docker-e2e.sh` with an isolated `LANGBOT_HOME` as a non-root
Docker-group user. It installs v4.11.7, creates a root-only data directory, upgrades
to v4.11.8, checks backup contents and HTTP readiness, and stops its own deployment.
`LBCTL_DOCKER_TEST_IMAGE=rockchin/langbot:v4.11.8 go test -v ./internal/deploy -run
TestDockerBackupNonRootRoundTrip` separately exercises backup, mutation and restore.
