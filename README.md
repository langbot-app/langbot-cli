<div align="center">

<a href="https://langbot.app">
<img src="https://raw.githubusercontent.com/langbot-app/LangBot/master/res/logo-blue.png" width="96" alt="LangBot"/>
</a>

<h1>LangBot CLI</h1>

<p>Control your LangBot Workspace and self-hosted deployments from the terminal.</p>

English / [简体中文](README_CN.md) / [繁體中文](README_TW.md) / [日本語](README_JP.md) / [Español](README_ES.md) / [Français](README_FR.md) / [한국어](README_KO.md) / [Русский](README_RU.md) / [Tiếng Việt](README_VI.md)

[![Release](https://img.shields.io/github/v/release/langbot-app/langbot-cli)](https://github.com/langbot-app/langbot-cli/releases/latest)
[![CLI checks](https://img.shields.io/github/actions/workflow/status/langbot-app/langbot-cli/ci.yml?branch=main&label=checks)](https://github.com/langbot-app/langbot-cli/actions/workflows/ci.yml)
[![Go](https://img.shields.io/badge/Go-1.26-00ADD8?logo=go&logoColor=white)](go.mod)
[![License](https://img.shields.io/github/license/langbot-app/langbot-cli)](LICENSE)

<a href="https://langbot.app">Website</a> ｜
<a href="https://github.com/langbot-app/LangBot">LangBot Core</a> ｜
<a href="https://github.com/langbot-app/langbot-cli/releases">Releases</a> ｜
<a href="https://github.com/langbot-app/langbot-cli/issues">Issues</a>

</div>

<p align="center">
<a href="#installation">Install</a> ·
<a href="#connection">Connect</a> ·
<a href="#deployment">Deploy</a> ·
<a href="#resources">Resources</a> ·
<a href="#automation">Automation</a> ·
<a href="#source">Build</a>
</p>

---

Output language is detected automatically (English or Simplified Chinese).
Override it with `--lang en`, `--lang zh-Hans`, or `LBCTL_LANG`. Use `--verbose`
for full installation subprocess output. The first run prints the terms and
anonymous telemetry notice to stderr; disable telemetry with
`LANGBOT_TELEMETRY=0` or `DO_NOT_TRACK=1`. JSON/YAML and schema output remain
language-independent. See [output and localization](dev-docs/localization.md).

The standalone LangBot command-line tool, provided as `lbctl`. It manages Workspace resources over HTTP, with multiple contexts, identity and capability discovery, resource management, RAG configuration discovery, Pipeline extensions and test runs, runtime records and Sandbox diagnostics, asynchronous task waiting, and installation, adoption, lifecycle management, and upgrades for native Python and Docker Compose deployments.

Use a LangBot Core that provides:

- API Key authentication for `/api/v1/system/context` and `/api/v1/system/capabilities`.
- Bot, Pipeline, Knowledge Base, Provider, Model, Plugin, Skill, MCP Server, and task APIs.
- Server-side redaction through `include_secret=false` for Provider/Model queries.

`lbctl` executes operations according to the capabilities, Workspace bindings, and permissions declared by Core.

<a id="installation"></a>

## Installation

macOS, Linux, or Windows Git Bash:

```sh
curl -fsSL https://download.langbot.app/install.sh | sh
```

Windows PowerShell:

```powershell
irm https://download.langbot.app/install.ps1 | iex
```

The installer selects the binary for your platform and verifies SHA-256. The shell installer defaults to `~/.local/bin`; the PowerShell installer defaults to `%LOCALAPPDATA%\Programs\lbctl` and adds it to your user `PATH`.

Cloudflare Workers serves the download mirror for installers, binaries, and checksums. Releases are synchronized from [GitHub Releases](https://github.com/langbot-app/langbot-cli/releases), where the original files are also available. Set `LBCTL_VERSION` to install a specific published version.

<a id="source"></a>

## Build from source

```sh
make build
./bin/lbctl version
./bin/lbctl --help
```

The Go baseline is 1.26. Run the compiled binary directly; deployment management supports native Python and Docker Compose.

<a id="connection"></a>

## Configure remote instances

Provide the API Key through an environment variable or one-time stdin input. Configuration is selected in this order: `--config`, `LANGBOT_CONFIG`, then `~/.config/langbot/config.yaml`. It stores the variable name rather than the key itself. `context list`, `context show`, `status`, and `doctor` display the selected configuration path. Without a name, `context show` inspects the current context. Offline credential availability only means the variable is set; use `context check` or `whoami` to verify authentication.

Create a Workspace API Key in the target LangBot dashboard's API Key management after initial setup. Authenticated commands detect missing credentials and show the selected environment variable, hidden-input PowerShell/Bash examples, and a same-target `whoami` verification step. JSON/YAML includes `error.reason` and `error.guidance`; the error type remains `auth` with exit code 3. A rejected key is reported separately from an unset variable; the CLI never prompts automatically or saves the key.

First discover the Workspace associated with the key:

```sh
export LANGBOT_PRODUCTION_API_KEY='<api-key>'

LANGBOT_API_KEY="$LANGBOT_PRODUCTION_API_KEY" \
  lbctl --endpoint https://bot.example.com/langbot whoami
```

Bind the returned `workspace_uuid` to a context:

```sh
lbctl context add production \
  --endpoint https://bot.example.com/langbot \
  --api-key-env LANGBOT_PRODUCTION_API_KEY \
  --expect-workspace '<workspace_uuid>' \
  --timeout 15s

lbctl context use production
lbctl context check production
lbctl whoami
lbctl capabilities
```

Manage multiple targets:

```sh
lbctl context add dev --endpoint http://localhost:5300 \
  --api-key-env LANGBOT_DEV_API_KEY \
  --expect-workspace '<workspace_uuid>'
lbctl context list
lbctl context show production
lbctl context check --all
lbctl --context dev whoami
lbctl context update production --timeout 30s
lbctl context remove dev --yes
```

Connection settings follow this precedence, with validation of the selected target and credentials.

| Setting | Precedence |
|---|---|
| Context | `--context` → `LANGBOT_CONTEXT` → current context |
| Endpoint | `--endpoint` → `LANGBOT_ENDPOINT` → context configuration |
| Key | `--api-key-stdin` → `LANGBOT_API_KEY` → context environment variable reference |
| Timeout | `--timeout` → `LANGBOT_TIMEOUT` → context configuration → 30s |

Use `--api-key-stdin` for a single invocation and `--file -` separately for request-body input. Temporary endpoints support controlled reads; resource writes use a saved, Workspace-bound context.

Credential stdin must be closed after the key is provided. Its wait budget uses the resolved `--timeout` / `LANGBOT_TIMEOUT` / context timeout (30s by default), separately from each HTTP request; a timeout returns `network` with `reason: stdin_timeout` and exit code 7 without sending an authenticated request.

<a id="deployment"></a>

## Local deployment and upgrades

Install the latest stable Core with an isolated, automatically provisioned Python 3.12 runtime:

```sh
lbctl install
lbctl install --driver native --name mybot --port 5301 --region cn --yes
lbctl install --driver docker --name container --port 5302
lbctl upgrade --yes
lbctl update --check
lbctl update
```

Native is the default, including non-interactive installs. On a terminal, when Docker Compose v2 and its daemon are available, the wizard offers native (recommended) or Docker. Enter selects native; `--yes` skips prompts. An occupied HTTP port offers the next free port. Prompts follow `LANG`. Pin Core with `--version vX.Y.Z`; `upgrade` requires `--yes` and otherwise resolves the latest stable version from LangBot Space, with GitHub as fallback. `update` (alias `self-update`) upgrades **lbctl**, not Core; package-managed installations print the package manager command instead.

New instances use `$LANGBOT_HOME/instances/<name>` (`--name default`). `LANGBOT_HOME` defaults to `~/.langbot`, `/opt/langbot` for root, or `%LOCALAPPDATA%\LangBot` on Windows. Existing locator registrations, including `~/.config/langbot/deployment`, remain manageable. Native installs use checksum-verified uv 0.9.11 under `tools/uv/0.9.11`, managed Python under `tools/python`, and a separate `venvs/vX.Y.Z` for each Core version; system uv and Python are not used or modified. Optional dependencies: `--extra seekdb,milvus,valkey`.

Native services use systemd (user units for non-root), launchd LaunchAgents, or Windows per-user login startup (`HKCU\Software\Microsoft\Windows\CurrentVersion\Run`), with a pidfile-backed process fallback where these managers are unavailable. Windows installs do not require administrator privileges: `windows-run` starts a detached supervisor immediately and registers it for the current user's next login. `stop` removes the startup entry; `start` restores it, and `uninstall` removes it. Existing scheduled-task deployments keep their recorded manager. The Windows startup command must fit the registry Run key's 260-character limit; use shorter binary and deployment paths if installation reports that limit. For systemd user units, run `loginctl enable-linger` to retain the service after logout. Runtime logs are stored in `instances/<name>/logs/langbot.log` with three rotated files and a 10 MiB limit per file. Installation output is saved to `logs/install.log`; installation failures report the failed step, existing log paths, and applicable recovery commands. `start`, `stop`, `restart`, `status`, `logs -f`, `doctor`, `upgrade`, and `uninstall` select the recorded driver. `doctor` reports missing Box sandbox, stdio MCP, and audio capabilities (Docker/Podman/nsjail, node+npx, and ffmpeg respectively).

Rerun `lbctl install` with the same name and port to repair a failed native installation. Without `--version`, an existing native deployment retains its recorded Core version, identity, and data while missing runtime dependencies and service registration are repaired. Use `lbctl upgrade --yes` to change Core versions. Registration conflicts identify the existing deployment and print a command to resume that instance instead.

Use `--name <instance>` or `--dir <directory>` with `start`, `stop`, `restart`, `uninstall`, and `autostart` to select a local instance independently of the current context. Do not combine these local selectors with connection flags. A successful native uninstall preserves its record and data but releases its endpoint registration, allowing another name or directory to reuse that port.

Native upgrades prepare a new venv before stopping the service, back up data while excluding rebuildable caches/venvs, and check `/healthz` after switching. A failed health check restores the previous version and data. The newest three backups are retained. Uninstall preserves data and backups unless `--purge-data` is explicitly passed.

On Windows, the supervisor releases its console before acquiring the instance lock or starting Python. Login startup can briefly flash a console, but leaves no persistent console window. Python and process-tree cleanup run without a console window; closing the launching terminal does not stop the instance.

Use `lbctl autostart status|enable|disable` to inspect or change login/boot startup without stopping the running instance. Select a named instance with `--name mybot`, or an explicit directory with `--dir`. Status reports the mechanism and its unit/plist/registry/Compose location. Explicitly disabling startup persists `autostart: false` in the deployment record: start, restart, and upgrade preserve that preference. On Windows, a normal stop removes the Run entry temporarily; a later start restores it only if autostart was not explicitly disabled. On macOS, disabling removes the login plist without unloading the running job; subsequent manual starts use a detached process. The process-only fallback cannot enable login startup and reports `supported: false`.

New Docker installations use `restart: unless-stopped`. For lbctl-generated Compose deployments, autostart toggles rewrite the Compose restart policy and apply `docker update --restart=...` to existing containers without stopping/recreating them. Disabled instances use `restart: "no"`, including after upgrade. Existing installations without an explicit preference retain their previous policy until changed. Adopted/custom Compose layouts remain readable through `autostart status`, but modifying their startup policy requires editing their Compose configuration directly; lbctl does not regenerate user-owned service definitions.

Core 4.11.x native deployments require exclusive runtime ports 5401 (plugin debugging) and 5410 (Box). Installation and startup reject occupied ports, including an HTTP port overlapping either runtime port. Use Docker for concurrently running multiple instances. The HTTP port is managed by lbctl: select `--port` during installation; for an existing deployment, stop it, update `port` and `endpoint` in `deployment.yaml`, then run `lbctl start`. Editing `api.port` in Core's config alone does not change the managed port. The webhook URL is initialized only before the first `data/config.yaml` exists; subsequent starts preserve a public `api.webhook_prefix` configured by the user.

`--region auto|cn|global` uses `LANGBOT_REGION` first, then a two-second country probe. `cn` selects npmmirror for Python, Aliyun PyPI (Tsinghua fallback), and the same index for plugin dependencies. Docker selects the fastest available public mirror in `cn`, or when Docker Hub is unavailable, with `docker.langbot.app/langbot-public/rockchin/langbot` as its final fallback. Automatic pulls use an official digest and verify it before saving immutable Compose references. Upgrade reuses the saved source, reselecting on failure; `doctor` lists source availability. An explicit `--image-repository` disables selection and fallback. See [Docker image sources](dev-docs/docker-image-sources.md). A missing LangBot wheel can be obtained from PyPI while dependencies keep the configured mirror. Override Python sources with `--pypi-index` or `--python-mirror`; settings persist for upgrades. The uv mirror is preferred in `cn`, with GitHub fallback.

Update notices read a local cache only; a timeout-bounded detached check refreshes it at most daily. Notices appear only on a terminal, never for JSON/YAML, `CI`, or `LBCTL_NO_UPDATE_CHECK=1`. `status` includes current/latest lbctl and Core versions when cached. `install --output json` includes `url`, `driver`, `instance_dir`, `version`, `name`, `region`, and `service_manager`.

Lifecycle commands send anonymous operation outcomes and redacted failure details directly to LangBot Space, even when Core cannot start. Set `LANGBOT_TELEMETRY=0` or `DO_NOT_TRACK=1` to opt out. See [lifecycle telemetry](dev-docs/telemetry.md) for collected fields, timeout guarantees, and local testing.

Discover and register multiple local LangBot Compose instances:

```sh
lbctl context list --discover
lbctl --endpoint http://127.0.0.1:15301 adopt \
  --dir /absolute/path/records/first --file /absolute/path/first/compose.yaml --project first
lbctl --endpoint http://127.0.0.1:15302 adopt \
  --dir /absolute/path/records/second --file /absolute/path/second/compose.yaml --project second
lbctl context add first --endpoint http://127.0.0.1:15301
lbctl context add second --endpoint http://127.0.0.1:15302
lbctl context list
lbctl context use second
lbctl status
lbctl stop
lbctl --context first status
```

`context list` lists saved contexts and local deployment registrations offline, including matching Compose projects. Add `--discover` to inspect local Compose projects without adopting them or saving contexts. Unreadable projects and projects using multiple Compose files appear in `skipped_projects`. Use `adopt` to register each instance with a distinct record directory and Compose project. Status, diagnostics, lifecycle, logs, and upgrades match the active context/endpoint against all registered deployments; ambiguous matches are rejected.

Use `context use <name>` for both cloud and local instances, or `--context <name>` for a single invocation. Switching works offline and temporary selection preserves the saved current context. Explicit selectors and environment variables retain their existing priority. Legacy single-deployment locators migrate on the next registration, preserving the original fallback directory. Adopting another instance does not change the current context. Older CLI versions cannot read the new registry format.

`install --driver docker` supports independent Compose projects and host port mappings. Defaults remain `--project langbot-lbctl`, `--plugin-debug-port 5401`, and `--adapter-ports 2280-2285:2280-2285`. Use a different `--dir`, `--project`, and HTTP `--port` for each installation. Set `--plugin-debug-port 0` and `--adapter-ports none` to disable those host publications for headless instances:

```sh
lbctl install --driver docker --version <vX.Y.Z> --dir /absolute/path/regression \
  --project regression --port 15301 --profile all \
  --plugin-debug-port 0 --adapter-ports none --dry-run
```

Remove `--dry-run` to install. For plugin development or inbound adapters, select unused host ports instead:

```sh
lbctl install --driver docker --version <vX.Y.Z> --dir /absolute/path/second \
  --project second --port 15302 --plugin-debug-port 15402 \
  --adapter-ports 12280-12285:2280-2285
```

Adapter mappings accept comma-separated `HOST:CONTAINER` pairs or equal-length ranges; container ports are limited to `2280-2285`. Publish only the ports you need, for example `--adapter-ports 12280:2280,12283:2283`. Preflight checks only the selected host ports and Compose project, and rejects overlapping mappings. Container-internal service ports stay unchanged. The generated debug URL follows the selected host debug port. Publication settings are saved with the deployment and preserved during upgrades; legacy deployment records continue to work.

`adopt` registers an existing Compose deployment for local management while preserving its service configuration:

```sh
lbctl --endpoint http://127.0.0.1:5300 adopt \
  --file /absolute/path/docker-compose.yaml --project langbot --profile all
lbctl status
lbctl doctor
lbctl stop
lbctl start
lbctl restart
lbctl logs --follow --tail 100
```

Install a new local deployment, or uninstall a deployment while retaining its data:

```sh
lbctl install --version <vX.Y.Z> --profile basic --port 5300
lbctl uninstall --yes
```

For Docker deployments, `lbctl upgrade` upgrades the LangBot service images. CLI-installed and adopted deployments use the same commands:

```sh
lbctl upgrade --version <newer-vX.Y.Z> --dry-run -o json
lbctl upgrade --version <newer-vX.Y.Z> --yes -o json
```

Adopted upgrades replace the images for `langbot`, `langbot_plugin_runtime`, and an existing `langbot_box`, preserving other Compose content, including ports, mounts, environment configuration, comments, and other services. An upgrade pulls and verifies the target image, stops the deployment, backs up data, Compose, the project `.env` when present, and the deployment record, then starts and verifies the target version. Backups and recovery state remain available after failures for inspection with `status` and `doctor`.

Automatic upgrades support services declaring `rockchin/langbot:<tag>` directly, with all data under the Compose directory's `data` bind mount and local SQLite, Chroma, and local storage. Compose configuration, backup scope, and versions are checked before upgrading; the target version is newer than the current version. `--dry-run` shows the plan, and execution verifies the target image. After manually editing adopted Compose content, run `adopt` again for the same target to confirm its updated asset digest.

Local lifecycle commands target deployment instances. Resources such as Bots, Pipelines, and Skills use the same commands below to manage the context's Workspace.

An existing instance's resources can be managed as soon as its context and API key are configured. Docker adoption is needed for local lifecycle operations, deployment logs, and upgrades.

Human help groups commands by purpose, shows applicable and required flags, and includes common examples. Lists show names and state first while retaining complete IDs. Long names are shortened in tables; wide tables use per-item fields (`COLUMNS` can set the display width). JSON/YAML retains complete names and structured data. Resource lists accept `--filter` for a case-insensitive substring match on names or IDs. Filtering happens in the client; it does not reduce server transfer or choose among duplicate names.

```sh
lbctl bot list --filter my-bot
lbctl bot get <bot-id>             # Follow use_pipeline_uuid with pipeline get
lbctl model list --type llm --provider <provider-id>
lbctl model list --filter deepseek -o json
lbctl bot update --help            # Minimal body example and preflight scope
lbctl schema --summary             # Start with a compact command index
lbctl schema --command bot         # Expand one command group
lbctl schema --command 'bot update'
```

Shell completion reads saved context names and static enums without requesting remote resources. Long commands display elapsed waiting time on stderr in human terminals; redirected and JSON/YAML output remain quiet. Results distinguish preflight, submission, verification, and unverified hidden fields while preserving partial results and exit codes. Where applicable, `meta.next_steps` contains follow-up argument arrays retaining connection selectors, rendered as copyable commands for humans.

<a id="resources"></a>

## Manage resources

Use JSON or YAML files for complex request bodies, with fields matching the LangBot HTTP API. `--file -` reads from stdin.

### Bot and Pipeline

```sh
lbctl bot list
lbctl bot get <uuid>
lbctl bot create --file bot.yaml
lbctl bot update <uuid> --file bot.yaml
lbctl bot delete <uuid> --yes

lbctl pipeline list
lbctl pipeline get <uuid>
lbctl pipeline apply --file pipeline.yaml
lbctl pipeline copy <uuid>
lbctl pipeline delete <uuid> --yes
```

`pipeline apply` updates when the request body contains `uuid`, and creates otherwise.

### Knowledge Base

```sh
lbctl knowledge-base list
lbctl knowledge-base get <uuid>
lbctl knowledge-base create --file knowledge-base.yaml
lbctl knowledge-base update <uuid> --file knowledge-base.yaml
lbctl knowledge-base retrieve <uuid> --file query.yaml
lbctl knowledge-base file list <uuid>
lbctl knowledge-base file delete <uuid> <file-id> --yes
lbctl knowledge-base delete <uuid> --yes
```

Upload a document, submit ingestion, and wait for completion:

```sh
lbctl knowledge-base ingest <uuid> --file ./document.pdf --wait
```

Repeat `--file` or recursively import a directory with `--dir`. Both can be combined; duplicate paths are submitted once:

```sh
lbctl knowledge-base ingest <uuid> --file ./guide.pdf --file ./faq.md --wait
lbctl knowledge-base ingest <uuid> --dir ./documents --dry-run
lbctl knowledge-base ingest <uuid> --dir ./documents --wait
```

Directory scans skip dot-prefixed files/directories and symbolic links. Each file remains subject to the existing 10 MiB upload limit, including request metadata. The CLI checks all local paths before uploading and submitting files sequentially. `--wait` follows each task, with `--wait-timeout` applied separately to each file. Batch JSON/YAML output retains per-file status, file IDs, task IDs, and errors in `files`, with counts in `summary`; human output shows the same per-file outcomes. Without `--wait`, success means tasks were submitted.

Confirmed file-specific failures allow the remaining files to proceed. An unknown submission, incomplete wait, authentication/permission failure, or incompatible target stops further submissions, preserves completed and unstarted items, and exits nonzero. Re-running uploads again; there is no cross-run deduplication or automatic retry. Single-file ZIP submission remains supported, but Core returns only one child task ID. Batch `--wait` rejects ZIP inputs before uploading: extract the archive and use `--dir` instead.

### Provider and Model

```sh
lbctl provider list
lbctl provider get <uuid>
lbctl provider create --file provider.yaml
lbctl provider update <uuid> --file provider.yaml
lbctl provider scan-models <uuid> --type llm
lbctl provider delete <uuid> --yes

lbctl model list
lbctl model list --type llm --provider <provider-uuid>
lbctl model get <uuid> --type llm
lbctl model create --type llm --file model.yaml
lbctl model update <uuid> --type llm --file model.yaml
lbctl model test <uuid> --type llm
lbctl model delete <uuid> --type llm --yes
```

Model types are `llm`, `embedding`, and `rerank`. Provider/Model output uses server-side redaction and client-side field filtering; model scanning returns structured results.

### Plugin and Skill

```sh
lbctl plugin list
lbctl plugin get <author> <name>
lbctl plugin config get <author> <name>
lbctl plugin config update <author> <name> --file config.yaml
lbctl plugin install github --file plugin.yaml --wait
lbctl plugin install marketplace --file plugin.yaml --wait
lbctl plugin install local --file plugin.zip --wait
lbctl plugin upgrade <author> <name> --wait
lbctl plugin logs <author> <name>
lbctl plugin delete <author> <name> --yes --wait

lbctl skill list
lbctl skill get <name>
lbctl skill preview <name>
lbctl skill file list <name>
lbctl skill file read <name> <path>
lbctl skill file write <name> <path> --file content.md
lbctl skill install github --file skill.yaml
lbctl skill install upload --file skill.zip
lbctl skill delete <name> --yes
```

Skill installation with `--dry-run` uses a server-side preview. Plugin installation, upgrade, and deletion support `--wait` to follow asynchronous tasks.

GitHub plugin requests include `owner`, `repo`, `release_tag`, and either `release_id`/`asset_id` or `asset_url`. For URL input, the CLI queries the GitHub release and submits the same ID-based request as WebUI. GitHub metadata requests are separate from Workspace authentication; dry-run performs preflight checks. Example:

```yaml
owner: langbot-app
repo: langbot-plugins
release_tag: 0.0.1.alpha.1
asset_url: https://github.com/langbot-app/langbot-plugins/releases/download/0.0.1.alpha.1/langbot-team-GoogleSearch-0.1.2.lbpkg
```

Plugin configuration updates wait for readback after reload within a bounded timeout. Deletion supports both running plugins and installations that failed to start.

### MCP Server

```sh
lbctl mcp-server list
lbctl mcp-server get <name>
lbctl mcp-server create --file server.yaml
lbctl mcp-server update <name> --file server.yaml
lbctl mcp-server test <name> --wait
lbctl mcp-server resources <name>
lbctl mcp-server resource-templates <name>
lbctl mcp-server resource-read <name> --file request.yaml
lbctl mcp-server logs <name>
lbctl mcp-server delete <name> --yes
```

## Writes and tasks

Writes verify the saved context, Workspace binding, actual identity, permissions, capabilities, and required resource prerequisites. General rules:

- `--dry-run` performs preflight checks; Skill installation uses a server-side preview.
- Deletion requires explicit `--yes` confirmation.
- Verifiable writes read back the resource after completion.
- Uncertain request outcomes return `result_unknown` for inspection before deciding the next step.
- Multi-step operations retain created resources or `task_id` values for progress tracking.

Bot/Pipeline reads retain ordinary configuration and mask credentials with `***`. Creates, updates, and Pipeline copies compare observable fields on readback; mismatches return `verification_failed`. `verification.observable_fields_match` describes the comparison, while `unverified_fields` lists masked fields. `verified` is true only when every checked field is verifiable and matches. Bot updates preserve existing credentials for exact `***` placeholders; placeholders that cannot be safely restored are rejected before updating.

Query asynchronous tasks independently:

```sh
lbctl task list
lbctl task list --type <type> --kind <kind>
lbctl task get <task-id>
lbctl task get <task-id> --wait --wait-timeout 10m
```

Local waiting and server-side task lifecycles are independent; continue checking with `task get`.

## Privacy

Lifecycle commands report anonymous outcomes and redacted errors to Space; set `LANGBOT_TELEMETRY=0` or `DO_NOT_TRACK=1` to opt out. See [lifecycle telemetry](dev-docs/telemetry.md) for collected fields and delivery limits. Existing requests to your own LangBot include a client identifier (`lbctl/<version> (<os>/<arch>)`) and the canonical command path, without arguments or flag values. Managed deployments record `LANGBOT_DEPLOY_SOURCE=lbctl` and the CLI version in `LANGBOT_DEPLOY_TOOL_VERSION`; upgrades refresh these values while preserving your other environment settings. Core-side telemetry is controlled separately by `space.disable_telemetry`.

## Controlled API access

`api get/post` calls operations registered in the built-in registry:

```sh
lbctl api get '/api/v1/system/tasks?kind=plugin'
lbctl api post '/api/v1/knowledge/bases/<uuid>/retrieve' --file query.yaml
```

Use dedicated commands for Provider/Model management, installation, testing, and other sensitive operations. `raw` provides compatibility reads of `/api/v1/system/info`.

<a id="automation"></a>

## Output and checks

The default output is concise and human-readable. Automation can use `-o json` or `-o yaml` for complete structured results. Success and error results go to stdout; diagnostics go to stderr.

`status` summarizes the active environment and keeps identity-mismatch and recovery warnings visible. Add `-o json` or `-o yaml` to the same command for full instance/Workspace IDs and deployment paths. A fetched capability list does not mean every operation is supported; inspect `capabilities` for individual operation states.

| Exit code | Meaning |
|---|---|
| 0 | Success |
| 2 | Input or configuration error |
| 3 / 4 / 5 | Authentication error / permission denied / not found |
| 6 | Unmet prerequisite or conflict |
| 7 | Network, TLS, timeout, or cancellation |
| 8 | API or protocol incompatibility |
| 9 | Deployment recovery required |
| 10 | Unclassified server or internal error |

```sh
make check
make cross-build

# Read-only diagnostics against a running Core.
LBCTL_TEST_ENDPOINT=http://localhost:5300 \
  go test ./internal/integration -run TestLiveCore -v
```

`make cross-build` generates amd64/arm64 binaries for macOS, Linux, and Windows, plus `dist/checksums.txt`.

Agent-friendly command discovery and shell completion:

```sh
lbctl schema
lbctl schema --command plugin.install.github
lbctl completion bash
```

## RAG configuration discovery

These commands use Core's corresponding capabilities and require `resource.view`. Output supports `-o json` and `-o yaml`.

```sh
lbctl knowledge-engine list
lbctl knowledge-engine creation-schema author/engine -o yaml
lbctl knowledge-engine retrieval-schema author/engine -o json
lbctl knowledge-parser list --mime-type application/pdf
```

Select a `plugin_id` from the engine list. The creation schema defines knowledge-base creation settings, and the retrieval schema defines query settings. Use `knowledge-base create`, `ingest`, and `retrieve` for ingestion and retrieval.

## Pipeline extension bindings

```sh
lbctl pipeline extensions get PIPELINE_UUID -o json
```

`pipeline extensions update PIPELINE_UUID --file extensions.yaml` **replaces all bindings**. All eight fields below are required. Use `extensions get` to inspect current bindings and available resources, and retain every item you want to keep. Updates are verified by readback; `--dry-run` checks input and prerequisites.

This example clears all four explicit binding lists and turns off every enable switch.

```yaml
bound_plugins: []
bound_mcp_servers: []
bound_skills: []
bound_mcp_resources: []
enable_all_plugins: false
enable_all_mcp_servers: false
enable_all_skills: false
mcp_resource_agent_read_enabled: false
```

Empty arrays with `enable_all_*: false` leave that extension type unbound; `true` enables all available extensions of that type. Plugin entries use `{author: author, name: plugin}`; MCP Server entries use `uuid` from `available_mcp_servers`; Skill entries use `name` from `available_skills`. An MCP Resource entry looks like `{server_uuid: SERVER_UUID, uri: "docs://guide"}`. `mcp_resource_agent_read_enabled` controls Agent resource listing, reading, and inclusion of bound resources in context.

Updates require a saved, Workspace-bound context, `resource.manage` and `resource.view` permissions, and the corresponding Core capability.

## Single-turn Pipeline test runs

```sh
lbctl pipeline run PIPELINE_UUID --message 'Answer this question using the knowledge base' --timeout 2m -o json
```

Test runs use a saved, Workspace-bound context and require `runtime.operate` and `resource.view`. They call the configured models and tools. Each run creates a fresh session. Results include the reply, session ID, query ID, and available message IDs; failures return a nonzero exit code.

Core waits up to 60 seconds; CLI `--timeout` controls the HTTP request wait. A timeout means **execution status is unknown**. Inspect runtime records using returned message/session identifiers on the LangBot WebUI monitoring page (`/home/monitoring`). After a client timeout, you can also search recent records by Pipeline and time.

Core declares the `pipeline.run` capability; use `--dry-run` to check prerequisites.

## Application runtime diagnostics

Query runtime records, failed message details, and call errors in the current API Key's Workspace using the corresponding Core capabilities.

```sh
lbctl monitoring errors --pipeline PIPELINE_UUID --limit 20
lbctl monitoring messages --pipeline PIPELINE_UUID --start-time 2026-09-16T10:00:00+08:00 --end-time 2026-09-16T10:05:00+08:00
lbctl monitoring messages --session SESSION_ID --limit 20
lbctl monitoring message MESSAGE_ID
lbctl monitoring session SESSION_ID
lbctl monitoring llm-calls --pipeline PIPELINE_UUID
lbctl monitoring tool-calls --session SESSION_ID
lbctl monitoring embedding-calls --knowledge-base KB_UUID
lbctl monitoring sessions --active true
```

Runtime-record queries require `resource.view`. Lists support `--limit 1..500` and `--offset`, subject to Core's limits. `--start-time` and `--end-time` use ISO 8601. See each command's `--help` for filters.

## Sandbox configuration and diagnostics

Workspace sandbox configuration uses Core's `workspace.sandbox.get/update/check/test` capabilities:

```sh
lbctl sandbox config get --refresh
lbctl sandbox config update --file sandbox.yaml --dry-run
lbctl sandbox config update --file sandbox.yaml --yes
lbctl sandbox config check --file e2b.yaml --e2b-api-key-env E2B_API_KEY
lbctl --timeout 2m sandbox config test --file e2b.yaml --e2b-api-key-env E2B_API_KEY --yes
```

Example `sandbox.yaml`:

```yaml
backend: docker
limits:
  timeout_sec: 60
  max_sessions: 2
```

Updates accept `backend: docker/e2b/nsjail`, with optional `e2b` and `limits`. E2B fields are `api_url`, `sandbox_url`, `template`, and `api_key`. Prefer injecting the private key with `--e2b-api-key-env`, or supply the request body via `--file -`. Body and environment-key inputs cannot be combined. The Workspace `--api-key-stdin` remains available but cannot share stdin with `--file -`. Queries expose only `api_key_configured`; the CLI never stores the private key in its context. Core determines credential retention and when changed addresses require a new key.

A check/test draft such as `e2b.yaml` contains `{"e2b":{"api_url":"https://api.example.com","sandbox_url":"https://sandbox.example.com","template":"base"}}`. Omit the file to use Core's current E2B configuration. `check` neither saves nor creates a sandbox. Explicit `test --yes` consumes E2B capacity and may incur charges: Core creates a temporary sandbox, executes a validation command, and cleans it up. HTTP 200 alone is not success; unconfirmed creation, execution, or cleanup exits nonzero with backend diagnostics retained. The CLI does not retry these operations automatically.

Queries require `workspace.view`. Updates, checks, and tests require `workspace.update` and a saved, Workspace-bound context. Updates also require `workspace.view` and the get capability for independent readback verification. Hidden key values remain unverified; only their configured status is visible. Saved settings apply to new tasks; existing tasks and MCP connections retain their configuration snapshots.

Limits include `timeout_sec`, `idle_ttl_sec`, `max_sessions`, `cpus`, `memory_mb`, `pids_limit`, and `workspace_quota_mb`. Inspect `limit_caps`, per-backend `limit_capabilities`, and deployment policy with `config get`. The CLI checks advertised support and upper bounds while Core remains authoritative for configuration semantics. `--dry-run` neither saves nor creates a test sandbox and does not guarantee full server-side validation. These commands require matching Core routes and Box SDK/Runtime support; older versions return capability or compatibility errors.

```sh
lbctl sandbox status
lbctl sandbox sessions
lbctl sandbox errors
```

Sandbox queries read the current Workspace: status requires `resource.view`; sessions and errors require `audit.view`. Managed sandboxes use server-side admission checks. Status includes `enabled`, `available`, and server-provided explanations.

Core declares the corresponding capabilities; output supports `-o json` and `-o yaml`. `enabled: false` means disabled; `enabled: true` with `available: false` means unavailable.

## Operation commands

New commands use `POST /api/v1/operations/<id>`, with SSE at `/<id>/stream`. They check `operation.invoke` (plus `operation.stream` for streams). Older Core versions return `incompatible` (exit 8), identifying the required Cloud/new Core capability, without guessing alternative routes. Existing commands retain their v4.11.8-compatible routes, including `monitoring embedding-calls`, `mcp-server resource-templates`, and file-based marketplace installation.

```sh
lbctl op list --domain agent --search run
lbctl op describe agent.create
lbctl op call agent.list -o json
printf '%s' '{"agent_data":{"name":"demo","config":{}}}' | lbctl agent create --file - --yes
lbctl agent runs list <agent-uuid>
lbctl monitoring executions --file filters.json
lbctl op stream <streaming-operation-id> --file input.json --yes
lbctl agent debug <agent-uuid> --file debug.json --yes
```

`op call` accepts an optional JSON/YAML object via `--file path` or `--file -`, defaulting to `{}`; `op stream` requires `--file`. Generic calls obtain risk and execution mode from `op describe`; catalog-hidden IDs already registered by this CLI use their built-in contracts. Unknown hidden IDs remain unavailable. Writes and high/dangerous-risk operations require interactive confirmation or `--yes`. Friendly commands use their registered operation contracts and the same confirmation rule. Writes require a saved, Workspace-bound context. Stdin cannot simultaneously supply a request body, an API key, or a confirmation answer. Streams emit each event immediately in the selected format, then a completion result; errors or disconnection before `done` fail without retrying.

Friendly positionals populate Core input fields; remaining fields go in `--file`. Bodies are **operation inputs**, not legacy HTTP payloads: agent creation uses `{"agent_data":{...}}`, debug uses `{"payload":{...}}`, and `monitoring execution <id>` needs `{"source":"agent"}` or another Core execution source. Conflicting positional/body IDs fail locally. `--help` lists input field names; `op describe` supplies schemas for catalog-visible operations. All commands support concise output and `-o json|yaml`. Every Core request carries the canonical `X-LangBot-Client-Command` name.

### Command reference

`lbctl schema --summary` is the complete offline index. Use `lbctl schema --command "agent create"` for flags/safety requirements and each group's `--help` for positionals.

| Group | Commands |
| --- | --- |
| Connection/deployment | `context`, `whoami`, `capabilities`, `status`, `doctor`, `version`, `install`, `adopt`, `start`, `stop`, `restart`, `upgrade`, `uninstall`, `logs` |
| Generic | `op list/describe/call/stream` |
| Agent | `list/get/create/update/delete/metadata`, `runs list/events`, `debug` (stream) |
| Plugin | Existing list/get/config/logs/delete/upgrade; `permissions get/confirm`, `search`, `readme`, `debug-info`, `config-file upload/delete`, `github releases/assets`, `page-action`, `install local/github/marketplace` |
| Bot | Existing CRUD; `routes preview/status`, `message send/test`, `logs`, `runtime`, `admins list/add/delete`, `group-filter preview/apply`, `setup <platform> start/status/cancel` |
| Monitoring | Existing messages/llm-calls/tool-calls/embedding-calls/sessions/errors/message/session; `overview`, `executions`, `execution`, `tokens`, `inflight`, `feedback list/stats`, `export <kind>`, `session analysis/reset` |
| Pipeline | Existing CRUD/copy/apply/extensions/run; `metadata`, `defaults`, `chat history/reset/connections/broadcast`, `migration preview/execute` |
| Model | `list/get/create/update/delete/test --type llm\|embedding\|rerank\|asr\|tts`, `default get`, `starred get/set` |
| Provider | Existing CRUD/scan-models; `models count`, `codex start/poll/status/cancel/disconnect` |
| Skill | Existing CRUD/files/preview/install; `directory scan/import`, `reload`, `package get`, `github preview`, `zip preview` |
| MCP server | Existing CRUD/resources/resource-templates/logs/test; `runtime` |
| Knowledge | Existing knowledge-base/engine/parser commands; `knowledge-base migration preview/execute` |
| Discovery | `adapter list/get`, `requester list/get`, `tool list/get`, `extension list`, `stats` |
| API key/webhook | `api-key list/get/create/update/delete`, `webhook list/get/create/update/delete` |
| Settings | `governance get/update`, `operation-logs list/filters/export` |
| Workspace | `current/get`, `language get/update`, `member list/update/remove`, `invitation create/list/revoke` |
| Assistant | `list/get/create/turn/run/stream/stop` |
| File | `file image upload/get` |
| System | `storage-analysis`, `model-availability`, `model-recommended`, `plugin-status` |
| Other existing groups | `task`, `sandbox`, `api`, `raw`, `schema`, `completion` |

Bot setup platforms: `dingtalk`, `lark`, `qqofficial`, `slack`, `wecombot`, `weixin`; QR/authorization information is printed as text. Export kinds: `messages`, `sessions`, `errors`, `llm-calls`, `embedding-calls`, `feedback`. `assistant run` invokes `assistant.run.start`; `assistant stream` uses SSE. ASR/TTS commands probe `/api/v1/provider/models/asr|tts` before enabling CRUD/test rather than using the operation catalog. Unfiltered `model list` keeps its existing three-type aggregate.

### Plugin permissions

```sh
lbctl plugin install marketplace <author> <name> --version 1.2.3 --wait
lbctl plugin install local --file plugin.lbpkg --confirm-permissions
lbctl plugin install github --file github.json --confirm-permissions
lbctl plugin permissions get <author> <name>
lbctl plugin permissions confirm <author> <name> --yes
lbctl plugin permissions confirm <author> <name> --capability storage.plugin --operation agent.list --yes
```

Confirmation grants all declared permissions unless individual IDs are supplied. Compatibility-mode plugins always receive the complete declared set. Core checks each grant against the API key's own permissions. `--confirm-permissions` implies waiting for successful installation, confirming, and verifying that the plugin is enabled. It requires `plugin.permissions.confirm_api_key` and `operation.invoke`; failure never proceeds to confirmation. Local/GitHub identity comes from the package manifest, not the filename/repository name. GitHub confirmation downloads the selected public release asset to inspect its manifest without sending the Workspace key to GitHub.

### Core access boundaries

The catalog is filtered by permission, principal, and exposure; it is not every friendly command. Catalog-hidden operations already known to the CLI can still be invoked using their fixed contracts; Core remains the authorization authority. Workspace members/invitations, provider Codex, Assistant, pipeline migration, and knowledge-base migration automatically use a member session, including equivalent `op call/describe/stream` IDs. Other commands default to API keys even when a member token is configured; `--as-member` explicitly selects the member session. Knowledge migration `preview` uses `/api/v1/knowledge/migration/status`.

### Member sessions

```bash
lbctl auth login --email you@example.com
lbctl auth login --email you@example.com --password-stdin < password-file
lbctl auth login --email you@example.com --totp
lbctl auth set-token --stdin < token-file
lbctl workspace member list
lbctl op list --as-member
lbctl auth logout
```

Login requires a saved context. Password and TOTP/recovery-code prompts hide input; `--totp` completes Core's challenge, including when the password comes from stdin and a controlling terminal is available. Tokens are stored as the optional `user_token` field in the current context's credential-bearing configuration file, atomically written with mode `0600`. API keys remain environment references. `LANGBOT_USER_TOKEN` overrides the saved token. Tokens are masked in output; never pass passwords or tokens as command-line arguments.

Cloud disables password login (`password_login_disabled`). Sign into the target Workspace's WebUI, open browser developer tools → Application/Storage → Local Storage for that origin, and copy the `token` value. Paste it into `lbctl auth set-token --stdin` (finish stdin with EOF), or supply it through a protected stdin pipe. Treat it as a password and do not share it in logs or screenshots. The CLI does not refresh sessions: when the token expires or Core rejects a member request with 401/403, the error is `requires_member_session` (exit 3); log in again or import a fresh WebUI token. Missing tokens fail before any network request. `auth logout` removes the saved token; also unset `LANGBOT_USER_TOKEN` if exported. It does not revoke other browser sessions. Member permissions and Workspace binding are checked against `/api/v1/workspaces/current`; API-key-only system discovery endpoints are not used for member operation calls.

### Binary files

Binary uploads keep the existing multipart routes. `file image get <key> -O image.png` downloads original bytes from `/api/v1/files/image/<key>`, including on Core v4.11.8 without `operation.invoke`. Without `-O`, it writes binary stdout only when stdout is not a terminal. This command emits no JSON success envelope.

With Core's `operation.binary_base64` support, `op call file.image.get --file request.json` preserves the base64 fields as JSON (`output_schema` marks them with `contentEncoding: base64`). Add `--decode-binary content -O image.png` to decode the named top-level field into a file instead. Both flags are required together. The system wizard is excluded because it manages WebUI onboarding state rather than a headless resource workflow.
