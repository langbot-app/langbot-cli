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

The standalone LangBot command-line tool, provided as `lbctl`. It manages Workspace resources over HTTP, with multiple contexts, identity and capability discovery, resource management, RAG configuration discovery, Pipeline extensions and test runs, runtime records and Sandbox diagnostics, asynchronous task waiting, and installation, adoption, lifecycle management, and upgrades for local Docker Compose deployments.

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

The Go baseline is 1.26. Run the compiled binary directly; local deployment management uses Docker Compose.

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

Built-in `install` supports independent Compose projects and host port mappings. Defaults remain `--project langbot-lbctl`, `--plugin-debug-port 5401`, and `--adapter-ports 2280-2285:2280-2285`. Use a different `--dir`, `--project`, and HTTP `--port` for each installation. Set `--plugin-debug-port 0` and `--adapter-ports none` to disable those host publications for headless instances:

```sh
lbctl install --version <vX.Y.Z> --dir /absolute/path/regression \
  --project regression --port 15301 --profile all \
  --plugin-debug-port 0 --adapter-ports none --dry-run
```

Remove `--dry-run` to install. For plugin development or inbound adapters, select unused host ports instead:

```sh
lbctl install --version <vX.Y.Z> --dir /absolute/path/second \
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

`lbctl upgrade` upgrades the LangBot service images. CLI-installed and adopted deployments use the same commands:

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
