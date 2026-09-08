package app

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/langbot-app/langbot-cli/internal/api"
	"github.com/langbot-app/langbot-cli/internal/config"
	endpointutil "github.com/langbot-app/langbot-cli/internal/endpoint"
	"github.com/langbot-app/langbot-cli/internal/result"
)

type Dependencies struct {
	In                io.Reader
	LookupEnv         func(string) (string, bool)
	DefaultConfigPath func() (string, error)
	Transport         http.RoundTripper
	Version           string
	Commit            string
	BuildDate         string
}

type Result struct {
	Data any
	Meta map[string]any
}

// WaitOptions 控制异步任务的本地轮询，不会向服务端发送取消请求。
type WaitOptions struct {
	Wait         bool
	PollInterval time.Duration
	WaitTimeout  time.Duration
}

// WritePreflight 是经过身份、绑定、权限和能力确认后的写连接快照。
type WritePreflight struct {
	Connection   config.Connection
	Identity     api.Context
	Capabilities api.Capabilities
}

func (p WritePreflight) Meta() map[string]any {
	return resourceMeta(p.Connection, p.Identity)
}

type Service struct {
	deps Dependencies
	ctx  context.Context
}

func New(deps Dependencies) *Service {
	if deps.In == nil {
		deps.In = strings.NewReader("")
	}
	if deps.LookupEnv == nil {
		deps.LookupEnv = os.LookupEnv
	}
	if deps.DefaultConfigPath == nil {
		deps.DefaultConfigPath = config.DefaultPath
	}
	return &Service{deps: deps, ctx: context.Background()}
}

func (s *Service) WithContext(ctx context.Context) *Service {
	copy := *s
	if ctx == nil {
		ctx = context.Background()
	}
	copy.ctx = ctx
	return &copy
}

func (s *Service) Version(ctx context.Context, server bool, options CheckOptions) (Result, error) {
	data := map[string]any{
		"version":    s.deps.Version,
		"commit":     s.deps.Commit,
		"build_date": s.deps.BuildDate,
	}
	if !server {
		return Result{Data: data}, nil
	}
	conn, err := s.connection(ctx, config.Options{
		Context: options.Context, Endpoint: options.Endpoint, Timeout: options.Timeout,
		ContextSet: options.ContextSet, EndpointSet: options.EndpointSet, TimeoutSet: options.TimeoutSet,
		APIKeyStdin: options.APIKeyStdin,
	})
	if err != nil {
		return Result{Meta: connectionMeta(conn)}, err
	}
	info, err := s.info(ctx, conn)
	if err != nil {
		return Result{Meta: connectionMeta(conn)}, err
	}
	data["server_version"] = info.Version
	data["server_edition"] = info.Edition
	return Result{Data: data, Meta: connectionMeta(conn)}, nil
}

func (s *Service) ContextList() (Result, error) {
	file, err := s.load()
	if err != nil {
		return Result{}, err
	}
	names := sortedContextNames(file)
	items := make([]map[string]any, 0, len(names))
	for _, name := range names {
		item := contextView(name, file.Contexts[name], name == file.CurrentContext)
		items = append(items, item)
	}
	return Result{Data: map[string]any{
		"current_context": file.CurrentContext,
		"contexts":        items,
	}}, nil
}

func (s *Service) ContextShow(name string, overrides ...CheckOptions) (Result, error) {
	file, err := s.load()
	if err != nil {
		return Result{}, err
	}
	ctx, ok := file.Contexts[name]
	if !ok {
		return Result{}, result.New("not_found", "context 不存在")
	}
	saved := contextView(name, ctx, name == file.CurrentContext)
	effective := map[string]any{}
	options := CheckOptions{}
	if len(overrides) != 0 {
		options = overrides[0]
	}
	if options.ContextSet && options.Context != name {
		return Result{}, result.New("input", "命令参数 context 与 --context 不一致")
	}
	conn, resolveErr := config.Resolve(file, config.Options{
		Context: name, Endpoint: options.Endpoint, Timeout: options.Timeout,
		ContextSet: true, EndpointSet: options.EndpointSet, TimeoutSet: options.TimeoutSet,
		APIKeyStdin: options.APIKeyStdin,
	}, s.deps.LookupEnv)
	if resolveErr == nil {
		effective = connectionView(conn)
	} else {
		if conn.Endpoint != "" {
			effective = connectionView(conn)
		}
		effective["error"] = result.AsError(resolveErr)
	}
	data := map[string]any{
		"saved":     saved,
		"effective": effective,
	}
	if resolveErr != nil {
		return Result{Data: data}, resolveErr
	}
	return Result{Data: data}, nil
}

func (s *Service) ContextAdd(name, endpoint, apiKeyEnv, expectedWorkspace, timeout string) (Result, error) {
	if err := validateContextName(name); err != nil {
		return Result{}, err
	}
	if strings.TrimSpace(endpoint) == "" {
		return Result{}, result.New("input", "新增 context 必须提供 endpoint")
	}
	var saved config.Context
	var current string
	err := s.update(func(file *config.File) error {
		if _, ok := file.Contexts[name]; ok {
			return result.New("precondition", "context 已存在")
		}
		ctx := config.Context{Endpoint: endpoint, ExpectedWorkspaceUUID: expectedWorkspace, Timeout: timeout}
		if apiKeyEnv != "" {
			ctx.Credential = &config.Credential{Type: "env", Name: apiKeyEnv}
		}
		file.Contexts[name] = ctx
		saved = ctx
		current = file.CurrentContext
		return nil
	})
	if err != nil {
		return Result{}, err
	}
	return localContextResult(name, saved, current), nil
}

func (s *Service) ContextUpdate(name, endpoint, apiKeyEnv, expectedWorkspace, timeout string, endpointSet, apiKeySet, expectedSet, timeoutSet, clearCredential, clearBinding bool) (Result, error) {
	if err := validateContextName(name); err != nil {
		return Result{}, err
	}
	if !endpointSet && !apiKeySet && !expectedSet && !timeoutSet && !clearCredential && !clearBinding {
		return Result{}, result.New("input", "update 至少需要一个修改项")
	}
	var saved config.Context
	var current string
	err := s.update(func(file *config.File) error {
		ctx, ok := file.Contexts[name]
		if !ok {
			return result.New("not_found", "context 不存在")
		}
		if endpointSet {
			if strings.TrimSpace(endpoint) == "" {
				return result.New("input", "endpoint 不能为空")
			}
			oldEndpoint, oldErr := endpointutil.Normalize(ctx.Endpoint)
			newEndpoint, newErr := endpointutil.Normalize(endpoint)
			if oldErr != nil || newErr != nil {
				return result.New("input", "endpoint 必须是有效的 HTTP(S) 服务根地址")
			}
			ctx.Endpoint = endpoint
			if oldEndpoint != newEndpoint {
				if !apiKeySet && !clearCredential {
					ctx.Credential = nil
				}
				if !expectedSet && !clearBinding {
					ctx.ExpectedWorkspaceUUID = ""
				}
			}
		}
		if clearCredential {
			ctx.Credential = nil
		}
		if apiKeySet {
			if clearCredential {
				return result.New("input", "不能同时设置 --api-key-env 和 --clear-credential")
			}
			if strings.TrimSpace(apiKeyEnv) == "" {
				return result.New("input", "api-key-env 不能为空")
			}
			ctx.Credential = &config.Credential{Type: "env", Name: apiKeyEnv}
		}
		if clearBinding {
			ctx.ExpectedWorkspaceUUID = ""
		}
		if expectedSet {
			if clearBinding {
				return result.New("input", "不能同时设置 --expect-workspace 和 --clear-binding")
			}
			ctx.ExpectedWorkspaceUUID = expectedWorkspace
		}
		if timeoutSet {
			ctx.Timeout = timeout
		}
		file.Contexts[name] = ctx
		saved = ctx
		current = file.CurrentContext
		return nil
	})
	if err != nil {
		return Result{}, err
	}
	return localContextResult(name, saved, current), nil
}

func (s *Service) ContextUse(name string) (Result, error) {
	var saved config.Context
	var current string
	err := s.update(func(file *config.File) error {
		ctx, ok := file.Contexts[name]
		if !ok {
			return result.New("not_found", "context 不存在")
		}
		file.CurrentContext = name
		saved = ctx
		current = file.CurrentContext
		return nil
	})
	if err != nil {
		return Result{}, err
	}
	return localContextResult(name, saved, current), nil
}

func (s *Service) ContextRemove(name string, confirmed bool) (Result, error) {
	err := s.update(func(file *config.File) error {
		if _, ok := file.Contexts[name]; !ok {
			return result.New("not_found", "context 不存在")
		}
		if file.CurrentContext == name && !confirmed {
			return result.New("input", "删除当前 context 需要 --yes")
		}
		delete(file.Contexts, name)
		if file.CurrentContext == name {
			file.CurrentContext = ""
		}
		return nil
	})
	if err != nil {
		return Result{}, err
	}
	return Result{Data: map[string]any{"removed": name}}, nil
}

type CheckOptions struct {
	Name        string
	All         bool
	Context     string
	Endpoint    string
	Timeout     string
	ContextSet  bool
	EndpointSet bool
	TimeoutSet  bool
	APIKeyStdin bool
}

func (s *Service) Check(ctx context.Context, options CheckOptions) (Result, error) {
	if options.All {
		if options.Name != "" || options.EndpointSet || options.APIKeyStdin || options.ContextSet {
			return Result{}, result.New("input", "context check --all 不接受单目标覆盖参数")
		}
		if _, ok := s.deps.LookupEnv("LANGBOT_ENDPOINT"); ok {
			return Result{}, result.New("input", "context check --all 不接受 LANGBOT_ENDPOINT")
		}
		if _, ok := s.deps.LookupEnv("LANGBOT_API_KEY"); ok {
			return Result{}, result.New("input", "context check --all 不接受 LANGBOT_API_KEY")
		}
		if _, ok := s.deps.LookupEnv("LANGBOT_CONTEXT"); ok {
			return Result{}, result.New("input", "context check --all 不接受 LANGBOT_CONTEXT")
		}
		return s.checkAll(ctx, options)
	}
	if options.Name != "" && options.ContextSet && options.Name != options.Context {
		return Result{}, result.New("input", "命令参数 context 与 --context 不一致")
	}
	configOptions := config.Options{
		Context:     options.Context,
		Endpoint:    options.Endpoint,
		Timeout:     options.Timeout,
		ContextSet:  options.ContextSet,
		EndpointSet: options.EndpointSet,
		TimeoutSet:  options.TimeoutSet,
		APIKeyStdin: options.APIKeyStdin,
	}
	if options.Name != "" {
		configOptions.Context = options.Name
		configOptions.ContextSet = true
	}
	file, err := s.load()
	if err != nil {
		return Result{}, err
	}
	conn, err := s.resolve(ctx, file, configOptions)
	if err != nil {
		return Result{Meta: connectionMeta(conn)}, err
	}
	info, err := s.info(ctx, conn)
	var identity api.Context
	var identityErr error
	if err == nil || hasHTTPResponse(err) {
		identity, identityErr = s.identity(ctx, conn)
	}
	data := checkData(conn, info, err, identity, identityErr)
	callErr := firstError(err, identityErr)
	if callErr == nil {
		callErr = workspaceBindingError(conn, identity)
		if callErr != nil {
			data["error"] = result.AsError(callErr)
		}
	}
	if callErr != nil {
		return Result{Data: data, Meta: connectionMeta(conn)}, callErr
	}
	return Result{Data: data, Meta: connectionMeta(conn)}, nil
}

func (s *Service) Status(ctx context.Context, options CheckOptions) (Result, error) {
	file, err := s.load()
	if err != nil {
		return Result{}, err
	}
	conn, err := s.resolve(ctx, file, config.Options{
		Context: options.Context, Endpoint: options.Endpoint, Timeout: options.Timeout,
		ContextSet: options.ContextSet, EndpointSet: options.EndpointSet,
		TimeoutSet: options.TimeoutSet, APIKeyStdin: options.APIKeyStdin,
	})
	if err != nil {
		return Result{Meta: connectionMeta(conn)}, err
	}
	info, infoErr := s.info(ctx, conn)
	data := checkData(conn, info, infoErr, api.Context{}, nil)
	data["identity"] = "unknown"
	if infoErr != nil {
		return Result{Data: data, Meta: connectionMeta(conn)}, infoErr
	}
	return Result{Data: data, Meta: connectionMeta(conn)}, nil
}

func (s *Service) RawInfo(ctx context.Context, options CheckOptions, methodAndPath ...string) (Result, error) {
	method, path := "GET", "/api/v1/system/info"
	if len(methodAndPath) == 2 {
		method, path = methodAndPath[0], methodAndPath[1]
	}
	if method != "GET" || path != "/api/v1/system/info" {
		return Result{}, result.New("incompatible", "只允许读取已确认的 system/info 接口")
	}
	file, err := s.load()
	if err != nil {
		return Result{}, err
	}
	conn, err := s.resolve(ctx, file, config.Options{
		Context: options.Context, Endpoint: options.Endpoint, Timeout: options.Timeout,
		ContextSet: options.ContextSet, EndpointSet: options.EndpointSet,
		TimeoutSet: options.TimeoutSet, APIKeyStdin: options.APIKeyStdin,
	})
	if err != nil {
		return Result{Meta: connectionMeta(conn)}, err
	}
	client := api.Client{Transport: s.deps.Transport}
	data, err := client.Raw(ctx, api.Target{Endpoint: conn.Endpoint, APIKey: conn.APIKey, Timeout: conn.Timeout}, method, path)
	if err != nil {
		return Result{Meta: connectionMeta(conn)}, err
	}
	return Result{Data: data, Meta: connectionMeta(conn)}, nil
}

func (s *Service) Identity(ctx context.Context, kind string, options CheckOptions) (Result, error) {
	file, err := s.load()
	if err != nil {
		return Result{}, err
	}
	conn, err := s.resolve(ctx, file, config.Options{
		Context: options.Context, Endpoint: options.Endpoint, Timeout: options.Timeout,
		ContextSet: options.ContextSet, EndpointSet: options.EndpointSet,
		TimeoutSet: options.TimeoutSet, APIKeyStdin: options.APIKeyStdin,
	})
	if err != nil {
		if kind == "capabilities" {
			return Result{Data: unknownCapabilities(), Meta: connectionMeta(conn)}, err
		}
		return Result{Meta: connectionMeta(conn)}, err
	}
	identity, err := s.identity(ctx, conn)
	if err != nil {
		if kind == "capabilities" {
			return Result{Data: unknownCapabilities(), Meta: connectionMeta(conn)}, err
		}
		return Result{Meta: connectionMeta(conn)}, err
	}
	data := identityView(identity)
	if err := workspaceBindingError(conn, identity); err != nil {
		if kind == "capabilities" {
			data["capabilities"] = "unknown"
		}
		return Result{Data: data, Meta: connectionMeta(conn)}, err
	}
	if kind == "capabilities" {
		capabilities, err := s.capabilities(ctx, conn)
		if err != nil {
			data["capabilities"] = "unknown"
			return Result{Data: data, Meta: connectionMeta(conn)}, err
		}
		data["schema_version"] = capabilities.SchemaVersion
		data["operations"] = capabilityStatuses(capabilities)
	}
	return Result{Data: data, Meta: connectionMeta(conn)}, nil
}

func (s *Service) BotList(ctx context.Context, options CheckOptions) (Result, error) {
	conn, identity, err := s.resourceConnection(ctx, options)
	if err != nil {
		return Result{Meta: resourceMeta(conn, identity)}, err
	}
	bots, err := (api.Client{Transport: s.deps.Transport}).Bots(ctx, api.Target{
		Endpoint: conn.Endpoint, APIKey: conn.APIKey, Timeout: conn.Timeout,
	})
	if err != nil {
		return Result{Meta: resourceMeta(conn, identity)}, err
	}
	return Result{Data: map[string]any{"bots": bots}, Meta: resourceMeta(conn, identity)}, nil
}

func (s *Service) BotGet(ctx context.Context, identifier string, options CheckOptions) (Result, error) {
	conn, identity, err := s.resourceConnection(ctx, options)
	if err != nil {
		return Result{Meta: resourceMeta(conn, identity)}, err
	}
	bot, err := (api.Client{Transport: s.deps.Transport}).Bot(ctx, api.Target{
		Endpoint: conn.Endpoint, APIKey: conn.APIKey, Timeout: conn.Timeout,
	}, identifier)
	if err != nil {
		return Result{Meta: resourceMeta(conn, identity)}, err
	}
	return Result{Data: map[string]any{"bot": bot}, Meta: resourceMeta(conn, identity)}, nil
}

func (s *Service) PipelineList(ctx context.Context, options CheckOptions) (Result, error) {
	conn, identity, err := s.resourceConnection(ctx, options)
	if err != nil {
		return Result{Meta: resourceMeta(conn, identity)}, err
	}
	pipelines, err := (api.Client{Transport: s.deps.Transport}).Pipelines(ctx, api.Target{
		Endpoint: conn.Endpoint, APIKey: conn.APIKey, Timeout: conn.Timeout,
	})
	if err != nil {
		return Result{Meta: resourceMeta(conn, identity)}, err
	}
	return Result{Data: map[string]any{"pipelines": pipelines}, Meta: resourceMeta(conn, identity)}, nil
}

func (s *Service) PipelineGet(ctx context.Context, identifier string, options CheckOptions) (Result, error) {
	conn, identity, err := s.resourceConnection(ctx, options)
	if err != nil {
		return Result{Meta: resourceMeta(conn, identity)}, err
	}
	pipeline, err := (api.Client{Transport: s.deps.Transport}).Pipeline(ctx, api.Target{
		Endpoint: conn.Endpoint, APIKey: conn.APIKey, Timeout: conn.Timeout,
	}, identifier)
	if err != nil {
		return Result{Meta: resourceMeta(conn, identity)}, err
	}
	return Result{Data: map[string]any{"pipeline": pipeline}, Meta: resourceMeta(conn, identity)}, nil
}

// TaskGet 返回异步任务公共状态。
func (s *Service) TaskGet(ctx context.Context, identifier string, options CheckOptions) (Result, error) {
	conn, identity, err := s.resourceConnection(ctx, options)
	if err != nil {
		return Result{Meta: resourceMeta(conn, identity)}, err
	}
	capabilities, err := s.capabilities(ctx, conn)
	if err != nil {
		return Result{Meta: resourceMeta(conn, identity)}, err
	}
	preflight := WritePreflight{Connection: conn, Identity: identity, Capabilities: capabilities}
	if err := requireCapability(preflight, "task.get"); err != nil {
		return Result{Meta: resourceMeta(conn, identity)}, err
	}
	task, err := (api.Client{Transport: s.deps.Transport}).Task(ctx, api.Target{
		Endpoint: conn.Endpoint, APIKey: conn.APIKey, Timeout: conn.Timeout,
	}, identifier)
	if err != nil {
		return Result{Meta: resourceMeta(conn, identity)}, err
	}
	return Result{Data: map[string]any{"task": task}, Meta: resourceMeta(conn, identity)}, nil
}

// TaskWait 等待异步任务进入终态，只在本地停止轮询。
func (s *Service) TaskWait(ctx context.Context, identifier string, wait WaitOptions, options CheckOptions) (Result, error) {
	if ctx != nil && ctx.Err() != nil {
		return Result{Data: map[string]any{"task_id": identifier, "server_cancelled": false}}, taskStatusError("wait_cancelled", "等待已取消，服务端任务未被取消")
	}
	conn, identity, err := s.resourceConnection(ctx, options)
	if err != nil {
		return Result{Meta: resourceMeta(conn, identity)}, err
	}
	capabilities, err := s.capabilities(ctx, conn)
	if err != nil {
		return Result{Meta: resourceMeta(conn, identity)}, err
	}
	preflight := WritePreflight{Connection: conn, Identity: identity, Capabilities: capabilities}
	if err := requireCapability(preflight, "task.get"); err != nil {
		return Result{Meta: resourceMeta(conn, identity)}, err
	}
	task, waitErr := waitForTask(ctx, api.Client{Transport: s.deps.Transport}, api.Target{
		Endpoint: conn.Endpoint, APIKey: conn.APIKey, Timeout: conn.Timeout,
	}, identifier, wait)
	data := map[string]any{"task_id": identifier, "server_cancelled": false}
	if task != nil {
		data["task"] = task
	}
	if waitErr != nil {
		return Result{Data: data, Meta: resourceMeta(conn, identity)}, waitErr
	}
	return Result{Data: data, Meta: resourceMeta(conn, identity)}, nil
}

// KnowledgeBaseIngest 上传文件并提交知识库入库任务。
func (s *Service) KnowledgeBaseIngest(
	ctx context.Context,
	identifier string,
	filename string,
	parserPluginID string,
	dryRun bool,
	wait WaitOptions,
	options CheckOptions,
) (Result, error) {
	if strings.TrimSpace(identifier) == "" {
		return Result{}, result.New("input", "知识库 ID 不能为空")
	}
	if strings.TrimSpace(filename) == "" {
		return Result{}, result.New("input", "上传文件不能为空")
	}
	fileInfo, err := os.Stat(filename)
	if err != nil {
		return Result{}, result.New("input", "无法读取上传文件")
	}
	if !fileInfo.Mode().IsRegular() {
		return Result{}, result.New("input", "上传路径必须是普通文件")
	}
	preflight, err := s.WritePreflight(ctx, "knowledge_base.file.store", options)
	if err != nil {
		return Result{Meta: preflight.Meta()}, err
	}
	if err := requireCapability(preflight, "file.document.upload"); err != nil {
		return Result{Meta: preflight.Meta()}, err
	}
	if err := requireCapability(preflight, "knowledge_base.get"); err != nil {
		return Result{Meta: preflight.Meta()}, err
	}
	if wait.Wait && !dryRun {
		if err := requireCapability(preflight, "task.get"); err != nil {
			return Result{Meta: preflight.Meta()}, err
		}
	}
	client, target := s.writeClient(preflight)
	base, err := client.KnowledgeBase(ctx, target, identifier)
	if err != nil {
		return Result{Meta: preflight.Meta()}, err
	}
	if dryRun {
		return Result{Data: map[string]any{
			"operation":               "knowledge_base.ingest",
			"dry_run":                 true,
			"server_write":            false,
			"preconditions_confirmed": true,
			"business_validation":     "not_run",
			"knowledge_base":          base,
			"file": map[string]any{
				"name":       filepath.Base(filename),
				"size_bytes": fileInfo.Size(),
			},
		}, Meta: preflight.Meta()}, nil
	}
	file, err := os.Open(filename)
	if err != nil {
		return Result{Data: map[string]any{"step": "validated", "knowledge_base": base}, Meta: preflight.Meta()}, result.New("input", "无法读取上传文件")
	}
	defer file.Close()
	fileID, err := client.UploadDocument(ctx, target, filepath.Base(filename), file)
	if err != nil {
		data := map[string]any{"step": "validated", "knowledge_base": base}
		if result.AsError(err).Type == "result_unknown" {
			data["step"] = "upload_unknown"
		}
		return Result{Data: data, Meta: preflight.Meta()}, err
	}
	data := map[string]any{
		"step":                "uploaded",
		"knowledge_base":      base,
		"file_id":             fileID,
		"server_write":        true,
		"submitted":           false,
		"preflight_confirmed": true,
	}
	taskID, err := client.KnowledgeBaseStoreFile(ctx, target, identifier, fileID, parserPluginID)
	if err != nil {
		failure := result.AsError(err)
		if failure.Type == "result_unknown" {
			data["step"] = "store_unknown"
			data["submitted"] = "unknown"
			data["submission_result"] = "unknown"
			return Result{Data: data, Meta: preflight.Meta()}, err
		}
		if failure.HTTPStatus >= http.StatusBadRequest && failure.HTTPStatus < http.StatusInternalServerError {
			data["step"] = "store_failed"
			data["submitted"] = false
			return Result{Data: data, Meta: preflight.Meta()}, partialIngestError(err)
		}
		data["step"] = "store_unknown"
		data["submitted"] = "unknown"
		data["submission_result"] = "unknown"
		return Result{Data: data, Meta: preflight.Meta()}, err
	}
	data["step"] = "submitted"
	data["submitted"] = true
	data["task_id"] = taskID
	if !wait.Wait {
		return Result{Data: data, Meta: preflight.Meta()}, nil
	}
	task, waitErr := waitForTask(ctx, client, target, taskID, wait)
	data["task"] = task
	if waitErr != nil {
		return Result{Data: data, Meta: preflight.Meta()}, waitErr
	}
	data["step"] = "completed"
	return Result{Data: data, Meta: preflight.Meta()}, nil
}

func waitForTask(ctx context.Context, client api.Client, target api.Target, identifier string, options WaitOptions) (*api.Task, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	interval := options.PollInterval
	if interval <= 0 {
		interval = time.Second
	}
	timeout := options.WaitTimeout
	if timeout <= 0 {
		timeout = 5 * time.Minute
	}
	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var lastTask *api.Task
	for {
		task, err := client.Task(waitCtx, target, identifier)
		if err != nil {
			if waitCtx.Err() != nil {
				if ctx.Err() != nil {
					return lastTask, taskStatusError("wait_cancelled", "等待已取消，服务端任务未被取消")
				}
				return lastTask, taskStatusError("wait_timeout", "等待异步任务超时")
			}
			failure := result.AsError(err)
			if failure.Kind == "not_found" {
				failure.Type = "task_not_found"
				failure.Message = "异步任务记录不存在或已被清理"
			}
			return nil, err
		}
		taskValue := &task
		lastTask = taskValue
		switch task.Status {
		case "succeeded":
			return taskValue, nil
		case "failed":
			return taskValue, taskStatusError("task_failed", "异步任务执行失败")
		case "cancelled":
			return taskValue, taskStatusError("task_cancelled", "异步任务已取消")
		}
		timer := time.NewTimer(interval)
		select {
		case <-waitCtx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			if ctx.Err() != nil {
				return taskValue, taskStatusError("wait_cancelled", "等待已取消，服务端任务未被取消")
			}
			return taskValue, taskStatusError("wait_timeout", "等待异步任务超时")
		case <-timer.C:
		}
	}
}

func taskStatusError(kind, message string) *result.Error {
	err := result.New("server", message)
	err.Type = kind
	return err
}

func partialIngestError(cause error) *result.Error {
	failure := result.AsError(cause)
	err := result.New(failure.Kind, "文件已上传，但知识库入库提交失败")
	err.Type = "partial_write"
	err.HTTPStatus = failure.HTTPStatus
	err.ServerCode = failure.ServerCode
	err.RequestID = failure.RequestID
	return err
}

func (s *Service) BotCreate(ctx context.Context, body map[string]any, dryRun bool, options CheckOptions) (Result, error) {
	if _, exists := body["uuid"]; exists {
		return Result{}, result.New("input", "创建 Bot 时不能指定 uuid")
	}
	preflight, err := s.WritePreflight(ctx, "bot.create", options)
	if err != nil {
		return Result{Meta: preflight.Meta()}, err
	}
	if dryRun {
		return writePlan(preflight, "bot.create", ""), nil
	}
	client, target := s.writeClient(preflight)
	written, err := client.BotCreate(ctx, target, body)
	if err != nil {
		return Result{Meta: preflight.Meta()}, err
	}
	bot, err := client.Bot(ctx, target, written.UUID)
	data := writeResultData("bot.create", written.UUID)
	if err != nil {
		return Result{Data: data, Meta: preflight.Meta()}, readbackError(err)
	}
	data["verified"] = true
	data["bot"] = bot
	return Result{Data: data, Meta: preflight.Meta()}, nil
}

func (s *Service) BotUpdate(ctx context.Context, uuid string, body map[string]any, dryRun bool, options CheckOptions) (Result, error) {
	if err := api.ValidateResourceID(uuid); err != nil {
		return Result{}, err
	}
	if err := validateBodyUUID(body, uuid); err != nil {
		return Result{}, err
	}
	preflight, err := s.WritePreflight(ctx, "bot.update", options)
	if err != nil {
		return Result{Meta: preflight.Meta()}, err
	}
	if dryRun {
		return writePlan(preflight, "bot.update", uuid), nil
	}
	client, target := s.writeClient(preflight)
	if _, err := client.BotUpdate(ctx, target, uuid, withoutUUID(body)); err != nil {
		return Result{Meta: preflight.Meta()}, err
	}
	bot, err := client.Bot(ctx, target, uuid)
	data := writeResultData("bot.update", uuid)
	if err != nil {
		return Result{Data: data, Meta: preflight.Meta()}, readbackError(err)
	}
	data["verified"] = true
	data["bot"] = bot
	return Result{Data: data, Meta: preflight.Meta()}, nil
}

func (s *Service) BotDelete(ctx context.Context, uuid string, confirmed, dryRun bool, options CheckOptions) (Result, error) {
	if err := api.ValidateResourceID(uuid); err != nil {
		return Result{}, err
	}
	if !dryRun && !confirmed {
		return Result{}, result.New("input", "删除 Bot 需要 --yes")
	}
	preflight, err := s.WritePreflight(ctx, "bot.delete", options)
	if err != nil {
		return Result{Meta: preflight.Meta()}, err
	}
	if dryRun {
		return writePlan(preflight, "bot.delete", uuid), nil
	}
	client, target := s.writeClient(preflight)
	if _, err := client.BotDelete(ctx, target, uuid); err != nil {
		return Result{Meta: preflight.Meta()}, err
	}
	data := writeResultData("bot.delete", uuid)
	_, err = client.Bot(ctx, target, uuid)
	if err != nil && result.AsError(err).Kind == "not_found" {
		data["verified"] = true
		return Result{Data: data, Meta: preflight.Meta()}, nil
	} else if err != nil {
		return Result{Data: data, Meta: preflight.Meta()}, readbackError(err)
	}
	return Result{Data: data, Meta: preflight.Meta()}, verificationError("删除后资源仍可读取")
}

func (s *Service) PipelineApply(ctx context.Context, body map[string]any, dryRun bool, options CheckOptions) (Result, error) {
	if body == nil {
		return Result{}, result.New("input", "请求体必须是 JSON object")
	}
	uuid, updating, err := optionalBodyUUID(body)
	if err != nil {
		return Result{}, err
	}
	operation := "pipeline.create"
	if updating {
		if err := api.ValidateResourceID(uuid); err != nil {
			return Result{}, err
		}
		operation = "pipeline.update"
	}
	preflight, err := s.WritePreflight(ctx, operation, options)
	if err != nil {
		return Result{Meta: preflight.Meta()}, err
	}
	if !updating {
		if err := requireCapability(preflight, "pipeline.update"); err != nil {
			return Result{Meta: preflight.Meta()}, err
		}
	}
	if dryRun {
		return writePlan(preflight, "pipeline.apply", uuid), nil
	}
	client, target := s.writeClient(preflight)
	payload := withoutUUID(body)
	if !updating {
		created, createErr := client.PipelineCreate(ctx, target, payload)
		if createErr != nil {
			return Result{Meta: preflight.Meta()}, createErr
		}
		uuid = created.UUID
		if _, updateErr := client.PipelineUpdate(ctx, target, uuid, payload); updateErr != nil {
			data := writeResultData("pipeline.apply", uuid)
			data["phase"] = "created"
			return Result{Data: data, Meta: preflight.Meta()}, partialWriteError(updateErr)
		}
	} else if _, err := client.PipelineUpdate(ctx, target, uuid, payload); err != nil {
		return Result{Meta: preflight.Meta()}, err
	}
	pipeline, err := client.Pipeline(ctx, target, uuid)
	data := writeResultData("pipeline.apply", uuid)
	if err != nil {
		return Result{Data: data, Meta: preflight.Meta()}, readbackError(err)
	}
	data["verified"] = true
	data["pipeline"] = pipeline
	return Result{Data: data, Meta: preflight.Meta()}, nil
}

func (s *Service) PipelineCopy(ctx context.Context, uuid string, dryRun bool, options CheckOptions) (Result, error) {
	if err := api.ValidateResourceID(uuid); err != nil {
		return Result{}, err
	}
	preflight, err := s.WritePreflight(ctx, "pipeline.copy", options)
	if err != nil {
		return Result{Meta: preflight.Meta()}, err
	}
	if dryRun {
		return writePlan(preflight, "pipeline.copy", uuid), nil
	}
	client, target := s.writeClient(preflight)
	written, err := client.PipelineCopy(ctx, target, uuid)
	if err != nil {
		return Result{Meta: preflight.Meta()}, err
	}
	pipeline, err := client.Pipeline(ctx, target, written.UUID)
	data := writeResultData("pipeline.copy", written.UUID)
	if err != nil {
		return Result{Data: data, Meta: preflight.Meta()}, readbackError(err)
	}
	data["verified"] = true
	data["pipeline"] = pipeline
	return Result{Data: data, Meta: preflight.Meta()}, nil
}

func (s *Service) PipelineDelete(ctx context.Context, uuid string, confirmed, dryRun bool, options CheckOptions) (Result, error) {
	if err := api.ValidateResourceID(uuid); err != nil {
		return Result{}, err
	}
	if !dryRun && !confirmed {
		return Result{}, result.New("input", "删除 Pipeline 需要 --yes")
	}
	preflight, err := s.WritePreflight(ctx, "pipeline.delete", options)
	if err != nil {
		return Result{Meta: preflight.Meta()}, err
	}
	if dryRun {
		return writePlan(preflight, "pipeline.delete", uuid), nil
	}
	client, target := s.writeClient(preflight)
	if _, err := client.PipelineDelete(ctx, target, uuid); err != nil {
		return Result{Meta: preflight.Meta()}, err
	}
	data := writeResultData("pipeline.delete", uuid)
	_, err = client.Pipeline(ctx, target, uuid)
	if err != nil && result.AsError(err).Kind == "not_found" {
		data["verified"] = true
		return Result{Data: data, Meta: preflight.Meta()}, nil
	} else if err != nil {
		return Result{Data: data, Meta: preflight.Meta()}, readbackError(err)
	}
	return Result{Data: data, Meta: preflight.Meta()}, verificationError("删除后资源仍可读取")
}

func (s *Service) writeClient(preflight WritePreflight) (api.Client, api.Target) {
	return api.Client{Transport: s.deps.Transport}, api.Target{
		Endpoint: preflight.Connection.Endpoint,
		APIKey:   preflight.Connection.APIKey,
		Timeout:  preflight.Connection.Timeout,
	}
}

func writePlan(preflight WritePreflight, operation, uuid string) Result {
	data := map[string]any{
		"operation":               operation,
		"dry_run":                 true,
		"server_write":            false,
		"preconditions_confirmed": true,
	}
	if uuid != "" {
		data["uuid"] = uuid
	}
	return Result{Data: data, Meta: preflight.Meta()}
}

func writeResultData(operation, uuid string) map[string]any {
	return map[string]any{"operation": operation, "uuid": uuid, "verified": false}
}

func requireCapability(preflight WritePreflight, operation string) error {
	supported, known := preflight.Capabilities.Operations[operation]
	if !known {
		return result.New("precondition", "服务端未明确支持该写操作")
	}
	if !supported {
		return result.New("precondition", "服务端明确不支持该写操作")
	}
	return nil
}

func optionalBodyUUID(body map[string]any) (string, bool, error) {
	value, exists := body["uuid"]
	if !exists {
		return "", false, nil
	}
	uuid, ok := value.(string)
	if !ok || strings.TrimSpace(uuid) == "" {
		return "", false, result.New("input", "uuid 必须是非空字符串")
	}
	return uuid, true, nil
}

func validateBodyUUID(body map[string]any, expected string) error {
	uuid, exists, err := optionalBodyUUID(body)
	if err != nil {
		return err
	}
	if exists && uuid != expected {
		return result.New("input", "请求体 uuid 与命令参数不一致")
	}
	return nil
}

func withoutUUID(body map[string]any) map[string]any {
	copy := make(map[string]any, len(body))
	for key, value := range body {
		if key != "uuid" {
			copy[key] = value
		}
	}
	return copy
}

func readbackError(cause error) *result.Error {
	failure := result.AsError(cause)
	err := result.New(failure.Kind, "写操作已返回成功，但回读验证失败")
	err.Type = "verification_failed"
	err.HTTPStatus = failure.HTTPStatus
	err.ServerCode = failure.ServerCode
	err.RequestID = failure.RequestID
	return err
}

func verificationError(message string) *result.Error {
	err := result.New("server", message)
	err.Type = "verification_failed"
	return err
}

func partialWriteError(cause error) *result.Error {
	failure := result.AsError(cause)
	err := result.New(failure.Kind, "Pipeline 已创建，但完整配置未应用")
	err.Type = "partial_write"
	err.HTTPStatus = failure.HTTPStatus
	err.ServerCode = failure.ServerCode
	err.RequestID = failure.RequestID
	return err
}

func (s *Service) resourceConnection(ctx context.Context, options CheckOptions) (config.Connection, api.Context, error) {
	file, err := s.load()
	if err != nil {
		return config.Connection{}, api.Context{}, err
	}
	conn, err := s.resolve(ctx, file, config.Options{
		Context: options.Context, Endpoint: options.Endpoint, Timeout: options.Timeout,
		ContextSet: options.ContextSet, EndpointSet: options.EndpointSet,
		TimeoutSet: options.TimeoutSet, APIKeyStdin: options.APIKeyStdin,
	})
	if err != nil {
		return conn, api.Context{}, err
	}
	identity, err := s.identity(ctx, conn)
	if err != nil {
		return conn, api.Context{}, err
	}
	if err := workspaceBindingError(conn, identity); err != nil {
		return conn, identity, err
	}
	if !hasPermission(identity, "resource.view") {
		return conn, identity, result.New("permission", "当前 API Key 缺少 resource.view 权限")
	}
	return conn, identity, nil
}

// WritePreflight 只允许已保存且绑定 Workspace 的命名 context 执行写操作。
func (s *Service) WritePreflight(ctx context.Context, operation string, options CheckOptions) (WritePreflight, error) {
	if options.EndpointSet {
		return WritePreflight{}, result.New("precondition", "写操作不允许使用临时 endpoint")
	}
	file, err := s.load()
	if err != nil {
		return WritePreflight{}, err
	}
	configOptions := config.Options{
		Context: options.Context, Endpoint: options.Endpoint, Timeout: options.Timeout,
		ContextSet: options.ContextSet, EndpointSet: options.EndpointSet, TimeoutSet: options.TimeoutSet,
		APIKeyStdin: options.APIKeyStdin,
	}
	contextName, err := writeContextName(file, options, s.deps.LookupEnv)
	if err != nil {
		return WritePreflight{}, err
	}
	saved, ok := file.Contexts[contextName]
	if !ok {
		return WritePreflight{}, result.New("precondition", "写操作要求使用已保存的 context")
	}
	if strings.TrimSpace(saved.ExpectedWorkspaceUUID) == "" {
		return WritePreflight{}, result.New("precondition", "写操作要求 context 已绑定 Workspace")
	}
	conn, err := config.Resolve(file, configOptions, s.deps.LookupEnv)
	if err != nil {
		return WritePreflight{Connection: conn}, err
	}
	if conn.Temporary {
		return WritePreflight{Connection: conn}, result.New("precondition", "写操作不允许使用临时连接")
	}
	conn, err = s.withCredential(ctx, conn, configOptions)
	if err != nil {
		return WritePreflight{Connection: conn}, err
	}
	identity, err := s.identity(ctx, conn)
	if err != nil {
		return WritePreflight{Connection: conn}, err
	}
	if err := workspaceBindingError(conn, identity); err != nil {
		return WritePreflight{Connection: conn, Identity: identity}, err
	}
	if !hasPermission(identity, "resource.manage") {
		return WritePreflight{Connection: conn, Identity: identity}, result.New("permission", "当前 API Key 缺少 resource.manage 权限")
	}
	if !hasPermission(identity, "resource.view") {
		return WritePreflight{Connection: conn, Identity: identity}, result.New("permission", "当前 API Key 缺少写后回读所需的 resource.view 权限")
	}
	capabilities, err := s.capabilities(ctx, conn)
	if err != nil {
		return WritePreflight{Connection: conn, Identity: identity}, err
	}
	preflight := WritePreflight{Connection: conn, Identity: identity, Capabilities: capabilities}
	if err := requireCapability(preflight, operation); err != nil {
		return preflight, err
	}
	if readback := readbackCapability(operation); readback != "" {
		if err := requireCapability(preflight, readback); err != nil {
			return preflight, result.New("precondition", "服务端未明确支持写后回读")
		}
	}
	return preflight, nil
}

func readbackCapability(operation string) string {
	switch {
	case strings.HasPrefix(operation, "bot."):
		return "bot.get"
	case strings.HasPrefix(operation, "pipeline."):
		return "pipeline.get"
	default:
		return ""
	}
}

func writeContextName(file config.File, options CheckOptions, lookup func(string) (string, bool)) (string, error) {
	if options.ContextSet {
		if strings.TrimSpace(options.Context) == "" {
			return "", result.New("input", "context 不能为空")
		}
		return options.Context, nil
	}
	if lookup != nil {
		if value, present := lookup("LANGBOT_CONTEXT"); present {
			if strings.TrimSpace(value) == "" {
				return "", result.New("input", "LANGBOT_CONTEXT 为空")
			}
			return value, nil
		}
	}
	if strings.TrimSpace(file.CurrentContext) == "" {
		return "", result.New("precondition", "写操作要求使用已保存的 context")
	}
	return file.CurrentContext, nil
}

func hasPermission(identity api.Context, required string) bool {
	for _, permission := range identity.Permissions {
		if permission == required {
			return true
		}
	}
	return false
}

func resourceMeta(conn config.Connection, identity api.Context) map[string]any {
	meta := connectionMeta(conn)
	if identity.InstanceUUID != "" && identity.WorkspaceUUID != "" {
		meta["instance_uuid"] = identity.InstanceUUID
		meta["workspace_uuid"] = identity.WorkspaceUUID
	}
	return meta
}

func localContextResult(name string, saved config.Context, current string) Result {
	return Result{Data: contextView(name, saved, name == current)}
}

func (s *Service) connection(ctx context.Context, options config.Options) (config.Connection, error) {
	file, err := s.load()
	if err != nil {
		return config.Connection{}, err
	}
	return s.resolve(ctx, file, options)
}

func (s *Service) resolve(ctx context.Context, file config.File, options config.Options) (config.Connection, error) {
	conn, err := config.Resolve(file, options, s.deps.LookupEnv)
	if err != nil {
		return conn, err
	}
	return s.withCredential(ctx, conn, options)
}

func (s *Service) withCredential(ctx context.Context, conn config.Connection, options config.Options) (config.Connection, error) {
	credentialInput := s.deps.In
	if options.APIKeyStdin {
		value, readErr := readCredential(ctx, credentialInput)
		if readErr != nil {
			return conn, readErr
		}
		credentialInput = strings.NewReader(value)
	}
	conn, err := conn.WithCredential(options, s.deps.LookupEnv, credentialInput)
	if err != nil {
		return conn, err
	}
	return conn, nil
}

func readCredential(ctx context.Context, reader io.Reader) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	type readResult struct {
		value string
		err   error
	}
	completed := make(chan readResult, 1)
	go func() {
		data, err := io.ReadAll(io.LimitReader(reader, (64<<10)+1))
		completed <- readResult{value: string(data), err: err}
	}()
	select {
	case <-ctx.Done():
		return "", result.New("network", "请求已取消")
	case read := <-completed:
		if read.err != nil {
			return "", result.New("auth", "读取 API Key 失败")
		}
		return read.value, nil
	}
}

func (s *Service) info(ctx context.Context, conn config.Connection) (api.Info, error) {
	client := api.Client{Transport: s.deps.Transport}
	return client.Info(ctx, api.Target{Endpoint: conn.Endpoint, APIKey: conn.APIKey, Timeout: conn.Timeout})
}

func (s *Service) identity(ctx context.Context, conn config.Connection) (api.Context, error) {
	client := api.Client{Transport: s.deps.Transport}
	return client.Context(ctx, api.Target{Endpoint: conn.Endpoint, APIKey: conn.APIKey, Timeout: conn.Timeout})
}

func (s *Service) capabilities(ctx context.Context, conn config.Connection) (api.Capabilities, error) {
	client := api.Client{Transport: s.deps.Transport}
	return client.Capabilities(ctx, api.Target{Endpoint: conn.Endpoint, APIKey: conn.APIKey, Timeout: conn.Timeout})
}

func unknownCapabilities() map[string]any {
	return map[string]any{
		"capabilities": "unknown",
	}
}

func capabilityStatuses(capabilities api.Capabilities) map[string]string {
	statuses := make(map[string]string, len(api.CapabilityOperationIDs()))
	for _, operationID := range api.CapabilityOperationIDs() {
		supported, ok := capabilities.Operations[operationID]
		status := "unknown"
		if ok {
			status = "unsupported"
			if supported {
				status = "supported"
			}
		}
		statuses[operationID] = status
	}
	return statuses
}

func validateBatchTimeout(options CheckOptions, lookup func(string) (string, bool)) error {
	value := options.Timeout
	present := options.TimeoutSet
	if !present && lookup != nil {
		value, present = lookup("LANGBOT_TIMEOUT")
	}
	if !present {
		return nil
	}
	if strings.TrimSpace(value) == "" {
		return result.New("input", "timeout 不能为空")
	}
	duration, err := time.ParseDuration(value)
	if err != nil || duration <= 0 {
		return result.New("input", "timeout 无效")
	}
	return nil
}

func (s *Service) checkAll(ctx context.Context, options CheckOptions) (Result, error) {
	if err := validateBatchTimeout(options, s.deps.LookupEnv); err != nil {
		return Result{}, err
	}
	file, err := s.load()
	if err != nil {
		return Result{}, err
	}
	names := sortedContextNames(file)
	if len(names) == 0 {
		return Result{Data: map[string]any{"checks": []any{}}}, nil
	}
	type item struct {
		name string
		data map[string]any
		err  error
	}
	results := make([]item, len(names))
	jobs := make(chan int)
	var wg sync.WaitGroup
	workers := 4
	if len(names) < workers {
		workers = len(names)
	}
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for index := range jobs {
				name := names[index]
				conn, resolveErr := s.resolve(ctx, file, config.Options{
					Context: name, ContextSet: true, Timeout: options.Timeout, TimeoutSet: options.TimeoutSet,
				})
				if resolveErr != nil {
					data := checkData(conn, api.Info{}, resolveErr, api.Context{}, nil)
					data["identity"] = "unknown"
					if data["context"] == "" {
						data["context"] = name
					}
					if data["endpoint"] == "" {
						data["endpoint"] = file.Contexts[name].Endpoint
					}
					results[index] = item{name: name, data: data, err: resolveErr}
					continue
				}
				info, infoErr := s.info(ctx, conn)
				var identity api.Context
				var identityErr error
				if infoErr == nil || hasHTTPResponse(infoErr) {
					identity, identityErr = s.identity(ctx, conn)
				}
				data := checkData(conn, info, infoErr, identity, identityErr)
				callErr := firstError(infoErr, identityErr)
				if callErr == nil {
					callErr = workspaceBindingError(conn, identity)
					if callErr != nil {
						data["error"] = result.AsError(callErr)
					}
				}
				results[index] = item{name: name, data: data, err: callErr}
			}
		}()
	}
	for i := range names {
		jobs <- i
	}
	close(jobs)
	wg.Wait()
	checks := make([]map[string]any, len(results))
	var firstErr error
	for i, current := range results {
		checks[i] = current.data
		if firstErr == nil && current.err != nil {
			firstErr = current.err
		}
	}
	data := map[string]any{"checks": checks}
	if firstErr != nil {
		return Result{Data: data}, firstErr
	}
	return Result{Data: data}, nil
}

func (s *Service) load() (config.File, error) {
	path, err := s.deps.DefaultConfigPath()
	if err != nil {
		return config.File{}, result.New("input", "无法确定配置文件路径")
	}
	file, err := (config.Store{Path: path}).Load()
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return config.File{Version: 1, Contexts: map[string]config.Context{}}, nil
		}
		return config.File{}, err
	}
	return file, nil
}

func (s *Service) update(fn func(*config.File) error) error {
	path, err := s.deps.DefaultConfigPath()
	if err != nil {
		return result.New("input", "无法确定配置文件路径")
	}
	return (config.Store{Path: path}).Update(s.ctx, fn)
}

func checkData(conn config.Connection, info api.Info, infoErr error, identity api.Context, identityErr error) map[string]any {
	diagnosticOK := infoErr == nil && identityErr == nil
	reachable := infoErr == nil || hasHTTPResponse(infoErr) || hasHTTPResponse(identityErr)
	data := map[string]any{
		"context":       conn.Context,
		"endpoint":      conn.Endpoint,
		"reachable":     reachable,
		"diagnostic_ok": diagnosticOK,
		"identity":      "unknown",
		"capabilities":  "unknown",
	}
	if infoErr == nil {
		data["server_version"] = info.Version
		data["server_edition"] = info.Edition
	}
	if identityErr == nil && identity.InstanceUUID != "" && identity.WorkspaceUUID != "" && identity.APIKeyID != "" {
		data["identity"] = identityView(identity)
	}
	if infoErr != nil {
		data["error"] = result.AsError(infoErr)
	} else if identityErr != nil {
		data["error"] = result.AsError(identityErr)
	}
	return data
}

func identityView(identity api.Context) map[string]any {
	return map[string]any{
		"instance_uuid":  identity.InstanceUUID,
		"workspace_uuid": identity.WorkspaceUUID,
		"api_key_id":     identity.APIKeyID,
		"permissions":    identity.Permissions,
	}
}

func workspaceBindingError(conn config.Connection, identity api.Context) error {
	if conn.ExpectedWorkspaceUUID == "" || conn.ExpectedWorkspaceUUID == identity.WorkspaceUUID {
		return nil
	}
	err := result.New("precondition", "服务端 Workspace 与 context 预期不一致")
	err.Type = "target_mismatch"
	return err
}

func hasHTTPResponse(err error) bool {
	return err != nil && result.AsError(err).HTTPStatus != 0
}

func firstError(first, second error) error {
	if first != nil {
		return first
	}
	return second
}

func connectionMeta(conn config.Connection) map[string]any {
	meta := map[string]any{"endpoint": conn.Endpoint}
	if conn.Context != "" {
		meta["context"] = conn.Context
	}
	if conn.CredentialSource != "" {
		meta["credential_source"] = conn.CredentialSource
	}
	if conn.Temporary {
		meta["temporary"] = true
	}
	return meta
}

func connectionView(conn config.Connection) map[string]any {
	view := connectionMeta(conn)
	view["expected_workspace_uuid"] = conn.ExpectedWorkspaceUUID
	view["timeout"] = conn.Timeout.String()
	return view
}

func contextView(name string, ctx config.Context, current bool) map[string]any {
	view := map[string]any{
		"name":                    name,
		"endpoint":                ctx.Endpoint,
		"expected_workspace_uuid": ctx.ExpectedWorkspaceUUID,
		"current":                 current,
	}
	if ctx.Timeout != "" {
		view["timeout"] = ctx.Timeout
	}
	if ctx.Credential == nil {
		view["credential_source"] = "none"
	} else {
		view["credential_source"] = ctx.Credential.Type + ":" + ctx.Credential.Name
	}
	return view
}

func sortedContextNames(file config.File) []string {
	names := make([]string, 0, len(file.Contexts))
	for name := range file.Contexts {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func validateContextName(name string) error {
	name = strings.TrimSpace(name)
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, "/\\") {
		return result.New("input", "context 名称无效")
	}
	return nil
}
