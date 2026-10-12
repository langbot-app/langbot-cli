package output

// Labels apply only to human rendering. Protocol keys and resource names stay unchanged.
var humanFieldLabels = map[string]string{
	"uuid": "ID", "id": "ID", "context": "环境", "contexts": "环境列表",
	"workspace_uuid": "Workspace ID", "expected_workspace_uuid": "绑定的 Workspace",
	"instance_uuid": "实例 ID", "api_key_id": "API Key ID", "credential_source": "凭据来源",
	"dir": "部署目录", "endpoint": "地址", "current": "当前", "name": "名称",
	"server_version": "Core 版本", "server_edition": "版本类型", "for_version": "Core 版本",
	"http_status": "HTTP 状态", "server_code": "服务端错误码", "request_id": "请求 ID",
	"enable": "启用", "enabled": "启用", "status": "状态", "state": "状态", "health": "健康检查",
	"bots": "Bot 列表", "pipelines": "Pipeline 列表", "providers": "模型供应商", "models": "模型列表",
	"bases": "知识库", "files": "文件列表", "servers": "MCP Server 列表", "plugins": "插件列表",
	"skills": "Skill 列表", "tasks": "任务列表", "operations": "能力列表", "checks": "检查结果",
	"adapter": "适配器", "use_pipeline_uuid": "Pipeline ID", "is_default": "默认",
	"reachable": "连接可达", "diagnostic_ok": "检查通过", "identity": "身份", "capabilities": "能力清单",
	"reason": "说明", "error": "错误", "type": "类型", "service": "服务",
	"created_at": "创建时间", "updated_at": "更新时间", "version": "版本", "author": "作者",
	"requester": "协议", "llm_count": "LLM 数量", "embedding_count": "Embedding 数量", "rerank_count": "Rerank 数量",
	"provider_uuid": "供应商 ID", "context_length": "上下文长度", "task_type": "任务类型", "kind": "任务类别",
	"knowledge_engine_plugin_id": "知识引擎", "file_name": "文件名", "extension": "文件类型",
	"display_name": "显示名称", "description": "说明", "mode": "模式", "operation": "操作", "action": "操作",
	"changed": "已变更", "dry_run": "预检", "verified": "完整验证", "verification": "写入验证",
	"observable_fields_match": "可观察字段一致", "unverified_fields": "未验证字段", "checked_fields": "已核对字段", "scope": "核对范围",
	"saved": "已保存", "effective": "本次生效", "instructions": "说明内容", "permissions": "权限", "schema_version": "Schema 版本",
}
