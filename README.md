# langbot-cli

LangBot 的独立命令行工具，二进制名为 `lbctl`。它通过 HTTP 管理已运行实例中的 Workspace 资源，支持多 context、身份与能力发现、资源管理及异步任务等待。本机部署的安装、启停和升级不在当前范围内。

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

## Sandbox 只读诊断

```sh
lbctl sandbox status
lbctl sandbox sessions
lbctl sandbox errors
```

Sandbox 查询只读当前 Workspace：状态要求 `resource.view`，会话和错误要求 `audit.view`。托管 sandbox 仍受服务端准入限制；状态保留 `enabled`、`available` 和服务端不可用原因，查询不会创建执行会话。

需要 Core 声明对应 capability；支持 `-o json` 和 `-o yaml`。`enabled: false` 表示未启用，`enabled: true` 且 `available: false` 表示不可用。
