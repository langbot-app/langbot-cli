# `/api/v1/system/info` 诊断契约

契约来源：LangBot 源码快照 `ec63978e`，`src/langbot/pkg/api/http/controller/groups/system.py` 的 `SystemRouterGroup`，以及 `src/langbot/pkg/api/http/controller/group.py` 的 `RouterGroup.success`。

## 请求

```http
GET /api/v1/system/info
```

这是公开的系统信息接口（服务端注册为 `AuthType.NONE`）。CLI 可以带 `X-API-Key`，但服务端对该接口不会据此完成 API Key 身份认证，所以成功响应不能证明 Key 有效。CLI 只允许调用这个固定的 GET 操作，不提供任意路径、任意方法或原始写请求。

服务根地址可以包含反向代理路径前缀，例如 `https://example.test/langbot`；客户端直接拼接已规范化的服务根地址和固定路径，保持该前缀。

## 响应

服务端成功响应使用以下包络：

```json
{
  "code": 0,
  "msg": "ok",
  "data": {
    "version": "4.10.10",
    "edition": "..."
  }
}
```

CLI 要求 `code` 为数字 `0` 或字符串 `"0"`，并要求 `data` 为对象。HTTP 非 2xx 或业务 `code` 非零都会转为统一错误；服务端 `msg` 不回显到 CLI 错误，避免把不可信响应内容带入输出。

`Raw` 的返回仍保留 `code`、`msg`、`data` 三个字段，但它是安全投影，不是服务端原始 JSON。`data` 只保留下列已确认的标量字段：

- `version`、`edition`、`wizard_status`（字符串）；
- `debug`、`enable_marketplace`、`disable_models_service`、`mcp_stdio_enabled`（布尔值）。

其他字段（包括 `wizard_progress`、网络地址、限制配置和邀请能力的嵌套数据）不返回，避免在没有逐字段脱敏契约时原样暴露。若已知响应字段包含本次 API Key，也会从安全投影中移除。

`Info` 只返回 `version` 和 `edition`。它同样只表达系统公开信息，不表达认证身份、Workspace、权限或能力。

## 请求 ID 与错误

服务端可能在 `X-Request-Id` 响应头或 JSON 的 `request_id` 字段提供请求 ID。CLI 只接受有限长度且不含控制字符的值；服务端错误正文和 HTML 不进入错误消息。`code` 仅保留安全的数字或有限字符字符串，避免把 API Key 或其他响应内容写回错误对象。

CLI 错误的 `http_status` 保留实际 HTTP 状态码，统一错误分类为 `auth`（401）、`permission`（403）、`not_found`（404）、`network`（408/429）、`incompatible`（重定向或协议错误）或 `server`（其他服务端错误）。客户端不自动重试，也不自动跟随重定向。

连接超时和取消使用固定的可操作提示；证书验证失败仍属于 `network`，但错误 `type` 为 `tls_error`，只提示证书验证失败，不回显底层 TLS 或证书文本。
