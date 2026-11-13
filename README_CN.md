<div align="center">

<a href="https://langbot.app">
<img src="https://raw.githubusercontent.com/langbot-app/LangBot/master/res/logo-blue.png" width="96" alt="LangBot"/>
</a>

<h1>LangBot CLI</h1>

<p>从终端管理 LangBot Workspace 与自部署实例。</p>

[English](README.md) / 简体中文 / [繁體中文](README_TW.md) / [日本語](README_JP.md) / [Español](README_ES.md) / [Français](README_FR.md) / [한국어](README_KO.md) / [Русский](README_RU.md) / [Tiếng Việt](README_VI.md)

[![Release](https://img.shields.io/github/v/release/langbot-app/langbot-cli)](https://github.com/langbot-app/langbot-cli/releases/latest)
[![CLI checks](https://img.shields.io/github/actions/workflow/status/langbot-app/langbot-cli/ci.yml?branch=main&label=checks)](https://github.com/langbot-app/langbot-cli/actions/workflows/ci.yml)
[![Go](https://img.shields.io/badge/Go-1.26-00ADD8?logo=go&logoColor=white)](go.mod)
[![License](https://img.shields.io/github/license/langbot-app/langbot-cli)](LICENSE)

<a href="https://langbot.app">官网</a> ｜
<a href="https://github.com/langbot-app/LangBot">LangBot 主库</a> ｜
<a href="https://github.com/langbot-app/langbot-cli/releases">版本发布</a> ｜
<a href="https://github.com/langbot-app/langbot-cli/issues">问题反馈</a>

</div>

<p align="center">
<a href="#installation">安装</a> ·
<a href="#connection">连接实例</a> ·
<a href="#deployment">本机部署</a> ·
<a href="#resources">资源管理</a> ·
<a href="#automation">自动化</a> ·
<a href="#source">源码构建</a>
</p>

---

LangBot 的独立命令行工具，二进制名为 `lbctl`。它通过 HTTP 管理 Workspace 资源，支持多 context、身份与能力发现、资源管理、RAG 配置发现、Pipeline 扩展与试运行、运行记录和 Sandbox 诊断、异步任务等待，以及本机 Docker Compose 部署的安装、接管、启停和升级。

当前源码需配套包含以下能力的 LangBot Core：

- API Key 鉴权的 `/api/v1/system/context` 与 `/api/v1/system/capabilities`。
- Bot、Pipeline、Knowledge Base、Provider、Model、Plugin、Skill、MCP Server 与任务查询接口。
- Provider/Model 查询的服务端脱敏参数 `include_secret=false`。

`lbctl` 根据 Core 声明的 capability、Workspace 绑定和权限执行操作。

<a id="installation"></a>

## 安装

macOS、Linux 或 Windows Git Bash：

```sh
curl -fsSL https://download.langbot.app/install.sh | sh
```

Windows PowerShell：

```powershell
irm https://download.langbot.app/install.ps1 | iex
```

安装脚本会自动选择当前平台的二进制并校验 SHA-256。Shell 脚本默认安装到 `~/.local/bin`；PowerShell 脚本默认安装到 `%LOCALAPPDATA%\Programs\lbctl` 并加入用户 `PATH`。

下载域名由 Cloudflare Workers 分发，安装脚本、二进制和校验文件均从镜像获取。发行包同步自 [GitHub Releases](https://github.com/langbot-app/langbot-cli/releases)，也可以在那里直接下载原始文件。设置 `LBCTL_VERSION` 可安装指定的已发布版本。

<a id="source"></a>

## 源码构建

```sh
make build
./bin/lbctl version
./bin/lbctl --help
```

Go 基线为 1.26。编译后的二进制可直接运行；本机部署管理使用 Docker Compose。

<a id="connection"></a>

## 配置远端实例

API Key 由环境变量或单次 stdin 提供。配置路径按 `--config` → `LANGBOT_CONFIG` → `~/.config/langbot/config.yaml` 选择，只保存环境变量名，不保存 Key 明文。`context list`、`context show`、`status` 和 `doctor` 会显示当前配置路径；`context show` 不带名称时查看当前环境。离线显示“凭据可用”仅说明变量已设置，实际鉴权用 `context check` 或 `whoami` 验证。

先在目标 LangBot 管理页面完成初始化，再通过 API Key 管理创建 Workspace API Key。需要鉴权的命令会检测缺失凭据，提示实际使用的环境变量、隐藏输入的 PowerShell/Bash 设置示例，以及保留原连接选项运行 `whoami` 的验证步骤。JSON/YAML 提供 `error.reason` 和 `error.guidance`；错误类型仍为 `auth`，退出码为 3。服务端拒绝已有 Key 与环境变量未设置会分别提示；CLI 不自动等待输入，也不保存 Key。

先查询 Key 对应的 Workspace：

```sh
export LANGBOT_PRODUCTION_API_KEY='<api-key>'

LANGBOT_API_KEY="$LANGBOT_PRODUCTION_API_KEY" \
  lbctl --endpoint https://bot.example.com/langbot whoami
```

将返回的 `workspace_uuid` 固定到 context：

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

管理多个目标：

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

连接参数按以下优先级解析，并校验所选目标与凭据。

| 参数 | 优先级 |
|---|---|
| Context | `--context` → `LANGBOT_CONTEXT` → 当前 context |
| Endpoint | `--endpoint` → `LANGBOT_ENDPOINT` → context 配置 |
| Key | `--api-key-stdin` → `LANGBOT_API_KEY` → context 的环境变量引用 |
| 超时 | `--timeout` → `LANGBOT_TIMEOUT` → context 配置 → 30s |

`--api-key-stdin` 用于单次调用，不能与 `--file -` 同时使用。临时 endpoint 只允许受控读取；资源写入必须使用已保存且绑定 Workspace 的 context。

提供 Key 后应关闭 stdin 输入流。凭据输入等待使用解析后的 `--timeout` / `LANGBOT_TIMEOUT` / context 超时（默认 30s），与单次 HTTP 请求分别计时；超时返回 `network`、`reason: stdin_timeout` 和退出码 7，不发送鉴权请求。

<a id="deployment"></a>

## 本机部署与升级

可以发现并登记多个本机 LangBot Compose 实例：

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

`context list` 离线列出已保存的 context 及本机部署登记，展示匹配的 Compose project；加 `--discover` 才会只读检查本机 Docker Compose 项目，不自动接管或保存 context。无法读取或使用多个 Compose 文件的项目会列入 `skipped_projects`。发现后，通过 `adopt` 显式登记，每个实例使用不同的记录目录和 Compose project。登记后，`status`、`doctor`、启停、日志和升级会按当前 context / endpoint 匹配对应的本机记录，匹配不唯一时拒绝自动选择。

云端和本地实例统一用 `context use <name>` 切换，用 `--context <name>` 临时选择。切换不要求实例在线，临时选择不改变已保存的当前 context。显式参数和环境变量保持原有优先级。旧的单部署定位记录会在下次登记时迁移，保留原有本机兜底目录；接管另一实例不会改变当前 context。新登记格式不能由旧版 CLI 读取。

内置 `install` 支持独立 Compose project 和宿主机端口映射。默认仍为 `--project langbot-lbctl`、`--plugin-debug-port 5401` 和 `--adapter-ports 2280-2285:2280-2285`。安装多个实例时，为每套部署指定不同的 `--dir`、`--project` 和 HTTP `--port`。无须插件调试或入站适配器的测试实例，可关闭这些宿主机端口发布：

```sh
lbctl install --version <vX.Y.Z> --dir /absolute/path/regression \
  --project regression --port 15301 --profile all \
  --plugin-debug-port 0 --adapter-ports none --dry-run
```

去掉 `--dry-run` 即可安装。需要插件调试或入站适配器时，可选择其他空闲宿主机端口：

```sh
lbctl install --version <vX.Y.Z> --dir /absolute/path/second \
  --project second --port 15302 --plugin-debug-port 15402 \
  --adapter-ports 12280-12285:2280-2285
```

适配器映射支持逗号分隔的 `HOST:CONTAINER` 或等长端口范围，容器端口限于 `2280-2285`。可以只发布需要的端口，例如 `--adapter-ports 12280:2280,12283:2283`。预检只检查选中的宿主机端口和 Compose project，并拒绝重复映射。容器内部服务端口保持不变，生成的调试地址随宿主机调试端口同步调整。端口配置随部署记录保存，升级时保留；旧部署记录继续兼容。

`adopt` 将已有 Compose 部署登记为本机受管部署，不改变它的服务配置：

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

安装新的本机部署，或卸载部署并保留数据：

```sh
lbctl install --version <vX.Y.Z> --profile basic --port 5300
lbctl uninstall --yes
```

`lbctl upgrade` 升级 LangBot 服务镜像。CLI 安装和接管的部署都可使用相同命令：

```sh
lbctl upgrade --version <newer-vX.Y.Z> --dry-run -o json
lbctl upgrade --version <newer-vX.Y.Z> --yes -o json
```

接管部署升级仅替换 `langbot`、`langbot_plugin_runtime` 和已有 `langbot_box` 的镜像；保留 Compose 的其他内容，包括端口、挂载、环境配置、注释和其他服务。升级先拉取并验证目标镜像，再停服，备份数据、Compose、存在的项目 `.env` 和实际部署记录，最后启动并验证版本。失败后的备份和升级现场会保留，可通过 `status`、`doctor` 检查。

自动升级支持直接声明 `rockchin/langbot:<tag>` 的服务，数据完整位于 Compose 目录的 `data` 绑定目录，且使用本地 SQLite、Chroma 和 local storage。升级前会校验 Compose 配置、备份范围和版本；目标版本高于当前版本。`--dry-run` 显示升级计划，执行升级时验证目标镜像。接管后手动改变 Compose，需要再次执行相同目标的 `adopt`，明确确认新的资产摘要。

本机生命周期作用于部署实例；Bot、Pipeline、Skill 等资源仍通过下面同一套命令管理 context 对应的 Workspace。

已有实例完成 context 和 API Key 配置后即可管理资源，无须先接管 Docker。`adopt` 用于启停、部署日志和升级等本机管理。

人工使用时，帮助按用途分组，显示适用选项、必填项和常用示例。列表优先展示名称和状态，保留完整 ID；长名称在表格中缩略，过宽的表格按条目显示（可用 `COLUMNS` 指定显示宽度）。`-o json` / `-o yaml` 保留完整名称和结构化数据。资源列表支持 `--filter` 按名称或 ID 包含文本筛选，不区分大小写；这是客户端筛选，不减少服务端传输，也不自动选择重名资源。

```sh
lbctl bot list --filter my-bot
lbctl bot get <bot-id>             # 查看 use_pipeline_uuid，再用 pipeline get 查看关联配置
lbctl model list --type llm --provider <provider-id>
lbctl model list --filter deepseek -o json
lbctl bot update --help            # 查看最小请求体示例和预检说明
lbctl schema --summary             # agent 先读简要命令索引
lbctl schema --command bot         # 按需读取整个命令组
lbctl schema --command 'bot update'
```

Shell 补全读取已保存的 context 和静态枚举，不请求远端资源。人工终端中的耗时命令会在 stderr 显示等待时间；重定向输出和 JSON/YAML 模式保持安静。操作结果区分预检、已提交、已验证和隐藏字段未验证；部分结果及退出码保持原有语义。适用时，`meta.next_steps` 提供保留连接选择的下一步命令参数，人工输出会显示可复制命令。

<a id="resources"></a>

## 管理资源

复杂请求体使用 JSON 或 YAML 文件，具体字段与对应 LangBot HTTP 接口一致。`--file -` 表示从 stdin 读取。

### Bot 与 Pipeline

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

`pipeline apply` 的请求体包含 `uuid` 时更新，否则创建。

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

上传文档、提交入库并等待任务结束：

```sh
lbctl knowledge-base ingest <uuid> --file ./document.pdf --wait
```

可重复指定 `--file`，或用 `--dir` 递归导入整个目录；二者也可合用，重复路径只提交一次：

```sh
lbctl knowledge-base ingest <uuid> --file ./guide.pdf --file ./faq.md --wait
lbctl knowledge-base ingest <uuid> --dir ./documents --dry-run
lbctl knowledge-base ingest <uuid> --dir ./documents --wait
```

目录扫描跳过点开头的文件、目录和符号链接；每个文件受现有 10 MiB 上传限制约束（含请求元数据）。CLI 先检查全部本地路径，再按顺序上传和提交；`--wait` 逐个等待任务，`--wait-timeout` 对每个文件分别计时。JSON/YAML 的批量结果在 `files` 保留各文件的状态、文件 ID、任务 ID 和错误，并在 `summary` 汇总；人工输出显示同样的逐文件结论。不加 `--wait` 只表示任务已提交。

已确认的单文件失败会继续处理其他文件；提交结果未知、等待未完成、鉴权或权限等目标级错误会停止后续提交，保留已完成和未开始项，并返回非零退出码。重复执行会重新上传，当前不做跨次去重或自动重试。ZIP 仍可按单文件方式提交，但 Core 只返回其中一个子任务 ID；批量 `--wait` 遇到 ZIP 会在上传前拒绝，请先解压后使用 `--dir`。

### Provider 与 Model

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

Model 类型支持 `llm`、`embedding` 和 `rerank`。Provider/Model 输出使用服务端脱敏和客户端字段筛选；模型扫描提供结构化结果。

### Plugin 与 Skill

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

Skill 安装的 `--dry-run` 调用服务端预览，不产生安装。Plugin 安装、升级和删除可用 `--wait` 跟踪异步任务。

使用 `lbctl plugin permissions get <author> <name>` 查看插件申请的权限、风险、授权情况和待确认状态。待确认时会显示目标 Workspace 和 WebUI 扩展页面入口，由有权管理插件的账户审阅。审批后再次运行命令确认状态。JSON/YAML 输出包含相同信息，需要 Core 的 `plugin.permissions.get` 能力。

GitHub 插件安装请求包含 `owner`、`repo`、`release_tag`，以及 `release_id`/`asset_id` 或 `asset_url`。链接输入会由 CLI 查询 GitHub release 并转换成与 WebUI 一致的 ID 请求；GitHub 查询与 Workspace 鉴权分离，dry-run 执行前置检查。示例：

```yaml
owner: langbot-app
repo: langbot-plugins
release_tag: 0.0.1.alpha.1
asset_url: https://github.com/langbot-app/langbot-plugins/releases/download/0.0.1.alpha.1/langbot-team-GoogleSearch-0.1.2.lbpkg
```

插件配置更新在受限时间内等待重载后的回读；删除命令支持清理运行中和启动失败的插件安装。

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

## 写入与任务

写操作会依次核对已保存 context、Workspace 绑定、实际身份、权限、capability 和必要的资源前提。通用规则如下：

- `--dry-run` 执行前置检查；Skill 安装使用服务端预览。
- 删除必须显式提供 `--yes`。
- 可验证的写入会在完成后回读资源。
- 请求结果无法确认时返回 `result_unknown`，便于核查后决定下一步。
- 多步操作保留已经产生的资源或 `task_id`，便于跟踪执行进度。

Bot/Pipeline 读取保留普通配置，凭据使用 `***` 脱敏。创建、更新和 Pipeline 复制会回读核对可观察字段，不一致时返回 `verification_failed`。`verification.observable_fields_match` 表示核对结果，`unverified_fields` 列出被隐藏的字段；只有所有核对字段都可验证且一致时，`verified` 才为 true。Bot 更新会为完整的 `***` 占位符保留原凭据，无法安全恢复的占位符会在更新前被拒绝。

异步任务也可以单独查询：

```sh
lbctl task list
lbctl task list --type <type> --kind <kind>
lbctl task get <task-id>
lbctl task get <task-id> --wait --wait-timeout 10m
```

本地等待与服务端任务生命周期独立，可通过 `task get` 继续查询。

## 受控 API 入口

`api get/post` 调用内置 registry 已登记的操作：

```sh
lbctl api get '/api/v1/system/tasks?kind=plugin'
lbctl api post '/api/v1/knowledge/bases/<uuid>/retrieve' --file query.yaml
```

Provider/Model 管理、安装、测试和其他敏感操作必须使用专用命令。`raw` 仅保留 `/api/v1/system/info` 的兼容读取。

<a id="automation"></a>

## 输出与检查

默认输出便于人工阅读的简洁视图；自动化可使用 `-o json` 或 `-o yaml` 获取完整结构化结果。成功和错误结果写入 stdout，诊断信息写入 stderr。

`status` 默认显示当前环境摘要，仍保留身份不匹配与升级恢复警告。在同一条命令后加 `-o json` 或 `-o yaml`，可查看完整实例/Workspace ID 和部署路径。能力清单已获取不代表全部操作受支持，具体支持状态通过 `capabilities` 查看。

| 退出码 | 含义 |
|---|---|
| 0 | 成功 |
| 2 | 输入或配置错误 |
| 3 / 4 / 5 | 凭据错误 / 权限不足 / 未找到 |
| 6 | 前提未满足或冲突 |
| 7 | 网络、TLS、超时或取消 |
| 8 | 接口或协议不兼容 |
| 9 | 部署需要恢复处理 |
| 10 | 未分类服务端或内部错误 |

```sh
make check
make cross-build

# 对运行中的 Core 做只读诊断。
LBCTL_TEST_ENDPOINT=http://localhost:5300 \
  go test ./internal/integration -run TestLiveCore -v
```

`make cross-build` 生成 macOS、Linux、Windows 的 amd64/arm64 二进制及 `dist/checksums.txt`。

面向 Agent 的命令发现与 shell 补全：

```sh
lbctl schema
lbctl schema --command plugin.install.github
lbctl completion bash
```

## RAG 配置发现

这些命令需要 Core 声明对应 capability，并要求 `resource.view` 权限；可用 `-o json` 或 `-o yaml` 输出。

```sh
lbctl knowledge-engine list
lbctl knowledge-engine creation-schema author/engine -o yaml
lbctl knowledge-engine retrieval-schema author/engine -o json
lbctl knowledge-parser list --mime-type application/pdf
```

从引擎列表选择 `plugin_id`；creation schema 定义创建知识库的参数，retrieval schema 定义检索参数。随后使用 `knowledge-base create`、`ingest`、`retrieve` 命令完成入库和检索。

## Pipeline 扩展绑定

```sh
lbctl pipeline extensions get PIPELINE_UUID -o json
```

`pipeline extensions update PIPELINE_UUID --file extensions.yaml` **完整替换**绑定，以下八个字段全部必填。先用 `extensions get` 查看当前绑定与可选资源，在请求中保留所有仍需使用的项。更新成功后自动回读核对，`--dry-run` 检查参数与前置条件。

以下示例会清空四类显式绑定并关闭全部启用开关。

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

空数组配合 `enable_all_*: false` 表示不绑定该类扩展；设为 `true` 则启用该类全部可用扩展。插件项使用 `{author: author, name: plugin}`，MCP Server 填 `available_mcp_servers` 中的 `uuid`，Skill 填 `available_skills` 中的 `name`。MCP Resource 项例如 `{server_uuid: SERVER_UUID, uri: "docs://guide"}`。`mcp_resource_agent_read_enabled` 控制 Agent 列举、读取 MCP 资源及将绑定资源放入上下文。

更新要求已保存且绑定 Workspace 的 context、`resource.manage` 和 `resource.view` 权限，以及 Core 对应 capability。

## Pipeline 单轮试运行

```sh
lbctl pipeline run PIPELINE_UUID --message '请根据知识库回答这个问题' --timeout 2m -o json
```

试运行要求已保存且绑定 Workspace 的 context，以及 `runtime.operate` 和 `resource.view` 权限；会实际调用模型和配置的工具。每次新建会话，不复用之前的历史。返回回复、会话 ID、查询 ID 和可用的消息 ID；执行失败返回非零退出码。

Core 最多等待 60 秒，CLI 的 `--timeout` 控制单次 HTTP 请求等待时间。超时表示**执行状态未知**；后续可在 LangBot Web 管理界面的监控页（`/home/monitoring`）用返回的消息或会话标识查询运行记录。客户端超时时，也可按 Pipeline 和时间查近期记录。

服务端需要声明 `pipeline.run` capability；可用 `--dry-run` 检查前置条件。

## 应用运行记录诊断

通过当前 API Key 所属 Workspace 查询运行记录、失败消息详情及调用错误，需要 Core 对应 capability。

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

运行记录查询要求 `resource.view`。列表支持 `--limit 1..500` 和 `--offset`，Core 可进一步限制；`--start-time`、`--end-time` 使用 ISO 8601 时间。可用筛选条件见各命令 `--help`。

## Sandbox 配置与诊断

工作区沙箱配置使用 Core 的 `workspace.sandbox.get/update/reset/check/test` 能力。

```sh
lbctl sandbox config get --refresh
lbctl sandbox config reset --dry-run
lbctl sandbox config reset --yes
lbctl sandbox config update --file sandbox.yaml --dry-run
lbctl sandbox config update --file sandbox.yaml --yes
lbctl sandbox config check --file e2b.yaml --e2b-api-key-env E2B_API_KEY
lbctl --timeout 2m sandbox config test --file e2b.yaml --e2b-api-key-env E2B_API_KEY --yes
```

`reset --yes` 清除当前工作区保存的后端、E2B 连接及凭据和资源限制覆盖，随后独立回读验证。后续任务继承部署默认配置，需要 `workspace.update`、`workspace.view` 权限和 Core 的 `workspace.sandbox.reset/get` 能力。已有任务保留配置快照。

`sandbox.yaml` 示例：

```yaml
backend: docker
limits:
  timeout_sec: 60
  max_sessions: 2
```

保存请求支持 `backend: docker/e2b/nsjail`，以及可选的 `e2b` 和 `limits`。`e2b` 可填写 `api_url`、`sandbox_url`、`template` 和 `api_key`；推荐用 `--e2b-api-key-env` 注入密钥，或通过 `--file -` 从 stdin 提供请求体。不要同时从请求体和环境变量选项提供 E2B 密钥。Workspace 的 `--api-key-stdin` 仍然可用，但不能与 `--file -` 共用 stdin。查询只展示 `api_key_configured`，CLI 不把密钥写入 context。省略密钥时的保留规则及更换地址是否必须提供新密钥，由 Core 校验。

`e2b.yaml` 的检查/测试请求形如 `{"e2b":{"api_url":"https://api.example.com","sandbox_url":"https://sandbox.example.com","template":"base"}}`；也可以省略文件，检查或测试服务端当前的 E2B 配置。`check` 不保存、不创建沙箱；显式 `test --yes` 会消耗 E2B 配额并可能计费，由 Core 创建临时沙箱、执行验证命令并清理。HTTP 200 不代表测试成功；创建、执行或清理未确认成功都会返回非零退出码并保留后端诊断。CLI 不自动重试这些操作。

查询要求 `workspace.view`；保存、检查和测试要求 `workspace.update`，并沿用已保存且绑定 Workspace 的 context。保存还要求 `workspace.view` 和查询能力，以独立回读验证提交字段；隐藏密钥只确认是否已配置，不声称核对过实际值。保存仅影响新任务，既有任务和 MCP 连接保留原配置快照。

限制字段包括 `timeout_sec`、`idle_ttl_sec`、`max_sessions`、`cpus`、`memory_mb`、`pids_limit`、`workspace_quota_mb`。先用 `config get` 查看 `limit_caps`、各后端的 `limit_capabilities` 和平台策略；CLI 按服务端上限及支持能力检查，最终配置语义由 Core 决定。`--dry-run` 不保存或创建测试沙箱，也不代表完整的服务端业务校验已通过。本功能要求具备对应接口的 Core 及配套 Box SDK/Runtime；旧版本会返回能力或兼容性错误。

```sh
lbctl sandbox status
lbctl sandbox sessions
lbctl sandbox errors
```

Sandbox 查询只读当前 Workspace：状态要求 `resource.view`，会话和错误要求 `audit.view`。托管 sandbox 使用服务端准入检查；状态保留 `enabled`、`available` 和服务端状态说明。

需要 Core 声明对应 capability；支持 `-o json` 和 `-o yaml`。`enabled: false` 表示未启用，`enabled: true` 且 `available: false` 表示不可用。
