# langbot-cli

LangBot 的独立命令行工具，二进制名为 `lbctl`。它通过 HTTP 管理已运行实例中的 Workspace 资源，支持多 context、身份与能力发现、资源管理及异步任务等待，也支持本机 Docker Compose 部署的安装、接管、启停和升级。

当前源码需配套包含以下能力的 LangBot Core：

- API Key 鉴权的 `/api/v1/system/context` 与 `/api/v1/system/capabilities`。
- Bot、Pipeline、Knowledge Base、Provider、Model、Plugin、Skill、MCP Server 与任务查询接口。
- Provider/Model 查询的服务端脱敏参数 `include_secret=false`。

服务端未声明某项 capability 时，`lbctl` 会拒绝执行该操作。

## 安装

macOS、Linux 或 Windows Git Bash：

```sh
curl -fsSL https://github.com/langbot-app/langbot-cli/releases/latest/download/install.sh | sh
```

Windows PowerShell：

```powershell
irm https://github.com/langbot-app/langbot-cli/releases/latest/download/install.ps1 | iex
```

安装脚本会自动选择当前平台的二进制并校验 SHA-256。Shell 脚本默认安装到 `~/.local/bin`；PowerShell 脚本默认安装到 `%LOCALAPPDATA%\Programs\lbctl` 并加入用户 `PATH`。

## 源码构建

```sh
make build
./bin/lbctl version
./bin/lbctl --help
```

Go 基线为 1.26。运行二进制无需 Python、Node、Docker 或浏览器。

## 配置远端实例

API Key 由环境变量或单次 stdin 提供。配置默认保存在 `~/.config/langbot/config.yaml`，只保存环境变量名，不保存 Key 明文。

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

连接参数的优先级如下。显式来源无效时直接报错，不回退到其他目标或凭据。

| 参数 | 优先级 |
|---|---|
| Context | `--context` → `LANGBOT_CONTEXT` → 当前 context |
| Endpoint | `--endpoint` → `LANGBOT_ENDPOINT` → context 配置 |
| Key | `--api-key-stdin` → `LANGBOT_API_KEY` → context 的环境变量引用 |
| 超时 | `--timeout` → `LANGBOT_TIMEOUT` → context 配置 → 30s |

`--api-key-stdin` 用于单次调用，不能与 `--file -` 同时使用。临时 endpoint 只允许受控读取；资源写入必须使用已保存且绑定 Workspace 的 context。

## 本机部署与升级

`adopt` 将已有 Compose 部署登记为本机受管部署，不改变它的服务配置：

```sh
lbctl --endpoint http://127.0.0.1:5300 adopt \
  --file /absolute/path/docker-compose.yaml --project langbot --profile all
lbctl status
lbctl doctor
lbctl stop
lbctl start
```

`lbctl upgrade` 升级 LangBot 服务镜像，而不是 CLI 程序。CLI 安装和接管的部署都可使用相同命令：

```sh
lbctl upgrade --version <newer-vX.Y.Z> --dry-run -o json
lbctl upgrade --version <newer-vX.Y.Z> --yes -o json
```

接管部署升级仅替换 `langbot`、`langbot_plugin_runtime` 和已有 `langbot_box` 的镜像；保留 Compose 的其他内容，包括端口、挂载、环境配置、注释和其他服务。升级先拉取并验证目标镜像，再停服，备份数据、Compose、存在的项目 `.env` 和实际部署记录，最后启动并验证版本。失败后的备份和升级现场会保留，可通过 `status`、`doctor` 检查。

自动升级支持直接声明 `rockchin/langbot:<tag>` 的服务，数据完整位于 Compose 目录的 `data` 绑定目录，且使用本地 SQLite、Chroma 和 local storage。外部/命名数据卷、服务级 `env_file`、镜像变量/别名、build/extends/include 等无法确认备份或改写范围的配置会在停服前拒绝。目标版本必须高于当前版本；`--dry-run` 不验证目标镜像是否已发布。接管后手动改变 Compose，需要再次执行相同目标的 `adopt`，明确确认新的资产摘要。

本机生命周期作用于部署实例；Bot、Pipeline、Skill 等资源仍通过下面同一套命令管理 context 对应的 Workspace。

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

Model 类型支持 `llm`、`embedding` 和 `rerank`。Provider/Model 普通输出强制使用服务端脱敏，并在客户端再次限制可输出字段；模型扫描不会输出服务端 debug 数据。

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

GitHub 插件安装请求包含 `owner`、`repo`、`release_tag`，以及 `release_id`/`asset_id` 或 `asset_url`。链接输入会由 CLI 查询 GitHub release 并转换成与 WebUI 一致的 ID 请求；此查询不发送 Workspace API Key，dry-run 不访问 GitHub。示例：

```yaml
owner: langbot-app
repo: langbot-plugins
release_tag: 0.0.1.alpha.1
asset_url: https://github.com/langbot-app/langbot-plugins/releases/download/0.0.1.alpha.1/langbot-team-GoogleSearch-0.1.2.lbpkg
```

插件配置更新会在受限时间内等待重载后的回读，不重复提交写入。删除命令也可清理未成功启动的安装，不要求插件先出现在实时运行列表中。

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

- `--dry-run` 只执行前置检查，不声称服务端业务校验已经通过。
- 删除必须显式提供 `--yes`。
- 可验证的写入会在完成后回读资源。
- 请求结果无法确认时返回 `result_unknown`，不会自动重试。
- 多步操作保留已经产生的资源或 `task_id`，不会自动重复上传、安装或删除。

异步任务也可以单独查询：

```sh
lbctl task list
lbctl task list --type <type> --kind <kind>
lbctl task get <task-id>
lbctl task get <task-id> --wait --wait-timeout 10m
```

本地等待超时或取消不会取消服务端任务。

## 受控 API 入口

`api get/post` 只调用内置 registry 已登记的操作，不是任意 HTTP 客户端：

```sh
lbctl api get '/api/v1/system/tasks?kind=plugin'
lbctl api post '/api/v1/knowledge/bases/<uuid>/retrieve' --file query.yaml
```

Provider/Model 管理、安装、测试和其他敏感操作必须使用专用命令。`raw` 仅保留 `/api/v1/system/info` 的兼容读取。

## 输出与检查

默认输出便于人工阅读的简洁视图；自动化可使用 `-o json` 或 `-o yaml` 获取完整结构化结果。成功和错误结果写入 stdout，诊断信息写入 stderr。

| 退出码 | 含义 |
|---|---|
| 0 | 成功 |
| 2 | 输入或配置错误 |
| 3 / 4 / 5 | 凭据错误 / 权限不足 / 未找到 |
| 6 | 前提未满足或冲突 |
| 7 | 网络、TLS、超时或取消 |
| 8 | 接口或协议不兼容 |
| 10 | 未分类服务端或内部错误 |

```sh
make check
make cross-build

# 对运行中的 Core 做只读诊断；未设置变量时跳过。
LBCTL_TEST_ENDPOINT=http://localhost:5300 \
  go test ./internal/integration -run TestLiveCore -v
```

`make cross-build` 生成 macOS、Linux、Windows 的 amd64/arm64 二进制及 `dist/checksums.txt`。交叉构建成功不代表已经在对应系统完成实机验收。
