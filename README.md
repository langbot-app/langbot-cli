# langbot-cli

LangBot 的独立命令行工具，二进制名为 `lbctl`。当前实现：多 context 配置、连接诊断、身份查询与统一输出。

## 构建

```sh
make build
./bin/lbctl version
./bin/lbctl --help
```

Go 基线为 1.26。

运行二进制无需 Python、Node、Docker 或浏览器。`make cross-build` 生成 macOS、Linux、Windows 的 amd64/arm64 二进制及 `dist/checksums.txt`；交叉构建通过不等于已在对应系统运行验收。

## 多 context 使用

```sh
lbctl context add dev --endpoint http://localhost:5300 --api-key-env LANGBOT_DEV_API_KEY
lbctl context add production --endpoint https://bot.example.com/langbot \
  --api-key-env LANGBOT_PRODUCTION_API_KEY --timeout 15s
lbctl context use dev
lbctl context list -o table
lbctl context show production
lbctl status
lbctl --context production version --server
lbctl context check --all
```

环境变量中的 Key 由调用环境提供。配置默认保存在 `~/.config/langbot/config.yaml`，可用 `--config` 指定其他文件；只存环境变量引用，不保存 Key。`context list/show` 离线运行，不读取所引用的 Key；`add` 不自动设置默认项。

```sh
lbctl context update production --timeout 30s --expect-workspace workspace-a
lbctl context update production --clear-credential --clear-binding
lbctl context remove dev --yes
```

删除当前项需 `--yes`，并清空默认选择。`--expect-workspace` 会在 `whoami` 和 `context check` 中与服务端返回的 Workspace 比较，不一致时返回前提冲突且不会自动更新。更新为不同 endpoint 时，旧凭据引用和 Workspace 约束会清除；需要保留时必须在本次更新中显式重新指定。

连接参数按以下优先级生效，显式来源无效时直接报错：

| 参数 | 优先级 |
|---|---|
| Context | `--context` → `LANGBOT_CONTEXT` → 保存的默认项 |
| Endpoint | `--endpoint` → `LANGBOT_ENDPOINT` → context 配置 |
| Key | `--api-key-stdin` → `LANGBOT_API_KEY` → context 的环境变量引用 |
| 超时 | `--timeout` → `LANGBOT_TIMEOUT` → context 配置 → 30s |

单次调用固定目标与凭据，后续 `context use` 不影响已开始的请求。覆盖 endpoint 导致目标变化时，不继承原目标的凭据和 Workspace 约束；原 context 配有凭据时，必须为本次请求显式提供新的 Key。`context check --all` 拒绝全局目标、Key 和 context 覆盖，以各项自身配置检查，最多并发 4 项，保留所有结果；任一项失败，整体退出码非零。

Key 可由上游程序通过管道传入 `--api-key-stdin`，不提供明文 Key 命令行参数。该输入不能与 `--file -` 同用。

## 当前 HTTP 边界

`status` 和 `version --server` 使用公开的 `GET /api/v1/system/info`；`context check` 还会请求 API Key 鉴权的 `GET /api/v1/system/context`。公开诊断成功不代表 Key 有效：`context check` 会分别报告服务可达、基础诊断和身份结果，认证失败时返回非零退出码。`whoami` 输出服务端确认的实例、Workspace、Key 标识和权限；`capabilities` 在服务端尚未提供能力契约时返回 unknown，并以退出码 6 表示前提未满足。

```sh
lbctl raw GET /api/v1/system/info
```

`raw` 目前仅允许这个明确登记的操作，并按字段白名单返回安全投影；其他路径、方法、请求体均拒绝。HTTP 写入尚未开放。客户端保留反向代理前缀，拒绝自动重定向，不自动重试。详见 [当前 HTTP 契约](contracts/system-info.md)。

## 输出与验证

默认 JSON，也支持 `-o table` / `-o yaml`。成功和错误均输出到 stdout，诊断输出到 stderr。JSON 使用 `{ok,data,meta}` 或 `{ok:false,error,meta}`；批量失败仍保留 `data.checks`。

| 退出码 | 含义 |
|---|---|
| 0 | 成功 |
| 2 | 输入或配置错误 |
| 3 / 4 / 5 | 凭据错误 / 权限不足 / 未找到 |
| 6 | 前提未满足或冲突 |
| 7 | 网络、TLS、超时或取消 |
| 8 | 当前接口或协议不兼容 |
| 10 | 未分类服务端或内部错误 |

```sh
make check
make cross-build
# 对运行中的 Core 做只读诊断；未设置该变量时此项测试明确跳过。
LBCTL_TEST_ENDPOINT=http://localhost:5300 go test ./internal/integration -run TestLiveCore -v
```
