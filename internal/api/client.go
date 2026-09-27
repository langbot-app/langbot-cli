package api

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/langbot-app/langbot-cli/internal/endpoint"
	"github.com/langbot-app/langbot-cli/internal/result"
)

const (
	infoPath            = "/api/v1/system/info"
	contextPath         = "/api/v1/system/context"
	capabilitiesPath    = "/api/v1/system/capabilities"
	botsPath            = "/api/v1/platform/bots"
	pipelinesPath       = "/api/v1/pipelines"
	tasksPath           = "/api/v1/system/tasks"
	knowledgeBasesPath  = "/api/v1/knowledge/bases"
	documentUploadPath  = "/api/v1/files/documents"
	pluginsPath         = "/api/v1/plugins"
	skillsPath          = "/api/v1/skills"
	mcpServersPath      = "/api/v1/mcp/servers"
	providersPath       = "/api/v1/provider/providers"
	llmModelsPath       = "/api/v1/provider/models/llm"
	embeddingModelsPath = "/api/v1/provider/models/embedding"
	rerankModelsPath    = "/api/v1/provider/models/rerank"
	defaultTimeout      = 30 * time.Second
	maxResponseBytes    = 1 << 20
	maxRequestBytes     = 1 << 20
	maxUploadBytes      = 10 << 20
	maxServerCodeSize   = 128
	maxRequestIDSize    = 128
)

// Target 是一次请求使用的不可变连接快照。
type Target struct {
	Endpoint string        `json:"endpoint" yaml:"endpoint"`
	APIKey   string        `json:"-" yaml:"-"`
	Timeout  time.Duration `json:"timeout" yaml:"timeout"`
}

// Client 只封装 HTTP 传输策略，不保存 context 或凭据状态。
type Client struct {
	Transport http.RoundTripper
}

// Info 是 /system/info 中 CLI 当前需要的稳定字段。
type Info struct {
	Version string `json:"version" yaml:"version"`
	Edition string `json:"edition" yaml:"edition"`
}

// Context 是 API Key 对应的可信服务端身份与权限。
type Context struct {
	InstanceUUID  string   `json:"instance_uuid" yaml:"instance_uuid"`
	WorkspaceUUID string   `json:"workspace_uuid" yaml:"workspace_uuid"`
	APIKeyID      string   `json:"api_key_id" yaml:"api_key_id"`
	Permissions   []string `json:"permissions" yaml:"permissions"`
}

// Capabilities 是服务端声明的 HTTP 操作能力。Operations 只包含响应中已知的操作。
type Capabilities struct {
	SchemaVersion int
	Operations    map[string]bool
}

// WriteResult 只保留写操作完成后可安全用于回读的资源 ID。
type WriteResult struct {
	UUID string
}

// TaskError 是异步任务的稳定错误表示。
type TaskError struct {
	Type    string `json:"type" yaml:"type"`
	Message string `json:"message" yaml:"message"`
}

// Task 是 API Key 可见的稳定异步任务表示，Result 保持服务端 JSON 不透明传递。
type Task struct {
	ID        json.Number `json:"id" yaml:"id"`
	TaskType  string      `json:"task_type" yaml:"task_type"`
	Kind      string      `json:"kind" yaml:"kind"`
	Status    string      `json:"status" yaml:"status"`
	Error     *TaskError  `json:"error" yaml:"error"`
	Result    any         `json:"result" yaml:"result"`
	CreatedAt json.Number `json:"created_at" yaml:"created_at"`
}

// TaskListFilters 限制任务列表的服务端筛选条件。
type TaskListFilters struct {
	Type string
	Kind string
}

var capabilityOperationIDs = []string{
	"knowledge_engine.list",
	"knowledge_engine.creation_schema",
	"knowledge_engine.retrieval_schema",
	"knowledge_parser.list",

	"bot.list",
	"bot.get",
	"bot.create",
	"bot.update",
	"bot.delete",
	"pipeline.list",
	"pipeline.get",
	"pipeline.create",
	"pipeline.update",
	"pipeline.delete",
	"pipeline.copy",
	"task.list",
	"task.get",
	"knowledge_base.list",
	"knowledge_base.get",
	"knowledge_base.create",
	"knowledge_base.update",
	"knowledge_base.delete",
	"knowledge_base.file.list",
	"knowledge_base.file.store",
	"knowledge_base.file.delete",
	"knowledge_base.retrieve",
	"file.document.upload",
	"plugin.install.github",
	"plugin.install.marketplace",
	"plugin.install.local",
	"plugin.upgrade",
	"plugin.get",
	"plugin.list",
	"plugin.config.get",
	"plugin.config.update",
	"plugin.logs",
	"plugin.delete",
	"provider.list",
	"provider.get",
	"provider.create",
	"provider.update",
	"provider.delete",
	"provider.scan_models",
	"model.llm.list",
	"model.llm.get",
	"model.llm.create",
	"model.llm.update",
	"model.llm.delete",
	"model.llm.test",
	"model.embedding.list",
	"model.embedding.get",
	"model.embedding.create",
	"model.embedding.update",
	"model.embedding.delete",
	"model.embedding.test",
	"model.rerank.list",
	"model.rerank.get",
	"model.rerank.create",
	"model.rerank.update",
	"model.rerank.delete",
	"model.rerank.test",
	"skill.list",
	"skill.get",
	"skill.create",
	"skill.update",
	"skill.delete",
	"skill.files.list",
	"skill.files.read",
	"skill.files.write",
	"skill.preview",
	"skill.install.github",
	"skill.install.upload",
	"mcp_server.list",
	"mcp_server.get",
	"mcp_server.create",
	"mcp_server.update",
	"mcp_server.delete",
	"mcp_server.resources",
	"mcp_server.resource_templates",
	"mcp_server.resource_read",
	"mcp_server.logs",
	"mcp_server.test",
}

// CapabilityOperationIDs 返回 CLI 支持展示的稳定操作顺序。
func CapabilityOperationIDs() []string {
	return append([]string(nil), capabilityOperationIDs...)
}

func ValidateResourceID(identifier string) error {
	_, err := resourcePath("", identifier)
	return err
}

// ValidateMCPServerName 校验 MCP Server 名称能否安全放入服务端路径参数。
func ValidateMCPServerName(name string) error {
	_, err := mcpServerPath(name)
	return err
}

// ValidateSkillFilePath 校验 Skill 包内的相对路径。
func ValidateSkillFilePath(path string, allowRoot bool) error {
	parts, err := safeFilePath(path)
	if err != nil {
		return err
	}
	if !allowRoot && len(parts) == 1 && parts[0] == "." {
		return result.New("input", "文件路径不是安全的相对路径")
	}
	return nil
}

type envelope struct {
	Code      json.RawMessage `json:"code"`
	Msg       string          `json:"msg"`
	Data      json.RawMessage `json:"data"`
	RequestID string          `json:"request_id"`
}

type response struct {
	StatusCode int
	Header     http.Header
	Body       envelope
	Data       map[string]any
}

// Info 请求已确认的公开诊断接口。该接口返回成功不代表 API Key 有效。
func (c Client) Info(ctx context.Context, target Target) (Info, error) {
	resp, err := c.fetch(ctx, target, http.MethodGet, infoPath)
	if err != nil {
		return Info{}, err
	}
	data := projectInfo(resp.Data, target.APIKey)
	if safeString(data["version"]) == "" {
		return Info{}, protocolError("服务返回的诊断数据缺少有效版本", resp.StatusCode, responseRequestID(resp.Header, resp.Body, target.APIKey))
	}

	return Info{
		Version: safeString(data["version"]),
		Edition: safeString(data["edition"]),
	}, nil
}

// Context 请求需要 API Key 鉴权的服务端身份接口。
func (c Client) Context(ctx context.Context, target Target) (Context, error) {
	resp, err := c.fetch(ctx, target, http.MethodGet, contextPath)
	if err != nil {
		return Context{}, err
	}
	identity, ok := parseContextData(resp.Data)
	if !ok {
		return Context{}, protocolError("服务返回的身份数据格式无效", resp.StatusCode, responseRequestID(resp.Header, resp.Body, target.APIKey))
	}
	return identity, nil
}

// Capabilities 请求服务端声明的 HTTP 操作能力。
func (c Client) Capabilities(ctx context.Context, target Target) (Capabilities, error) {
	resp, err := c.fetch(ctx, target, http.MethodGet, capabilitiesPath)
	if err != nil {
		return Capabilities{}, err
	}
	schemaVersion, ok := capabilitySchemaVersion(resp.Data["schema_version"])
	if !ok || schemaVersion != 1 {
		return Capabilities{}, protocolError("服务返回的能力协议版本不受支持", resp.StatusCode, responseRequestID(resp.Header, resp.Body, target.APIKey))
	}
	operations, ok := resp.Data["operations"].(map[string]any)
	if !ok {
		return Capabilities{}, protocolError("服务返回的能力数据格式无效", resp.StatusCode, responseRequestID(resp.Header, resp.Body, target.APIKey))
	}
	known := make(map[string]struct{}, len(capabilityOperationIDs))
	for _, operationID := range capabilityOperationIDs {
		known[operationID] = struct{}{}
	}
	parsed := make(map[string]bool, len(operations))
	for operationID, value := range operations {
		if _, isKnown := known[operationID]; !isKnown {
			continue
		}
		entry, ok := value.(map[string]any)
		if !ok {
			return Capabilities{}, protocolError("服务返回的能力项格式无效", resp.StatusCode, responseRequestID(resp.Header, resp.Body, target.APIKey))
		}
		supported, ok := entry["supported"].(bool)
		if !ok {
			return Capabilities{}, protocolError("服务返回的能力项缺少有效 supported 字段", resp.StatusCode, responseRequestID(resp.Header, resp.Body, target.APIKey))
		}
		parsed[operationID] = supported
	}
	return Capabilities{SchemaVersion: schemaVersion, Operations: parsed}, nil
}

// Task 返回一个异步任务的公共状态，不暴露服务端内部 runtime 字段。
func (c Client) Task(ctx context.Context, target Target, identifier string) (Task, error) {
	path, err := taskPath(identifier)
	if err != nil {
		return Task{}, err
	}
	resp, err := c.fetch(ctx, target, http.MethodGet, path)
	if err != nil {
		return Task{}, err
	}
	task, ok := parseTaskData(resp.Data, identifier)
	if !ok {
		return Task{}, protocolError("服务返回的任务数据格式无效", resp.StatusCode, responseRequestID(resp.Header, resp.Body, target.APIKey))
	}
	return task, nil
}

// Tasks 返回 API Key 可见的异步任务列表。
func (c Client) Tasks(ctx context.Context, target Target, filters TaskListFilters) ([]Task, error) {
	query := url.Values{}
	if strings.TrimSpace(filters.Type) != "" {
		query.Set("type", filters.Type)
	}
	if strings.TrimSpace(filters.Kind) != "" {
		query.Set("kind", filters.Kind)
	}
	path := tasksPath
	if encoded := query.Encode(); encoded != "" {
		path += "?" + encoded
	}
	resp, err := c.fetch(ctx, target, http.MethodGet, path)
	if err != nil {
		return nil, err
	}
	raw, ok := resp.Data["tasks"].([]any)
	if !ok {
		return nil, protocolError("服务返回的任务列表格式无效", resp.StatusCode, responseRequestID(resp.Header, resp.Body, target.APIKey))
	}
	tasks := make([]Task, 0, len(raw))
	for _, value := range raw {
		item, ok := value.(map[string]any)
		if !ok {
			return nil, protocolError("服务返回的任务格式无效", resp.StatusCode, responseRequestID(resp.Header, resp.Body, target.APIKey))
		}
		id, ok := stringValue(item["id"])
		if !ok || strings.TrimSpace(id) == "" || containsSecret(id, target.APIKey) {
			return nil, protocolError("服务返回的任务缺少有效 ID", resp.StatusCode, responseRequestID(resp.Header, resp.Body, target.APIKey))
		}
		task, ok := parseTaskData(item, id)
		if !ok {
			return nil, protocolError("服务返回的任务数据格式无效", resp.StatusCode, responseRequestID(resp.Header, resp.Body, target.APIKey))
		}
		tasks = append(tasks, task)
	}
	return tasks, nil
}

// KnowledgeBase 返回指定知识库的安全字段。
func (c Client) KnowledgeBase(ctx context.Context, target Target, identifier string) (map[string]any, error) {
	path, err := resourcePath(knowledgeBasesPath, identifier)
	if err != nil {
		return nil, err
	}
	resp, err := c.fetch(ctx, target, http.MethodGet, path)
	if err != nil {
		return nil, err
	}
	base, err := parseResourceObject(resp, "base", projectKnowledgeBase, target.APIKey)
	if err != nil {
		return nil, err
	}
	if base["uuid"] != identifier {
		return nil, protocolError("服务返回的知识库 UUID 与请求不一致", resp.StatusCode, responseRequestID(resp.Header, resp.Body, target.APIKey))
	}
	return base, nil
}

// KnowledgeBases 返回知识库的安全字段投影。
func (c Client) KnowledgeBases(ctx context.Context, target Target) ([]map[string]any, error) {
	resp, err := c.fetch(ctx, target, http.MethodGet, knowledgeBasesPath)
	if err != nil {
		return nil, err
	}
	return parseResourceList(resp, "bases", projectKnowledgeBase, target.APIKey)
}

func (c Client) KnowledgeBaseCreate(ctx context.Context, target Target, body map[string]any) (WriteResult, error) {
	return c.createResource(ctx, target, http.MethodPost, knowledgeBasesPath, body)
}

func (c Client) KnowledgeBaseUpdate(ctx context.Context, target Target, identifier string, body map[string]any) (WriteResult, error) {
	return c.updateResource(ctx, target, http.MethodPut, knowledgeBasesPath, identifier, body)
}

func (c Client) KnowledgeBaseDelete(ctx context.Context, target Target, identifier string) (WriteResult, error) {
	return c.updateResource(ctx, target, http.MethodDelete, knowledgeBasesPath, identifier, nil)
}

// KnowledgeBaseFiles 返回知识库文件列表。文件字段由服务端定义，连接凭据只做值替换。
func (c Client) KnowledgeBaseFiles(ctx context.Context, target Target, identifier string) ([]any, error) {
	path, err := resourcePath(knowledgeBasesPath, identifier)
	if err != nil {
		return nil, err
	}
	resp, err := c.fetch(ctx, target, http.MethodGet, path+"/files")
	if err != nil {
		return nil, err
	}
	files, ok := resp.Data["files"].([]any)
	if !ok {
		return nil, protocolError("服务返回的知识库文件列表格式无效", resp.StatusCode, responseRequestID(resp.Header, resp.Body, target.APIKey))
	}
	return redactConnectionSecret(files, target.APIKey).([]any), nil
}

func (c Client) KnowledgeBaseFileDelete(ctx context.Context, target Target, identifier, fileID string) error {
	base, err := resourcePath(knowledgeBasesPath, identifier)
	if err != nil {
		return err
	}
	path, err := resourcePath(base+"/files", fileID)
	if err != nil {
		return err
	}
	_, err = c.write(ctx, target, http.MethodDelete, path, nil)
	return err
}

func (c Client) KnowledgeBaseRetrieve(ctx context.Context, target Target, identifier string, body map[string]any) (any, error) {
	path, err := resourcePath(knowledgeBasesPath, identifier)
	if err != nil {
		return nil, err
	}
	resp, err := c.readPost(ctx, target, path+"/retrieve", body)
	if err != nil {
		return nil, err
	}
	return redactConnectionSecret(resp.Data, target.APIKey), nil
}

// UploadDocument 上传文件并返回服务端文件 ID。
func (c Client) UploadDocument(ctx context.Context, target Target, filename string, file io.Reader) (string, error) {
	if strings.TrimSpace(filename) == "" || file == nil {
		return "", result.New("input", "上传文件不能为空")
	}
	content, err := io.ReadAll(io.LimitReader(file, maxUploadBytes+1))
	if err != nil {
		return "", result.New("input", "读取上传文件失败")
	}
	if len(content) > maxUploadBytes {
		return "", result.New("input", "上传文件超过 10MB 限制")
	}
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", filename)
	if err != nil {
		return "", result.New("input", "无法构造上传请求")
	}
	if _, err = part.Write(content); err != nil {
		return "", result.New("input", "无法构造上传请求")
	}
	if err = writer.Close(); err != nil {
		return "", result.New("input", "无法构造上传请求")
	}
	if body.Len() > maxUploadBytes {
		return "", result.New("input", "上传文件连同请求元数据超过 10MB 限制")
	}
	resp, err := c.writeMultipart(ctx, target, documentUploadPath, body.Bytes(), writer.FormDataContentType())
	if err != nil {
		return "", err
	}
	fileID, ok := stringValue(resp.Data["file_id"])
	if !ok || strings.TrimSpace(fileID) == "" || containsSecret(fileID, target.APIKey) {
		return "", writeUnknown(&result.Error{HTTPStatus: resp.StatusCode, RequestID: responseRequestID(resp.Header, resp.Body, target.APIKey)})
	}
	return fileID, nil
}

// KnowledgeBaseStoreFile 提交已上传文件到知识库，返回异步任务 ID。
func (c Client) KnowledgeBaseStoreFile(ctx context.Context, target Target, identifier, fileID, parserPluginID string) (string, error) {
	path, err := resourcePath(knowledgeBasesPath, identifier)
	if err != nil {
		return "", err
	}
	body := map[string]any{"file_id": fileID}
	if parserPluginID != "" {
		body["parser_plugin_id"] = parserPluginID
	}
	resp, err := c.write(ctx, target, http.MethodPost, path+"/files", body)
	if err != nil {
		return "", err
	}
	taskID, ok := stringValue(resp.Data["task_id"])
	if !ok || strings.TrimSpace(taskID) == "" || containsSecret(taskID, target.APIKey) {
		return "", writeUnknown(&result.Error{HTTPStatus: resp.StatusCode, RequestID: responseRequestID(resp.Header, resp.Body, target.APIKey)})
	}
	return taskID, nil
}

// PluginInstallGitHub 提交 GitHub 插件安装任务。
func (c Client) PluginInstallGitHub(ctx context.Context, target Target, body map[string]any) (string, error) {
	return c.taskWrite(ctx, target, http.MethodPost, pluginsPath+"/install/github", body)
}

// PluginInstallMarketplace 提交 Marketplace 插件安装任务。
func (c Client) PluginInstallMarketplace(ctx context.Context, target Target, body map[string]any) (string, error) {
	return c.taskWrite(ctx, target, http.MethodPost, pluginsPath+"/install/marketplace", body)
}

// PluginInstallLocal 提交本地插件包安装任务。
func (c Client) PluginInstallLocal(ctx context.Context, target Target, filename string, file io.Reader) (string, error) {
	body, contentType, err := multipartBody(filename, file, nil, maxUploadBytes)
	if err != nil {
		return "", err
	}
	resp, err := c.writeMultipart(ctx, target, pluginsPath+"/install/local", body, contentType)
	if err != nil {
		return "", err
	}
	return taskID(resp)
}

// PluginUpgrade 提交已安装插件升级任务。
func (c Client) PluginUpgrade(ctx context.Context, target Target, author, name string) (string, error) {
	path, err := resourcePath(pluginsPath, author)
	if err != nil {
		return "", err
	}
	path, err = resourcePath(path, name)
	if err != nil {
		return "", err
	}
	return c.taskWrite(ctx, target, http.MethodPost, path+"/upgrade", nil)
}

// PluginGet 返回插件的安全存在性信息。
func (c Client) PluginGet(ctx context.Context, target Target, author, name string) (map[string]any, error) {
	path, err := resourcePath(pluginsPath, author)
	if err != nil {
		return nil, err
	}
	path, err = resourcePath(path, name)
	if err != nil {
		return nil, err
	}
	resp, err := c.fetch(ctx, target, http.MethodGet, path)
	if err != nil {
		return nil, err
	}
	plugin, ok := resp.Data["plugin"].(map[string]any)
	if !ok {
		return nil, protocolError("服务返回的资源格式无效", resp.StatusCode, responseRequestID(resp.Header, resp.Body, target.APIKey))
	}
	projected := projectPlugin(plugin, author, name, target.APIKey)
	if projected["author"] != author || projected["name"] != name {
		return nil, protocolError("服务返回的 Plugin 身份与请求不一致", resp.StatusCode, responseRequestID(resp.Header, resp.Body, target.APIKey))
	}
	return projected, nil
}

func (c Client) Plugins(ctx context.Context, target Target) ([]map[string]any, error) {
	resp, err := c.fetch(ctx, target, http.MethodGet, pluginsPath)
	if err != nil {
		return nil, err
	}
	raw, ok := resp.Data["plugins"].([]any)
	if !ok {
		return nil, protocolError("服务返回的插件列表格式无效", resp.StatusCode, responseRequestID(resp.Header, resp.Body, target.APIKey))
	}
	items := make([]map[string]any, 0, len(raw))
	for _, value := range raw {
		plugin, ok := value.(map[string]any)
		if !ok {
			return nil, protocolError("服务返回的插件格式无效", resp.StatusCode, responseRequestID(resp.Header, resp.Body, target.APIKey))
		}
		projected := projectPlugin(plugin, "", "", target.APIKey)
		author, authorOK := projected["author"].(string)
		name, nameOK := projected["name"].(string)
		if !authorOK || strings.TrimSpace(author) == "" || !nameOK || strings.TrimSpace(name) == "" {
			return nil, protocolError("服务返回的 Plugin 缺少有效身份", resp.StatusCode, responseRequestID(resp.Header, resp.Body, target.APIKey))
		}
		items = append(items, projected)
	}
	return items, nil
}

func (c Client) PluginConfig(ctx context.Context, target Target, author, name string) (any, error) {
	path, err := pluginResourcePath(author, name, "/config")
	if err != nil {
		return nil, err
	}
	resp, err := c.fetch(ctx, target, http.MethodGet, path)
	if err != nil {
		return nil, err
	}
	return redactConnectionSecret(resp.Data, target.APIKey), nil
}

func (c Client) PluginConfigUpdate(ctx context.Context, target Target, author, name string, body map[string]any) error {
	if body == nil {
		return result.New("input", "Plugin 配置必须是 JSON object")
	}
	path, err := pluginResourcePath(author, name, "/config")
	if err != nil {
		return err
	}
	_, err = c.write(ctx, target, http.MethodPut, path, body)
	return err
}

func (c Client) PluginDelete(ctx context.Context, target Target, author, name string, deleteData bool) (string, error) {
	path, err := pluginResourcePath(author, name, "")
	if err != nil {
		return "", err
	}
	if deleteData {
		path += "?delete_data=true"
	}
	return c.taskWrite(ctx, target, http.MethodDelete, path, nil)
}

func (c Client) PluginLogs(ctx context.Context, target Target, author, name, level string, limit int) (any, error) {
	path, err := pluginResourcePath(author, name, "/logs")
	if err != nil {
		return nil, err
	}
	query := url.Values{}
	if limit > 0 {
		query.Set("limit", strconv.Itoa(limit))
	}
	if strings.TrimSpace(level) != "" {
		query.Set("level", level)
	}
	if encoded := query.Encode(); encoded != "" {
		path += "?" + encoded
	}
	resp, err := c.fetch(ctx, target, http.MethodGet, path)
	if err != nil {
		return nil, err
	}
	return redactConnectionSecret(resp.Data, target.APIKey), nil
}

func (c Client) Skills(ctx context.Context, target Target) ([]map[string]any, error) {
	resp, err := c.fetch(ctx, target, http.MethodGet, skillsPath)
	if err != nil {
		return nil, err
	}
	raw, ok := resp.Data["skills"].([]any)
	if !ok {
		return nil, protocolError("服务返回的 Skill 列表格式无效", resp.StatusCode, responseRequestID(resp.Header, resp.Body, target.APIKey))
	}
	items := make([]map[string]any, 0, len(raw))
	for _, value := range raw {
		item, ok := value.(map[string]any)
		if !ok {
			return nil, protocolError("服务返回的 Skill 格式无效", resp.StatusCode, responseRequestID(resp.Header, resp.Body, target.APIKey))
		}
		projected := projectSkillSummary(item, target.APIKey)
		name, ok := projected["name"].(string)
		if !ok || strings.TrimSpace(name) == "" {
			return nil, protocolError("服务返回的 Skill 缺少有效名称", resp.StatusCode, responseRequestID(resp.Header, resp.Body, target.APIKey))
		}
		items = append(items, projected)
	}
	return items, nil
}

func (c Client) SkillGet(ctx context.Context, target Target, name string) (map[string]any, error) {
	path, err := skillResourcePath(name, "")
	if err != nil {
		return nil, err
	}
	resp, err := c.fetch(ctx, target, http.MethodGet, path)
	if err != nil {
		return nil, err
	}
	item, ok := resp.Data["skill"].(map[string]any)
	if !ok {
		return nil, protocolError("服务返回的 Skill 格式无效", resp.StatusCode, responseRequestID(resp.Header, resp.Body, target.APIKey))
	}
	projected := projectSkillSummary(item, target.APIKey)
	if skillName, ok := projected["name"].(string); !ok || skillName != name {
		return nil, protocolError("服务返回的 Skill 身份与请求不一致", resp.StatusCode, responseRequestID(resp.Header, resp.Body, target.APIKey))
	}
	if instructions, exists := item["instructions"]; exists {
		projected["instructions"] = redactConnectionSecret(instructions, target.APIKey)
	}
	return projected, nil
}

func (c Client) SkillFiles(ctx context.Context, target Target, name, pathValue string, includeHidden bool) (any, error) {
	path, err := skillResourcePath(name, "/files")
	if err != nil {
		return nil, err
	}
	if err := ValidateSkillFilePath(pathValue, true); err != nil {
		return nil, err
	}
	query := url.Values{}
	if strings.TrimSpace(pathValue) != "" {
		query.Set("path", pathValue)
	}
	if includeHidden {
		query.Set("include_hidden", "true")
	}
	if encoded := query.Encode(); encoded != "" {
		path += "?" + encoded
	}
	resp, err := c.fetch(ctx, target, http.MethodGet, path)
	if err != nil {
		return nil, err
	}
	return redactConnectionSecret(resp.Data, target.APIKey), nil
}

func (c Client) SkillFileRead(ctx context.Context, target Target, name, filePath string) (any, error) {
	if err := ValidateSkillFilePath(filePath, false); err != nil {
		return nil, err
	}
	data, _, err := c.skillFileRead(ctx, target, name, filePath, "")
	return data, err
}

// SkillFileMatches 在不暴露原始内容的前提下校验写后文件内容。
func (c Client) SkillFileMatches(ctx context.Context, target Target, name, filePath, expected string) (any, bool, error) {
	if err := ValidateSkillFilePath(filePath, false); err != nil {
		return nil, false, err
	}
	return c.skillFileRead(ctx, target, name, filePath, expected)
}

func (c Client) skillFileRead(ctx context.Context, target Target, name, filePath, expected string) (any, bool, error) {
	path, err := skillFileResourcePath(name, filePath)
	if err != nil {
		return nil, false, err
	}
	resp, err := c.fetch(ctx, target, http.MethodGet, path)
	if err != nil {
		return nil, false, err
	}
	content, ok := resp.Data["content"].(string)
	if !ok {
		return nil, false, protocolError("服务返回的 Skill 文件格式无效", resp.StatusCode, responseRequestID(resp.Header, resp.Body, target.APIKey))
	}
	return redactConnectionSecret(resp.Data, target.APIKey), content == expected, nil
}

func (c Client) SkillPreview(ctx context.Context, target Target, name string) (any, error) {
	path, err := skillResourcePath(name, "/preview")
	if err != nil {
		return nil, err
	}
	resp, err := c.fetch(ctx, target, http.MethodGet, path)
	if err != nil {
		return nil, err
	}
	return redactConnectionSecret(resp.Data, target.APIKey), nil
}

func (c Client) SkillCreate(ctx context.Context, target Target, body map[string]any) (map[string]any, error) {
	if body == nil {
		return nil, result.New("input", "Skill 请求体必须是 JSON object")
	}
	resp, err := c.write(ctx, target, http.MethodPost, skillsPath, body)
	if err != nil {
		return nil, err
	}
	expectedName, _ := body["name"].(string)
	skill, err := parseSkillResponse(resp, expectedName, target.APIKey)
	if err != nil {
		return nil, writeUnknown(result.AsError(err))
	}
	return skill, nil
}

func (c Client) SkillUpdate(ctx context.Context, target Target, name string, body map[string]any) (map[string]any, error) {
	if body == nil {
		return nil, result.New("input", "Skill 请求体必须是 JSON object")
	}
	path, err := skillResourcePath(name, "")
	if err != nil {
		return nil, err
	}
	resp, err := c.write(ctx, target, http.MethodPut, path, body)
	if err != nil {
		return nil, err
	}
	skill, err := parseSkillResponse(resp, name, target.APIKey)
	if err != nil {
		return nil, writeUnknown(result.AsError(err))
	}
	return skill, nil
}

func (c Client) SkillDelete(ctx context.Context, target Target, name string) error {
	path, err := skillResourcePath(name, "")
	if err != nil {
		return err
	}
	_, err = c.write(ctx, target, http.MethodDelete, path, nil)
	return err
}

func (c Client) SkillFileWrite(ctx context.Context, target Target, name, filePath, content string) (any, error) {
	if err := ValidateSkillFilePath(filePath, false); err != nil {
		return nil, err
	}
	path, err := skillFileResourcePath(name, filePath)
	if err != nil {
		return nil, err
	}
	resp, err := c.write(ctx, target, http.MethodPut, path, map[string]any{"content": content})
	if err != nil {
		return nil, err
	}
	return redactConnectionSecret(resp.Data, target.APIKey), nil
}

// SkillInstallGitHub 安装 GitHub Skill，服务端同步返回安装结果。
func (c Client) SkillInstallGitHub(ctx context.Context, target Target, body map[string]any) (map[string]any, error) {
	return c.skillWrite(ctx, target, "/install/github", body)
}

// SkillPreviewGitHub 请求服务端预览 GitHub Skill，不产生持久安装。
func (c Client) SkillPreviewGitHub(ctx context.Context, target Target, body map[string]any) (map[string]any, error) {
	resp, err := c.previewJSON(ctx, target, skillsPath+"/install/github/preview", body)
	if err != nil {
		return nil, err
	}
	return resp.Data, nil
}

// SkillInstallUpload 安装 ZIP Skill，保留 source_paths 的多值表单语义。
func (c Client) SkillInstallUpload(ctx context.Context, target Target, filename string, file io.Reader, sourcePaths []string) (map[string]any, error) {
	return c.skillMultipart(ctx, target, "/install/upload", filename, file, sourcePaths)
}

// SkillPreviewUpload 请求服务端预览 ZIP Skill，不产生持久安装。
func (c Client) SkillPreviewUpload(ctx context.Context, target Target, filename string, file io.Reader, sourcePaths []string) (map[string]any, error) {
	body, contentType, err := multipartBody(filename, file, sourcePaths, maxUploadBytes)
	if err != nil {
		return nil, err
	}
	resp, err := c.previewMultipart(ctx, target, skillsPath+"/install/upload/preview", body, contentType)
	if err != nil {
		return nil, err
	}
	return resp.Data, nil
}

// MCPServerGet 返回 MCP Server 的安全存在性信息。
func (c Client) MCPServerGet(ctx context.Context, target Target, name string) (map[string]any, error) {
	path, err := mcpServerPath(name)
	if err != nil {
		return nil, err
	}
	resp, err := c.fetch(ctx, target, http.MethodGet, path)
	if err != nil {
		return nil, err
	}
	item, ok := resp.Data["server"].(map[string]any)
	if !ok {
		return nil, protocolError("服务返回的 MCP Server 格式无效", resp.StatusCode, responseRequestID(resp.Header, resp.Body, target.APIKey))
	}
	server := projectMCPServer(item, target.APIKey)
	for _, field := range []string{"extra_args", "readme", "runtime_info"} {
		if value, exists := item[field]; exists {
			server[field] = redactConnectionSecret(value, target.APIKey)
		}
	}
	uuid, uuidOK := server["uuid"].(string)
	serverName, nameOK := server["name"].(string)
	if !uuidOK || strings.TrimSpace(uuid) == "" || containsSecret(uuid, target.APIKey) ||
		!nameOK || serverName != strings.TrimSpace(name) {
		return nil, protocolError("服务返回的 MCP Server 身份与请求不一致", resp.StatusCode, responseRequestID(resp.Header, resp.Body, target.APIKey))
	}
	return server, nil
}

// MCPServers 返回 MCP Server 的安全字段投影。
func (c Client) MCPServers(ctx context.Context, target Target) ([]map[string]any, error) {
	resp, err := c.fetch(ctx, target, http.MethodGet, mcpServersPath)
	if err != nil {
		return nil, err
	}
	return parseResourceList(resp, "servers", projectMCPServer, target.APIKey)
}

func (c Client) MCPServerCreate(ctx context.Context, target Target, body map[string]any) (WriteResult, error) {
	return c.createResource(ctx, target, http.MethodPost, mcpServersPath, body)
}

func (c Client) MCPServerUpdate(ctx context.Context, target Target, name string, body map[string]any) error {
	path, err := mcpServerPath(name)
	if err != nil {
		return err
	}
	_, err = c.write(ctx, target, http.MethodPut, path, body)
	return err
}

func (c Client) MCPServerDelete(ctx context.Context, target Target, name string) error {
	path, err := mcpServerPath(name)
	if err != nil {
		return err
	}
	_, err = c.write(ctx, target, http.MethodDelete, path, nil)
	return err
}

func (c Client) MCPServerResources(ctx context.Context, target Target, name string) (any, error) {
	return c.mcpServerRead(ctx, target, name, "/resources")
}

func (c Client) MCPServerResourceTemplates(ctx context.Context, target Target, name string) (any, error) {
	return c.mcpServerRead(ctx, target, name, "/resource-templates")
}

func (c Client) MCPServerLogs(ctx context.Context, target Target, name, level string, limit int) (any, error) {
	path, err := mcpServerPath(name)
	if err != nil {
		return nil, err
	}
	path += "/logs"
	query := url.Values{}
	if limit > 0 {
		query.Set("limit", strconv.Itoa(limit))
	}
	if strings.TrimSpace(level) != "" {
		query.Set("level", level)
	}
	if encoded := query.Encode(); encoded != "" {
		path += "?" + encoded
	}
	resp, err := c.fetch(ctx, target, http.MethodGet, path)
	if err != nil {
		return nil, err
	}
	return redactConnectionSecret(resp.Data, target.APIKey), nil
}

func (c Client) MCPServerResourceRead(ctx context.Context, target Target, name string, body map[string]any) (any, error) {
	path, err := mcpServerPath(name)
	if err != nil {
		return nil, err
	}
	resp, err := c.readPost(ctx, target, path+"/resources/read", body)
	if err != nil {
		return nil, err
	}
	return redactConnectionSecret(resp.Data, target.APIKey), nil
}

func (c Client) mcpServerRead(ctx context.Context, target Target, name, suffix string) (any, error) {
	path, err := mcpServerPath(name)
	if err != nil {
		return nil, err
	}
	resp, err := c.fetch(ctx, target, http.MethodGet, path+suffix)
	if err != nil {
		return nil, err
	}
	return redactConnectionSecret(resp.Data, target.APIKey), nil
}

// MCPServerTest 提交 MCP Server 测试任务。
func (c Client) MCPServerTest(ctx context.Context, target Target, name string, body map[string]any) (string, error) {
	path, err := mcpServerPath(name)
	if err != nil {
		return "", err
	}
	return c.taskWrite(ctx, target, http.MethodPost, path+"/test", body)
}

// Bots 返回 Bot 的安全字段投影，不输出 adapter_config 等配置内容。
func (c Client) Bots(ctx context.Context, target Target) ([]map[string]any, error) {
	resp, err := c.fetch(ctx, target, http.MethodGet, botsPath)
	if err != nil {
		return nil, err
	}
	return parseResourceList(resp, "bots", projectBot, target.APIKey)
}

// Bot 返回指定 Bot 的安全字段投影。
func (c Client) Bot(ctx context.Context, target Target, uuid string) (map[string]any, error) {
	path, err := resourcePath(botsPath, uuid)
	if err != nil {
		return nil, err
	}
	resp, err := c.fetch(ctx, target, http.MethodGet, path)
	if err != nil {
		return nil, err
	}
	bot, err := parseResourceObject(resp, "bot", projectBot, target.APIKey)
	if err != nil {
		return nil, err
	}
	if bot["uuid"] != uuid {
		return nil, protocolError("服务返回的 Bot UUID 与请求不一致", resp.StatusCode, responseRequestID(resp.Header, resp.Body, target.APIKey))
	}
	return bot, nil
}

// Pipelines 返回 Pipeline 的安全字段投影，不输出 config 等配置内容。
func (c Client) Pipelines(ctx context.Context, target Target) ([]map[string]any, error) {
	resp, err := c.fetch(ctx, target, http.MethodGet, pipelinesPath)
	if err != nil {
		return nil, err
	}
	return parseResourceList(resp, "pipelines", projectPipeline, target.APIKey)
}

// Pipeline 返回指定 Pipeline 的安全字段投影。
func (c Client) Pipeline(ctx context.Context, target Target, uuid string) (map[string]any, error) {
	path, err := resourcePath(pipelinesPath, uuid)
	if err != nil {
		return nil, err
	}
	resp, err := c.fetch(ctx, target, http.MethodGet, path)
	if err != nil {
		return nil, err
	}
	pipeline, err := parseResourceObject(resp, "pipeline", projectPipeline, target.APIKey)
	if err != nil {
		return nil, err
	}
	if pipeline["uuid"] != uuid {
		return nil, protocolError("服务返回的 Pipeline UUID 与请求不一致", resp.StatusCode, responseRequestID(resp.Header, resp.Body, target.APIKey))
	}
	return pipeline, nil
}

// BotCreate 创建 Bot，并返回服务端分配的 UUID。
func (c Client) BotCreate(ctx context.Context, target Target, body map[string]any) (WriteResult, error) {
	return c.createResource(ctx, target, http.MethodPost, botsPath, body)
}

// BotUpdate 更新 Bot。调用者可用 uuid 回读确认最终状态。
func (c Client) BotUpdate(ctx context.Context, target Target, uuid string, body map[string]any) (WriteResult, error) {
	return c.updateResource(ctx, target, http.MethodPut, botsPath, uuid, body)
}

// BotDelete 删除 Bot。调用者可用 uuid 回读确认最终状态。
func (c Client) BotDelete(ctx context.Context, target Target, uuid string) (WriteResult, error) {
	return c.updateResource(ctx, target, http.MethodDelete, botsPath, uuid, nil)
}

// PipelineCreate 创建 Pipeline，并返回服务端分配的 UUID。
func (c Client) PipelineCreate(ctx context.Context, target Target, body map[string]any) (WriteResult, error) {
	return c.createResource(ctx, target, http.MethodPost, pipelinesPath, body)
}

// PipelineUpdate 更新 Pipeline。调用者可用 uuid 回读确认最终状态。
func (c Client) PipelineUpdate(ctx context.Context, target Target, uuid string, body map[string]any) (WriteResult, error) {
	return c.updateResource(ctx, target, http.MethodPut, pipelinesPath, uuid, body)
}

// PipelineDelete 删除 Pipeline。调用者可用 uuid 回读确认最终状态。
func (c Client) PipelineDelete(ctx context.Context, target Target, uuid string) (WriteResult, error) {
	return c.updateResource(ctx, target, http.MethodDelete, pipelinesPath, uuid, nil)
}

// PipelineCopy 复制 Pipeline，并返回新资源 UUID。
func (c Client) PipelineCopy(ctx context.Context, target Target, uuid string) (WriteResult, error) {
	path, err := resourcePath(pipelinesPath, uuid)
	if err != nil {
		return WriteResult{}, err
	}
	return c.writeUUID(ctx, target, http.MethodPost, path+"/copy", nil)
}

func (c Client) createResource(ctx context.Context, target Target, method, base string, body map[string]any) (WriteResult, error) {
	if body == nil {
		return WriteResult{}, result.New("input", "请求体必须是 JSON object")
	}
	return c.writeUUID(ctx, target, method, base, body)
}

func (c Client) updateResource(ctx context.Context, target Target, method, base, uuid string, body map[string]any) (WriteResult, error) {
	if method == http.MethodPut && body == nil {
		return WriteResult{}, result.New("input", "请求体必须是 JSON object")
	}
	path, err := resourcePath(base, uuid)
	if err != nil {
		return WriteResult{}, err
	}
	_, err = c.write(ctx, target, method, path, body)
	if err != nil {
		return WriteResult{}, err
	}
	return WriteResult{UUID: uuid}, nil
}

func (c Client) writeUUID(ctx context.Context, target Target, method, path string, body map[string]any) (WriteResult, error) {
	resp, err := c.write(ctx, target, method, path, body)
	if err != nil {
		return WriteResult{}, err
	}
	var uuid string
	var ok bool
	if resp.Data != nil {
		uuid, ok = resp.Data["uuid"].(string)
	}
	if !ok || strings.TrimSpace(uuid) == "" || containsSecret(uuid, target.APIKey) || ValidateResourceID(uuid) != nil {
		return WriteResult{}, writeUnknown(&result.Error{
			HTTPStatus: resp.StatusCode,
			RequestID:  responseRequestID(resp.Header, resp.Body, target.APIKey),
		})
	}
	return WriteResult{UUID: uuid}, nil
}

// Raw 返回 /system/info 的安全投影。接口有意不接受任意路径，避免把 Raw 变成
// 绕过操作定义的通用 HTTP 入口。
func (c Client) Raw(ctx context.Context, target Target, method, path string) (any, error) {
	if method != http.MethodGet || path != infoPath {
		return nil, result.New("incompatible", "只允许读取已确认的 system/info 接口")
	}
	resp, err := c.fetch(ctx, target, method, path)
	if err != nil {
		return nil, err
	}

	return map[string]any{
		"code": 0,
		"msg":  "ok",
		"data": projectInfo(resp.Data, target.APIKey),
	}, nil
}

// APIRequest 执行登记在 operation registry 中的请求，并返回经过安全投影的 JSON 数据。
func (c Client) APIRequest(ctx context.Context, target Target, operation Operation, body map[string]any) (any, error) {
	if resolved, ok := ResolveOperation(operation.Method, operation.Path); !ok || resolved.ID != operation.ID {
		return nil, result.New("incompatible", "请求不在受控操作登记表中")
	}
	if !operation.ReadOnly {
		return nil, result.New("incompatible", "受控 API 暂不开放资源写入")
	}
	var resp response
	var err error
	if operation.Method == http.MethodGet {
		resp, err = c.do(ctx, target, http.MethodGet, operation.Path, nil, false)
	} else {
		resp, err = c.readPost(ctx, target, operation.Path, body)
	}
	if err != nil {
		return nil, err
	}
	return redactConnectionSecret(resp.Data, target.APIKey), nil
}

func (c Client) readPost(ctx context.Context, target Target, path string, body map[string]any) (response, error) {
	encoded, err := json.Marshal(bodyOrEmpty(body))
	if err != nil {
		return response{}, result.New("input", "请求体格式无法编码")
	}
	if len(encoded) > maxRequestBytes {
		return response{}, result.New("input", "请求体过大")
	}
	return c.do(ctx, target, http.MethodPost, path, bytes.NewReader(encoded), true)
}

func bodyOrEmpty(body map[string]any) map[string]any {
	if body == nil {
		return map[string]any{}
	}
	return body
}

func (c Client) fetch(ctx context.Context, target Target, method, path string) (response, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if method != http.MethodGet || !knownGetPath(path) {
		return response{}, result.New("incompatible", "只允许读取已确认的 system 或资源接口")
	}
	return c.do(ctx, target, method, path, nil, false)
}

func (c Client) write(ctx context.Context, target Target, method, path string, body map[string]any) (response, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if !knownWritePath(method, path) {
		return response{}, result.New("incompatible", "只允许执行已确认的资源写操作")
	}
	var payload io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return response{}, result.New("input", "请求体格式无法编码")
		}
		if len(encoded) > maxRequestBytes {
			return response{}, result.New("input", "请求体过大")
		}
		payload = bytes.NewReader(encoded)
	}
	response, err := c.do(ctx, target, method, path, payload, true)
	if err != nil {
		return response, classifyWriteError(err)
	}
	return response, nil
}

func (c Client) writeMultipart(ctx context.Context, target Target, path string, body []byte, contentType string) (response, error) {
	if !knownWritePath(http.MethodPost, path) {
		return response{}, result.New("incompatible", "只允许执行已确认的资源写操作")
	}
	response, err := c.doWithContentType(ctx, target, http.MethodPost, path, bytes.NewReader(body), true, contentType)
	if err != nil {
		return response, classifyWriteError(err)
	}
	return response, nil
}

func (c Client) taskWrite(ctx context.Context, target Target, method, path string, body map[string]any) (string, error) {
	resp, err := c.write(ctx, target, method, path, body)
	if err != nil {
		return "", err
	}
	return taskID(resp)
}

func taskID(resp response) (string, error) {
	id, ok := stringValue(resp.Data["task_id"])
	if !ok || strings.TrimSpace(id) == "" {
		return "", writeUnknown(&result.Error{
			HTTPStatus: resp.StatusCode,
			RequestID:  responseRequestID(resp.Header, resp.Body, ""),
		})
	}
	return id, nil
}

func (c Client) skillWrite(ctx context.Context, target Target, suffix string, body map[string]any) (map[string]any, error) {
	resp, err := c.write(ctx, target, http.MethodPost, skillsPath+suffix, body)
	if err != nil {
		return nil, err
	}
	return resp.Data, nil
}

func (c Client) skillMultipart(ctx context.Context, target Target, suffix, filename string, file io.Reader, sourcePaths []string) (map[string]any, error) {
	body, contentType, err := multipartBody(filename, file, sourcePaths, maxUploadBytes)
	if err != nil {
		return nil, err
	}
	resp, err := c.writeMultipart(ctx, target, skillsPath+suffix, body, contentType)
	if err != nil {
		return nil, err
	}
	return resp.Data, nil
}

func (c Client) previewJSON(ctx context.Context, target Target, path string, body map[string]any) (response, error) {
	if !knownPreviewPath(path) {
		return response{}, result.New("incompatible", "只允许调用已确认的预览接口")
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		return response{}, result.New("input", "请求体格式无法编码")
	}
	if len(encoded) > maxRequestBytes {
		return response{}, result.New("input", "请求体过大")
	}
	return c.do(ctx, target, http.MethodPost, path, bytes.NewReader(encoded), true)
}

func (c Client) previewMultipart(ctx context.Context, target Target, path string, body []byte, contentType string) (response, error) {
	if !knownPreviewPath(path) {
		return response{}, result.New("incompatible", "只允许调用已确认的预览接口")
	}
	return c.doWithContentType(ctx, target, http.MethodPost, path, bytes.NewReader(body), true, contentType)
}

func multipartBody(filename string, file io.Reader, sourcePaths []string, maxBytes int64) ([]byte, string, error) {
	if file == nil {
		return nil, "", result.New("input", "上传文件不能为空")
	}
	content, err := io.ReadAll(io.LimitReader(file, maxBytes+1))
	if err != nil {
		return nil, "", result.New("input", "读取上传文件失败")
	}
	if int64(len(content)) > maxBytes {
		return nil, "", result.New("input", "上传文件过大")
	}
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", filepath.Base(filename))
	if err != nil {
		return nil, "", result.New("input", "无法构造上传请求")
	}
	if _, err := part.Write(content); err != nil {
		return nil, "", result.New("input", "无法构造上传请求")
	}
	for _, sourcePath := range sourcePaths {
		if err := writer.WriteField("source_paths", sourcePath); err != nil {
			return nil, "", result.New("input", "无法构造上传请求")
		}
	}
	if err := writer.Close(); err != nil {
		return nil, "", result.New("input", "无法构造上传请求")
	}
	if int64(body.Len()) > maxBytes {
		return nil, "", result.New("input", "上传请求过大")
	}
	return body.Bytes(), writer.FormDataContentType(), nil
}

func (c Client) do(ctx context.Context, target Target, method, path string, requestBody io.Reader, allowNilData bool) (response, error) {
	return c.doWithContentType(ctx, target, method, path, requestBody, allowNilData, "application/json")
}

func (c Client) doWithContentType(ctx context.Context, target Target, method, path string, requestBody io.Reader, allowNilData bool, contentType string) (response, error) {
	base, err := endpoint.Normalize(target.Endpoint)
	if err != nil {
		return response{}, result.New("input", "服务地址无效")
	}
	requestURL := strings.TrimRight(base, "/") + path

	request, err := http.NewRequestWithContext(ctx, method, requestURL, requestBody)
	if err != nil {
		return response{}, result.New("input", "无法构造 HTTP 请求")
	}
	if target.APIKey != "" {
		request.Header.Set("X-API-Key", target.APIKey)
	}
	if requestBody != nil && contentType != "" {
		request.Header.Set("Content-Type", contentType)
	}

	transport := c.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	httpClient := &http.Client{
		Transport: transport,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	timeout := target.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	request = request.WithContext(callCtx)

	resp, err := httpClient.Do(request)
	if err != nil {
		return response{}, transportError(callCtx, err)
	}
	defer resp.Body.Close()

	responseBody, tooLarge, readErr := readLimited(resp.Body)
	if readErr != nil {
		err := transportError(callCtx, readErr)
		err.HTTPStatus = resp.StatusCode
		err.RequestID = responseRequestID(resp.Header, envelope{}, target.APIKey)
		return response{}, err
	}
	if tooLarge {
		err := protocolError("服务响应过大", resp.StatusCode, responseRequestID(resp.Header, envelope{}, target.APIKey))
		return response{}, err
	}
	parsed, parseErr := parseEnvelope(responseBody)
	requestID := responseRequestID(resp.Header, parsed, target.APIKey)
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return response{}, httpError(resp.StatusCode, parsed.Code, requestID, target.APIKey)
	}
	if parseErr != nil {
		return response{}, protocolError("服务返回的响应格式无效", resp.StatusCode, requestID)
	}
	if !isSuccessCode(parsed.Code) {
		return response{}, businessError(parsed.Code, requestID, resp.StatusCode, target.APIKey)
	}
	data, ok := parseDataObject(parsed.Data)
	if !ok && allowNilData && string(parsed.Data) == "null" {
		data = nil
		ok = true
	}
	if !ok {
		return response{}, protocolError("服务返回的诊断数据格式无效", resp.StatusCode, requestID)
	}
	if path == infoPath && !validInfoData(data, target.APIKey) {
		return response{}, protocolError("服务返回的诊断数据缺少有效版本", resp.StatusCode, requestID)
	}
	return response{
		StatusCode: resp.StatusCode,
		Header:     resp.Header,
		Body:       parsed,
		Data:       data,
	}, nil
}

func knownWritePath(method, path string) bool {
	basePath, query, ok := splitWritePath(path)
	if !ok {
		return false
	}
	path = basePath
	if len(query) != 0 {
		if method != http.MethodDelete || !safeNestedPath(path, pluginsPath, "", 2) || len(query) != 1 || len(query["delete_data"]) != 1 || query.Get("delete_data") != "true" {
			return false
		}
	}
	if method == http.MethodPost && path == documentUploadPath {
		return true
	}
	if method == http.MethodPost && (path == providersPath || path == llmModelsPath || path == embeddingModelsPath || path == rerankModelsPath) {
		return true
	}
	if method == http.MethodPost {
		for _, modelBase := range []string{llmModelsPath, embeddingModelsPath, rerankModelsPath} {
			if nestedPath(path, modelBase, 1, "test") {
				return true
			}
		}
	}
	if method == http.MethodPut || method == http.MethodDelete {
		for _, base := range []string{providersPath, llmModelsPath, embeddingModelsPath, rerankModelsPath} {
			if identifierPath(path, base) {
				return true
			}
		}
	}
	if method == http.MethodPost && strings.HasPrefix(path, knowledgeBasesPath+"/") && strings.HasSuffix(path, "/files") {
		identifier := strings.TrimSuffix(strings.TrimPrefix(path, knowledgeBasesPath+"/"), "/files")
		return identifier != "" && !strings.Contains(identifier, "/")
	}
	if method == http.MethodPost && path == knowledgeBasesPath {
		return true
	}
	if method == http.MethodDelete && strings.HasPrefix(path, knowledgeBasesPath+"/") {
		value := strings.TrimPrefix(path, knowledgeBasesPath+"/")
		parts := strings.Split(value, "/")
		if len(parts) == 3 && parts[1] == "files" && parts[0] != "" && parts[2] != "" {
			return true
		}
	}
	if (method == http.MethodPut || method == http.MethodDelete) && strings.HasPrefix(path, knowledgeBasesPath+"/") {
		value := strings.TrimPrefix(path, knowledgeBasesPath+"/")
		return value != "" && !strings.Contains(value, "/")
	}
	if method == http.MethodPost && path == mcpServersPath {
		return true
	}
	if method == http.MethodPost && path == skillsPath {
		return true
	}
	if method == http.MethodPut && strings.HasPrefix(path, skillsPath+"/") && strings.Contains(path, "/files/") {
		value := strings.TrimPrefix(path, skillsPath+"/")
		parts := strings.SplitN(value, "/files/", 2)
		_, err := safeFilePath(parts[1])
		return parts[0] != "" && !strings.Contains(parts[0], "/") && err == nil
	}
	if (method == http.MethodPut || method == http.MethodDelete) && strings.HasPrefix(path, skillsPath+"/") {
		value := strings.TrimPrefix(path, skillsPath+"/")
		return value != "" && !strings.Contains(value, "/")
	}
	if method == http.MethodPut && strings.HasPrefix(path, pluginsPath+"/") && strings.HasSuffix(path, "/config") {
		return safeNestedPath(path, pluginsPath, "/config", 2)
	}
	if method == http.MethodDelete && strings.HasPrefix(path, pluginsPath+"/") {
		return safeNestedPath(path, pluginsPath, "", 2)
	}
	if method == http.MethodPut || method == http.MethodDelete {
		if strings.HasPrefix(path, mcpServersPath+"/") {
			value := strings.TrimPrefix(path, mcpServersPath+"/")
			return value != "" && !strings.Contains(value, "/")
		}
	}
	if method == http.MethodPost && (path == botsPath || path == pipelinesPath) {
		return true
	}
	if method == http.MethodPut || method == http.MethodDelete {
		for _, base := range []string{botsPath, pipelinesPath} {
			if strings.HasPrefix(path, base+"/") && !strings.Contains(strings.TrimPrefix(path, base+"/"), "/") {
				return true
			}
		}
	}
	if method == http.MethodPost && strings.HasPrefix(path, pipelinesPath+"/") && strings.HasSuffix(path, "/copy") {
		identifier := strings.TrimSuffix(strings.TrimPrefix(path, pipelinesPath+"/"), "/copy")
		return identifier != "" && !strings.Contains(identifier, "/")
	}
	if method == http.MethodPost {
		for _, suffix := range []string{"/install/github", "/install/marketplace", "/install/local"} {
			if path == pluginsPath+suffix {
				return true
			}
		}
		if strings.HasPrefix(path, pluginsPath+"/") && strings.HasSuffix(path, "/upgrade") {
			return safeNestedPath(path, pluginsPath, "/upgrade", 2)
		}
		if strings.HasPrefix(path, skillsPath+"/install/") {
			return path == skillsPath+"/install/github" || path == skillsPath+"/install/upload"
		}
		if strings.HasPrefix(path, mcpServersPath+"/") && strings.HasSuffix(path, "/test") {
			return safeNestedPath(path, mcpServersPath, "/test", 1)
		}
	}
	return false
}

func splitWritePath(path string) (string, url.Values, bool) {
	if !strings.HasPrefix(path, "/") || strings.Contains(path, "#") {
		return "", nil, false
	}
	base, rawQuery, hasQuery := strings.Cut(path, "?")
	if strings.Contains(base, "?") || (hasQuery && strings.Contains(rawQuery, "?")) {
		return "", nil, false
	}
	query, err := url.ParseQuery(rawQuery)
	if err != nil {
		return "", nil, false
	}
	return base, query, true
}

func knownPreviewPath(path string) bool {
	return path == skillsPath+"/install/github/preview" || path == skillsPath+"/install/upload/preview"
}

func safeNestedPath(path, base, suffix string, segments int) bool {
	value := strings.TrimSuffix(strings.TrimPrefix(path, base+"/"), suffix)
	parts := strings.Split(value, "/")
	if len(parts) != segments || strings.TrimSpace(value) == "" {
		return false
	}
	for _, part := range parts {
		if _, err := resourcePath("", part); err != nil {
			return false
		}
	}
	return true
}

func classifyWriteError(err error) error {
	failure := result.AsError(err)
	if failure.HTTPStatus == http.StatusTooManyRequests ||
		(failure.HTTPStatus >= http.StatusMultipleChoices && failure.HTTPStatus < http.StatusBadRequest) {
		return err
	}
	if failure.Kind == "network" || failure.Kind == "incompatible" ||
		(failure.Kind == "server" && failure.HTTPStatus >= http.StatusInternalServerError) {
		return writeUnknown(failure)
	}
	return err
}

func writeUnknown(cause ...*result.Error) *result.Error {
	err := result.New("network", "写操作结果未知，请检查服务端状态")
	err.Type = "result_unknown"
	if len(cause) != 0 && cause[0] != nil {
		err.HTTPStatus = cause[0].HTTPStatus
		err.ServerCode = cause[0].ServerCode
		err.RequestID = cause[0].RequestID
	}
	return err
}

func knownGetPath(path string) bool {
	queryIndex := strings.IndexByte(path, '?')
	if queryIndex >= 0 {
		base := path[:queryIndex]
		query, err := url.ParseQuery(path[queryIndex+1:])
		if err != nil {
			return false
		}
		if base == providersPath || identifierPath(base, providersPath) {
			return validSecretProjectionQuery(query)
		}
		for _, modelBase := range []string{llmModelsPath, embeddingModelsPath, rerankModelsPath} {
			if base == modelBase {
				return validModelListQuery(query)
			}
			if identifierPath(base, modelBase) {
				return validSecretProjectionQuery(query)
			}
		}
		if nestedPath(base, providersPath, 1, "scan-models") {
			return validScanModelsQuery(query)
		}
		if base != tasksPath &&
			!(strings.HasPrefix(base, mcpServersPath+"/") && strings.HasSuffix(base, "/logs")) &&
			!(strings.HasPrefix(base, pluginsPath+"/") && strings.HasSuffix(base, "/logs")) &&
			!(strings.HasPrefix(base, skillsPath+"/") && strings.HasSuffix(base, "/files")) {
			return false
		}
		path = base
	}
	if path == infoPath || path == contextPath || path == capabilitiesPath || path == botsPath || path == pipelinesPath || path == tasksPath || path == knowledgeBasesPath || path == pluginsPath || path == skillsPath || path == mcpServersPath {
		return true
	}
	for _, base := range []string{botsPath, pipelinesPath, tasksPath, knowledgeBasesPath, pluginsPath, mcpServersPath} {
		if strings.HasPrefix(path, base+"/") && !strings.Contains(strings.TrimPrefix(path, base+"/"), "/") {
			return true
		}
	}
	if strings.HasPrefix(path, knowledgeBasesPath+"/") && strings.HasSuffix(path, "/files") {
		value := strings.TrimSuffix(strings.TrimPrefix(path, knowledgeBasesPath+"/"), "/files")
		return value != "" && !strings.Contains(value, "/")
	}
	for _, suffix := range []string{"/resources", "/resource-templates"} {
		if strings.HasPrefix(path, mcpServersPath+"/") && strings.HasSuffix(path, suffix) {
			value := strings.TrimSuffix(strings.TrimPrefix(path, mcpServersPath+"/"), suffix)
			return value != "" && !strings.Contains(value, "/")
		}
	}
	if strings.HasPrefix(path, mcpServersPath+"/") && strings.Contains(path, "/logs") {
		value := strings.TrimPrefix(path, mcpServersPath+"/")
		query := strings.IndexByte(value, '?')
		if query >= 0 {
			value = value[:query]
		}
		return strings.HasSuffix(value, "/logs") && !strings.Contains(strings.TrimSuffix(value, "/logs"), "/")
	}
	if strings.HasPrefix(path, skillsPath+"/") {
		value := strings.TrimPrefix(path, skillsPath+"/")
		if !strings.Contains(value, "/") {
			return true
		}
		if strings.HasSuffix(value, "/files") || strings.HasSuffix(value, "/preview") {
			return strings.Count(value, "/") == 1
		}
		if strings.Contains(value, "/files/") {
			parts := strings.SplitN(value, "/files/", 2)
			_, err := safeFilePath(parts[1])
			return parts[0] != "" && err == nil
		}
	}
	if strings.HasPrefix(path, pluginsPath+"/") && strings.Count(strings.TrimPrefix(path, pluginsPath+"/"), "/") == 1 {
		return true
	}
	if strings.HasPrefix(path, pluginsPath+"/") {
		value := strings.TrimPrefix(path, pluginsPath+"/")
		for _, suffix := range []string{"/config", "/logs"} {
			if strings.HasSuffix(value, suffix) {
				return strings.Count(strings.TrimSuffix(value, suffix), "/") == 1
			}
		}
	}
	if nestedPath(path, providersPath, 1, "scan-models") {
		return true
	}
	return false
}

func validSecretProjectionQuery(query url.Values) bool {
	if len(query) != 1 {
		return false
	}
	values, ok := query["include_secret"]
	return ok && len(values) == 1 && values[0] == "false"
}

func validModelListQuery(query url.Values) bool {
	if !validSecretProjectionQuery(url.Values{"include_secret": query["include_secret"]}) {
		return false
	}
	if len(query) == 1 {
		return true
	}
	values, ok := query["provider_uuid"]
	if !ok || len(values) != 1 {
		return false
	}
	_, err := resourcePath("", values[0])
	return err == nil
}

func validScanModelsQuery(query url.Values) bool {
	if len(query) == 0 {
		return true
	}
	values, ok := query["type"]
	if !ok || len(values) != 1 {
		return false
	}
	return values[0] == ModelTypeLLM || values[0] == ModelTypeEmbedding || values[0] == ModelTypeRerank
}

func capabilitySchemaVersion(value any) (int, bool) {
	number, ok := value.(json.Number)
	if !ok {
		return 0, false
	}
	version, err := strconv.Atoi(string(number))
	return version, err == nil
}

func resourcePath(base, identifier string) (string, error) {
	if strings.TrimSpace(identifier) == "" || identifier == "." || identifier == ".." ||
		strings.ContainsAny(identifier, "/\\?#") || strings.IndexFunc(identifier, func(r rune) bool { return r < 0x20 || r == 0x7f }) >= 0 {
		return "", result.New("input", "资源 ID 必须是单个安全的路径段")
	}
	escaped := url.PathEscape(identifier)
	if escaped == "." || escaped == ".." || strings.Contains(escaped, "/") {
		return "", result.New("input", "资源 ID 必须是单个安全的路径段")
	}
	return base + "/" + escaped, nil
}

func mcpServerPath(name string) (string, error) {
	if strings.TrimSpace(name) == "" || strings.TrimSpace(name) != name || strings.ContainsAny(name, "\\?#") ||
		strings.IndexFunc(name, func(r rune) bool { return r < 0x20 || r == 0x7f }) >= 0 {
		return "", result.New("input", "MCP Server 名称不是安全的路径参数")
	}
	for _, segment := range strings.Split(name, "/") {
		if strings.TrimSpace(segment) == "" || segment == "." || segment == ".." {
			return "", result.New("input", "MCP Server 名称不是安全的路径参数")
		}
	}
	return mcpServersPath + "/" + url.PathEscape(name), nil
}

func pluginResourcePath(author, name, suffix string) (string, error) {
	authorPath, err := resourcePath(pluginsPath, author)
	if err != nil {
		return "", err
	}
	namePath, err := resourcePath(authorPath, name)
	if err != nil {
		return "", err
	}
	return namePath + suffix, nil
}

func skillResourcePath(name, suffix string) (string, error) {
	path, err := resourcePath(skillsPath, name)
	if err != nil {
		return "", err
	}
	return path + suffix, nil
}

func skillFileResourcePath(name, filePath string) (string, error) {
	base, err := skillResourcePath(name, "/files")
	if err != nil {
		return "", err
	}
	parts, err := safeFilePath(filePath)
	if err != nil {
		return "", err
	}
	escaped := make([]string, 0, len(parts))
	for _, part := range parts {
		escaped = append(escaped, url.PathEscape(part))
	}
	return base + "/" + strings.Join(escaped, "/"), nil
}

func safeFilePath(value string) ([]string, error) {
	value = strings.TrimSpace(value)
	if value == "." {
		return []string{"."}, nil
	}
	if value == "" || strings.HasPrefix(value, "/") || strings.Contains(value, "\\") || strings.ContainsAny(value, "?#") ||
		strings.IndexFunc(value, func(r rune) bool { return r < 0x20 || r == 0x7f }) >= 0 {
		return nil, result.New("input", "文件路径不是安全的相对路径")
	}
	parts := strings.Split(value, "/")
	for _, part := range parts {
		decoded, err := url.PathUnescape(part)
		if err != nil || part == "" || part == "." || part == ".." || decoded == "." || decoded == ".." ||
			strings.ContainsAny(decoded, "/\\") {
			return nil, result.New("input", "文件路径不是安全的相对路径")
		}
	}
	return parts, nil
}

func taskPath(identifier string) (string, error) {
	if identifier == "" {
		return "", result.New("input", "任务 ID 必须是非负整数")
	}
	parsed, err := strconv.ParseUint(identifier, 10, 64)
	if err != nil || strconv.FormatUint(parsed, 10) != identifier {
		return "", result.New("input", "任务 ID 必须是非负整数")
	}
	return tasksPath + "/" + identifier, nil
}

func parseResourceList(resp response, key string, project func(map[string]any, string) map[string]any, secret string) ([]map[string]any, error) {
	raw, ok := resp.Data[key].([]any)
	if !ok {
		return nil, protocolError("服务返回的资源列表格式无效", resp.StatusCode, responseRequestID(resp.Header, resp.Body, secret))
	}
	items := make([]map[string]any, 0, len(raw))
	for _, value := range raw {
		item, ok := value.(map[string]any)
		if !ok {
			return nil, protocolError("服务返回的资源格式无效", resp.StatusCode, responseRequestID(resp.Header, resp.Body, secret))
		}
		if !validResourceUUID(item, secret) {
			return nil, protocolError("服务返回的资源缺少有效 UUID", resp.StatusCode, responseRequestID(resp.Header, resp.Body, secret))
		}
		items = append(items, project(item, secret))
	}
	return items, nil
}

func parseResourceObject(resp response, key string, project func(map[string]any, string) map[string]any, secret string) (map[string]any, error) {
	item, ok := resp.Data[key].(map[string]any)
	if !ok {
		return nil, protocolError("服务返回的资源格式无效", resp.StatusCode, responseRequestID(resp.Header, resp.Body, secret))
	}
	if !validResourceUUID(item, secret) {
		return nil, protocolError("服务返回的资源缺少有效 UUID", resp.StatusCode, responseRequestID(resp.Header, resp.Body, secret))
	}
	return project(item, secret), nil
}

func validResourceUUID(value map[string]any, secret string) bool {
	uuid, ok := value["uuid"].(string)
	return ok && strings.TrimSpace(uuid) != "" && !containsSecret(uuid, secret)
}

func parseTaskData(value map[string]any, identifier string) (Task, bool) {
	taskID, ok := value["id"].(json.Number)
	if !ok || taskID.String() != identifier {
		return Task{}, false
	}
	status, ok := value["status"].(string)
	if !ok {
		return Task{}, false
	}
	switch status {
	case "running", "succeeded", "failed", "cancelled":
	default:
		return Task{}, false
	}
	taskType, taskTypeOK := value["task_type"].(string)
	kind, kindOK := value["kind"].(string)
	createdAt, createdAtOK := value["created_at"].(json.Number)
	if !taskTypeOK || strings.TrimSpace(taskType) == "" || !kindOK || strings.TrimSpace(kind) == "" || !createdAtOK {
		return Task{}, false
	}
	task := Task{
		ID:        taskID,
		TaskType:  taskType,
		Kind:      kind,
		Status:    status,
		Result:    value["result"],
		CreatedAt: createdAt,
	}
	if rawError, exists := value["error"]; exists && rawError != nil {
		errorValue, ok := rawError.(map[string]any)
		if !ok {
			return Task{}, false
		}
		task.Error = &TaskError{Type: safeString(errorValue["type"]), Message: safeString(errorValue["message"])}
		if task.Error.Type == "" || task.Error.Message == "" {
			return Task{}, false
		}
	}
	if (status == "failed" || status == "cancelled") != (task.Error != nil) {
		return Task{}, false
	}
	if status != "succeeded" && task.Result != nil {
		return Task{}, false
	}
	return task, true
}

func stringValue(value any) (string, bool) {
	switch typed := value.(type) {
	case string:
		return typed, true
	case json.Number:
		return typed.String(), true
	case float64:
		return strconv.FormatInt(int64(typed), 10), typed == float64(int64(typed))
	default:
		return "", false
	}
}

func projectBot(value map[string]any, secret string) map[string]any {
	return projectScalarFields(value, []string{
		"uuid", "name", "description", "adapter", "enable", "use_pipeline_name", "use_pipeline_uuid", "created_at", "updated_at",
	}, secret)
}

func projectPipeline(value map[string]any, secret string) map[string]any {
	return projectScalarFields(value, []string{
		"uuid", "name", "description", "emoji", "for_version", "is_default", "created_at", "updated_at",
	}, secret)
}

func projectMCPServer(value map[string]any, secret string) map[string]any {
	return projectScalarFields(value, []string{
		"uuid", "name", "enable", "mode", "created_at", "updated_at",
	}, secret)
}

func projectKnowledgeBase(value map[string]any, secret string) map[string]any {
	return projectScalarFields(value, []string{
		"uuid", "name", "description", "knowledge_engine_plugin_id", "created_at", "updated_at",
	}, secret)
}

func projectPlugin(value map[string]any, author, name, secret string) map[string]any {
	projected := projectScalarFields(value, []string{"author", "name", "version", "description", "debug", "enabled", "status", "created_at", "updated_at"}, secret)
	metadata := value
	if manifest, ok := value["manifest"].(map[string]any); ok {
		if nested, ok := manifest["manifest"].(map[string]any); ok {
			if nestedMetadata, ok := nested["metadata"].(map[string]any); ok {
				metadata = nestedMetadata
			}
		}
	}
	if _, ok := projected["author"]; !ok && author != "" {
		projected["author"] = author
	} else if _, ok := projected["author"]; !ok {
		if candidate, ok := metadata["author"].(string); ok && !containsSecret(candidate, secret) {
			projected["author"] = candidate
		}
	}
	if _, ok := projected["name"]; !ok && name != "" {
		projected["name"] = name
	} else if _, ok := projected["name"]; !ok {
		if candidate, ok := metadata["name"].(string); ok && !containsSecret(candidate, secret) {
			projected["name"] = candidate
		}
	}
	for _, key := range []string{"version", "description"} {
		if _, exists := projected[key]; exists {
			continue
		}
		if candidate, ok := metadata[key].(string); ok && !containsSecret(candidate, secret) {
			projected[key] = candidate
		}
	}
	return projected
}

func projectSkillSummary(value map[string]any, secret string) map[string]any {
	return projectScalarFields(value, []string{"name", "display_name", "description", "package_root", "created_at", "updated_at"}, secret)
}

func parseSkillResponse(resp response, expectedName, secret string) (map[string]any, error) {
	item, ok := resp.Data["skill"].(map[string]any)
	if !ok {
		return nil, protocolError("服务返回的 Skill 格式无效", resp.StatusCode, responseRequestID(resp.Header, resp.Body, secret))
	}
	projected := projectSkillSummary(item, secret)
	name, ok := projected["name"].(string)
	if !ok || strings.TrimSpace(name) == "" || (expectedName != "" && name != expectedName) {
		return nil, protocolError("服务返回的 Skill 身份与请求不一致", resp.StatusCode, responseRequestID(resp.Header, resp.Body, secret))
	}
	if instructions, exists := item["instructions"]; exists {
		projected["instructions"] = redactConnectionSecret(instructions, secret)
	}
	return projected, nil
}

func projectScalarFields(value map[string]any, fields []string, secret string) map[string]any {
	projected := make(map[string]any, len(fields))
	for _, field := range fields {
		item, ok := value[field]
		if !ok || !safeResourceValue(item, secret) {
			continue
		}
		projected[field] = item
	}
	return projected
}

func safeResourceValue(value any, secret string) bool {
	switch value.(type) {
	case bool, nil:
		return true
	case string:
		return !containsSecret(value.(string), secret)
	default:
		return false
	}
}

func redactConnectionSecret(value any, secret string) any {
	switch typed := value.(type) {
	case map[string]any:
		projected := make(map[string]any, len(typed))
		for key, item := range typed {
			projected[key] = redactConnectionSecret(item, secret)
		}
		return projected
	case []any:
		items := make([]any, len(typed))
		for index, item := range typed {
			items[index] = redactConnectionSecret(item, secret)
		}
		return items
	case string:
		if secret != "" {
			return strings.ReplaceAll(typed, secret, "***")
		}
		return typed
	default:
		return value
	}
}

func parseContextData(data map[string]any) (Context, bool) {
	instanceUUID, instanceOK := data["instance_uuid"].(string)
	workspaceUUID, workspaceOK := data["workspace_uuid"].(string)
	apiKeyID, keyOK := data["api_key_id"].(string)
	permissionsValue, permissionsOK := data["permissions"].([]any)
	if !instanceOK || !workspaceOK || !keyOK || !permissionsOK ||
		strings.TrimSpace(instanceUUID) == "" || strings.TrimSpace(workspaceUUID) == "" || strings.TrimSpace(apiKeyID) == "" {
		return Context{}, false
	}
	permissions := make([]string, 0, len(permissionsValue))
	seen := make(map[string]struct{}, len(permissionsValue))
	for _, item := range permissionsValue {
		permission, ok := item.(string)
		if !ok {
			return Context{}, false
		}
		permission = strings.TrimSpace(permission)
		if permission == "" {
			return Context{}, false
		}
		if _, exists := seen[permission]; exists {
			continue
		}
		seen[permission] = struct{}{}
		permissions = append(permissions, permission)
	}
	sort.Strings(permissions)
	return Context{
		InstanceUUID:  instanceUUID,
		WorkspaceUUID: workspaceUUID,
		APIKeyID:      apiKeyID,
		Permissions:   permissions,
	}, true
}

func readLimited(body io.Reader) ([]byte, bool, error) {
	limited := io.LimitReader(body, maxResponseBytes+1)
	data, err := io.ReadAll(limited)
	if err != nil {
		return nil, false, err
	}
	if len(data) > maxResponseBytes {
		return nil, true, nil
	}
	return data, false, nil
}

func parseEnvelope(body []byte) (envelope, error) {
	var value map[string]json.RawMessage
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil || value == nil {
		return envelope{}, errors.New("response is not a JSON object")
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return envelope{}, errors.New("response has trailing JSON")
	}
	var parsed envelope
	parsed.Code = value["code"]
	parsed.Data = value["data"]
	if raw := value["msg"]; len(raw) != 0 {
		_ = json.Unmarshal(raw, &parsed.Msg)
	}
	if raw := value["request_id"]; len(raw) != 0 {
		_ = json.Unmarshal(raw, &parsed.RequestID)
	}
	if len(parsed.Code) == 0 {
		return envelope{}, errors.New("response has no code")
	}
	return parsed, nil
}

func parseDataObject(raw json.RawMessage) (map[string]any, bool) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, false
	}
	var data map[string]any
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.UseNumber()
	if err := decoder.Decode(&data); err != nil || data == nil {
		return nil, false
	}
	return data, true
}

func isSuccessCode(raw json.RawMessage) bool {
	var number json.Number
	if err := json.Unmarshal(raw, &number); err == nil && number.String() == "0" {
		return true
	}
	var text string
	return json.Unmarshal(raw, &text) == nil && text == "0"
}

func httpError(status int, code json.RawMessage, requestID, secret string) *result.Error {
	kind := "server"
	switch status {
	case http.StatusUnauthorized:
		kind = "auth"
	case http.StatusForbidden:
		kind = "permission"
	case http.StatusNotFound:
		kind = "not_found"
	case http.StatusMovedPermanently, http.StatusFound, http.StatusSeeOther,
		http.StatusTemporaryRedirect, http.StatusPermanentRedirect:
		kind = "incompatible"
	case http.StatusRequestTimeout, http.StatusTooManyRequests:
		kind = "network"
	}
	err := result.New(kind, httpStatusMessage(status))
	err.HTTPStatus = status
	err.ServerCode = safeServerCode(code, secret)
	err.RequestID = requestID
	return err
}

func businessError(code json.RawMessage, requestID string, status int, secret string) *result.Error {
	err := result.New("server", "服务拒绝了诊断请求")
	err.HTTPStatus = status
	err.ServerCode = safeServerCode(code, secret)
	err.RequestID = requestID
	return err
}

func protocolError(message string, status int, requestID string) *result.Error {
	err := result.New("incompatible", message)
	err.HTTPStatus = status
	err.RequestID = requestID
	return err
}

func transportMessage(ctx context.Context, err error) string {
	if errors.Is(ctx.Err(), context.Canceled) || errors.Is(err, context.Canceled) {
		return "请求已取消"
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) {
		return "请求超时"
	}
	return "无法连接服务"
}

func transportError(ctx context.Context, err error) *result.Error {
	if isTLSError(err) {
		failure := result.New("network", "TLS 证书验证失败")
		failure.Type = "tls_error"
		return failure
	}
	return result.New("network", transportMessage(ctx, err))
}

func isTLSError(err error) bool {
	var verificationError *tls.CertificateVerificationError
	if errors.As(err, &verificationError) {
		return true
	}
	var unknownAuthority x509.UnknownAuthorityError
	if errors.As(err, &unknownAuthority) {
		return true
	}
	var hostnameError x509.HostnameError
	if errors.As(err, &hostnameError) {
		return true
	}
	var certificateInvalid x509.CertificateInvalidError
	return errors.As(err, &certificateInvalid)
}

func responseRequestID(header http.Header, body envelope, secret string) string {
	if id := safeRequestID(header.Get("X-Request-Id"), secret); id != "" {
		return id
	}
	return safeRequestID(body.RequestID, secret)
}

func safeServerCode(raw json.RawMessage, secret string) any {
	if len(raw) == 0 {
		return nil
	}
	var number json.Number
	if err := json.Unmarshal(raw, &number); err == nil {
		if value, err := strconv.ParseInt(number.String(), 10, 64); err == nil {
			if containsSecret(strconv.FormatInt(value, 10), secret) {
				return nil
			}
			return value
		}
	}
	var text string
	if json.Unmarshal(raw, &text) != nil || len(text) == 0 || len(text) > maxServerCodeSize || containsSecret(text, secret) {
		return nil
	}
	for _, r := range text {
		if !(r == '_' || r == '-' || r == '.' || r == ':' || r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z') {
			return nil
		}
	}
	return text
}

func safeRequestID(value, secret string) string {
	if len(value) == 0 || len(value) > maxRequestIDSize || containsSecret(value, secret) {
		return ""
	}
	for _, r := range value {
		if r < 0x20 || r == 0x7f {
			return ""
		}
	}
	return value
}

func containsSecret(value, secret string) bool {
	return secret != "" && strings.Contains(value, secret)
}

func projectInfo(data map[string]any, secret string) map[string]any {
	projected := make(map[string]any, 7)
	for _, key := range []string{
		"version",
		"edition",
		"debug",
		"enable_marketplace",
		"disable_models_service",
		"mcp_stdio_enabled",
		"wizard_status",
	} {
		value, ok := data[key]
		if !ok {
			continue
		}
		switch key {
		case "version", "edition", "wizard_status":
			text, ok := value.(string)
			if ok && !containsSecret(text, secret) {
				projected[key] = value
			}
		default:
			if _, ok := value.(bool); ok {
				projected[key] = value
			}
		}
	}
	return projected
}

func validInfoData(data map[string]any, secret string) bool {
	version, ok := data["version"].(string)
	return ok && version != "" && !containsSecret(version, secret)
}

func safeString(value any) string {
	text, _ := value.(string)
	return text
}

func httpStatusMessage(status int) string {
	if status >= 300 && status < 400 {
		return "服务返回了重定向，CLI 不会自动跟随"
	}
	return fmt.Sprintf("服务返回 HTTP %d", status)
}
