package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/langbot-app/langbot-cli/internal/api"
	"github.com/langbot-app/langbot-cli/internal/config"
	"github.com/langbot-app/langbot-cli/internal/deploy"
	endpointutil "github.com/langbot-app/langbot-cli/internal/endpoint"
	"github.com/langbot-app/langbot-cli/internal/i18n"
	"github.com/langbot-app/langbot-cli/internal/result"
)

const maxExtensionUploadBytes int64 = 10 << 20

type Dependencies struct {
	In                io.Reader
	LookupEnv         func(string) (string, bool)
	DefaultConfigPath func() (string, error)
	Transport         http.RoundTripper
	Version           string
	Commit            string
	BuildDate         string
	LocalRunner       deploy.Runner
	LocalHTTP         *http.Client
	LocalDir          string
	LocalLocatorPath  string
	LatestRelease     func(context.Context) (string, error)
	LocalLogOut       io.Writer
	LocalLogErr       io.Writer
}

type Result struct {
	Data any
	Meta map[string]any
}

// WaitOptions 控制异步任务的本地轮询，不会向服务端发送取消请求。
type WaitOptions struct {
	ConfirmPermissions bool
	UseOperations      bool
	Wait               bool
	PollInterval       time.Duration
	WaitTimeout        time.Duration
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

func (s *Service) ContextList(discover bool) (Result, error) {
	file, err := s.load()
	if err != nil {
		return Result{}, err
	}
	names := sortedContextNames(file)
	deployments, localErr := s.localDeployments()
	items := make([]map[string]any, 0, len(names))
	for _, name := range names {
		item := contextView(name, file.Contexts[name], name == file.CurrentContext)
		item["credential_state"] = s.credentialState(file, name)
		for _, deployment := range deployments {
			if deployment.Binding != "registered" || !sameLocalEndpoint(deployment.Endpoint, file.Contexts[name].Endpoint) {
				continue
			}
			if item["deployment_id"] != nil {
				delete(item, "deployment_id")
				delete(item, "project")
				item["binding"] = "ambiguous"
				break
			}
			item["deployment_id"], item["project"], item["binding"] = deployment.ID, deployment.Project, "registered"
		}
		items = append(items, item)
	}
	data := map[string]any{
		"current_context": file.CurrentContext,
		"contexts":        items,
		"deployments":     deployments,
	}
	if localErr != nil {
		data["local_error"] = result.AsError(localErr)
	}
	if discover {
		discovered, err := s.DeploymentDiscover(s.ctx)
		if err != nil {
			return Result{Data: data}, err
		}
		local := discovered.Data.(map[string]any)
		data["discovered_deployments"], data["skipped_projects"] = local["deployments"], local["skipped_projects"]
	}
	return Result{Data: data}, nil
}

func (s *Service) ContextShow(name string, overrides ...CheckOptions) (Result, error) {
	file, err := s.load()
	if err != nil {
		return Result{}, err
	}
	if name == "" {
		options := CheckOptions{}
		if len(overrides) > 0 {
			options = overrides[0]
		}
		name, err = writeContextName(file, options, s.deps.LookupEnv)
		if err != nil {
			return Result{}, result.New("input", i18n.T("No environment selected; run lbctl context list and lbctl context use <name>"))
		}
	}
	ctx, ok := file.Contexts[name]
	if !ok {
		return Result{}, result.New("not_found", i18n.T("context does not exist"))
	}
	saved := contextView(name, ctx, name == file.CurrentContext)
	effective := map[string]any{}
	options := CheckOptions{}
	if len(overrides) != 0 {
		options = overrides[0]
	}
	if options.ContextSet && options.Context != name {
		return Result{}, result.New("input", i18n.T("Positional context does not match --context"))
	}
	conn, resolveErr := config.Resolve(file, config.Options{
		Context: name, Endpoint: options.Endpoint, Timeout: options.Timeout,
		ContextSet: true, EndpointSet: options.EndpointSet, TimeoutSet: options.TimeoutSet,
		APIKeyStdin: options.APIKeyStdin,
	}, s.deps.LookupEnv)
	if resolveErr == nil {
		effective = connectionView(conn)
		effective["credential_state"] = s.connectionCredentialState(conn)
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
		return Result{}, result.New("input", i18n.T("New context requires endpoint"))
	}
	var saved config.Context
	var current string
	err := s.update(func(file *config.File) error {
		if _, ok := file.Contexts[name]; ok {
			return result.New("precondition", i18n.T("context already exists"))
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
		return Result{}, result.New("input", i18n.T("update requires at least one change"))
	}
	var saved config.Context
	var current string
	err := s.update(func(file *config.File) error {
		ctx, ok := file.Contexts[name]
		if !ok {
			return result.New("not_found", i18n.T("context does not exist"))
		}
		if endpointSet {
			if strings.TrimSpace(endpoint) == "" {
				return result.New("input", i18n.T("endpoint must not be empty"))
			}
			oldEndpoint, oldErr := endpointutil.Normalize(ctx.Endpoint)
			newEndpoint, newErr := endpointutil.Normalize(endpoint)
			if oldErr != nil || newErr != nil {
				return result.New("input", i18n.T("endpoint must be a valid HTTP(S) service root URL"))
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
				return result.New("input", i18n.T("Cannot combine --api-key-env and --clear-credential"))
			}
			if strings.TrimSpace(apiKeyEnv) == "" {
				return result.New("input", i18n.T("api-key-env must not be empty"))
			}
			ctx.Credential = &config.Credential{Type: "env", Name: apiKeyEnv}
		}
		if clearBinding {
			ctx.ExpectedWorkspaceUUID = ""
		}
		if expectedSet {
			if clearBinding {
				return result.New("input", i18n.T("Cannot combine --expect-workspace and --clear-binding"))
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
			return result.New("not_found", i18n.T("context does not exist"))
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
			return result.New("not_found", i18n.T("context does not exist"))
		}
		if file.CurrentContext == name && !confirmed {
			return result.New("input", i18n.T("Deleting the current context requires --yes"))
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
	Region      string
	LocalName   string
	LocalDir    string
	Human       bool
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
			return Result{}, result.New("input", i18n.T("context check --all does not accept single-target overrides"))
		}
		if _, ok := s.deps.LookupEnv("LANGBOT_ENDPOINT"); ok {
			return Result{}, result.New("input", i18n.T("context check --all does not accept LANGBOT_ENDPOINT"))
		}
		if _, ok := s.deps.LookupEnv("LANGBOT_API_KEY"); ok {
			return Result{}, result.New("input", i18n.T("context check --all does not accept LANGBOT_API_KEY"))
		}
		if _, ok := s.deps.LookupEnv("LANGBOT_CONTEXT"); ok {
			return Result{}, result.New("input", i18n.T("context check --all does not accept LANGBOT_CONTEXT"))
		}
		return s.checkAll(ctx, options)
	}
	if options.Name != "" && options.ContextSet && options.Name != options.Context {
		return Result{}, result.New("input", i18n.T("Positional context does not match --context"))
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

func (s *Service) localDiagnostics(dir string) (deploy.Diagnostics, error) {
	resolved, err := s.resolveLocalDir(dir)
	if err != nil {
		return deploy.Diagnostics{}, result.New("input", i18n.T("Invalid deployment directory"))
	}
	runner := s.deps.LocalRunner
	if runner == nil {
		runner = deploy.DockerRunner{}
	}
	return deploy.Diagnostics{Dir: resolved, Store: deploy.Store{Dir: resolved}, Runner: runner, HTTP: s.deps.LocalHTTP}, nil
}

func (s *Service) resolveLocalDir(dir string) (string, error) {
	if strings.TrimSpace(dir) == "" && strings.TrimSpace(s.deps.LocalDir) != "" {
		return deploy.ResolveDir(s.deps.LocalDir)
	}
	return (deploy.Locator{Path: s.deps.LocalLocatorPath}).Resolve(dir)
}

func mapLocalError(err error) error {
	if err == nil {
		return nil
	}
	var problem deploy.Problem
	if errors.As(err, &problem) {
		return result.New(problem.Kind, problem.Msg)
	}
	return result.New("server", i18n.T("Local deployment check failed"))
}

func (s *Service) RawInfo(ctx context.Context, options CheckOptions, methodAndPath ...string) (Result, error) {
	method, path := "GET", "/api/v1/system/info"
	if len(methodAndPath) == 2 {
		method, path = methodAndPath[0], methodAndPath[1]
	}
	if method != "GET" || path != "/api/v1/system/info" {
		return Result{}, result.New("incompatible", i18n.T("Only the confirmed system/info endpoint is readable"))
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
	data, err := client.Raw(ctx, api.Target{Endpoint: conn.Endpoint, APIKey: conn.APIKey, UserToken: conn.UserToken, Timeout: conn.Timeout}, method, path)
	if err != nil {
		return Result{Meta: connectionMeta(conn)}, err
	}
	return Result{Data: data, Meta: connectionMeta(conn)}, nil
}

// APIRequest 执行受 operation registry 约束的 HTTP 请求。
func (s *Service) APIRequest(ctx context.Context, method, path string, body map[string]any, options CheckOptions) (Result, error) {
	operation, ok := api.ResolveOperation(method, path)
	if !ok {
		return Result{}, result.New("incompatible", i18n.T("Request path or method is not registered; generic API calls are not allowed"))
	}
	conn, identity, err := s.connectionWithIdentity(ctx, options)
	meta := resourceMeta(conn, identity)
	if err != nil {
		return Result{Meta: meta}, err
	}
	if operation.Permission != "" && !hasPermission(identity, operation.Permission) {
		return Result{Meta: meta}, result.New("permission", i18n.T("Current API Key lacks ")+operation.Permission+i18n.T(" permission"))
	}
	capabilities, err := s.capabilities(ctx, conn)
	if err != nil {
		return Result{Meta: meta}, err
	}
	if err := requireCapability(WritePreflight{Capabilities: capabilities}, operation.ID); err != nil {
		return Result{Meta: meta}, err
	}
	data, err := (api.Client{Transport: s.deps.Transport}).APIRequest(ctx, api.Target{
		Endpoint: conn.Endpoint, APIKey: conn.APIKey, UserToken: conn.UserToken, Timeout: conn.Timeout,
	}, operation, body)
	if err != nil {
		return Result{Meta: meta}, err
	}
	return Result{Data: data, Meta: meta}, nil
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
		Endpoint: conn.Endpoint, APIKey: conn.APIKey, UserToken: conn.UserToken, Timeout: conn.Timeout,
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
		Endpoint: conn.Endpoint, APIKey: conn.APIKey, UserToken: conn.UserToken, Timeout: conn.Timeout,
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
		Endpoint: conn.Endpoint, APIKey: conn.APIKey, UserToken: conn.UserToken, Timeout: conn.Timeout,
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
		Endpoint: conn.Endpoint, APIKey: conn.APIKey, UserToken: conn.UserToken, Timeout: conn.Timeout,
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
		Endpoint: conn.Endpoint, APIKey: conn.APIKey, UserToken: conn.UserToken, Timeout: conn.Timeout,
	}, identifier)
	if err != nil {
		return Result{Meta: resourceMeta(conn, identity)}, err
	}
	return Result{Data: map[string]any{"task": task}, Meta: resourceMeta(conn, identity)}, nil
}

// TaskList 返回当前 API Key 可见的任务列表。
func (s *Service) TaskList(ctx context.Context, taskType, kind string, options CheckOptions) (Result, error) {
	conn, identity, err := s.resourceConnection(ctx, options)
	if err != nil {
		return Result{Meta: resourceMeta(conn, identity)}, err
	}
	capabilities, err := s.capabilities(ctx, conn)
	if err != nil {
		return Result{Meta: resourceMeta(conn, identity)}, err
	}
	preflight := WritePreflight{Connection: conn, Identity: identity, Capabilities: capabilities}
	if err := requireCapability(preflight, "task.list"); err != nil {
		return Result{Meta: resourceMeta(conn, identity)}, err
	}
	tasks, err := (api.Client{Transport: s.deps.Transport}).Tasks(ctx, api.Target{
		Endpoint: conn.Endpoint, APIKey: conn.APIKey, UserToken: conn.UserToken, Timeout: conn.Timeout,
	}, api.TaskListFilters{Type: taskType, Kind: kind})
	if err != nil {
		return Result{Meta: resourceMeta(conn, identity)}, err
	}
	return Result{Data: map[string]any{"tasks": tasks}, Meta: resourceMeta(conn, identity)}, nil
}

// TaskWait 等待异步任务进入终态，只在本地停止轮询。
func (s *Service) TaskWait(ctx context.Context, identifier string, wait WaitOptions, options CheckOptions) (Result, error) {
	if ctx != nil && ctx.Err() != nil {
		return Result{Data: map[string]any{"task_id": identifier, "server_cancelled": false}}, taskStatusError("wait_cancelled", i18n.T("Wait cancelled; server task was not cancelled"))
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
		Endpoint: conn.Endpoint, APIKey: conn.APIKey, UserToken: conn.UserToken, Timeout: conn.Timeout,
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
		return Result{}, result.New("input", i18n.T("Knowledge-base ID must not be empty"))
	}
	if strings.TrimSpace(filename) == "" {
		return Result{}, result.New("input", i18n.T("Upload file must not be empty"))
	}
	fileInfo, err := os.Stat(filename)
	if err != nil {
		return Result{}, result.New("input", i18n.T("Unable to read upload file"))
	}
	if !fileInfo.Mode().IsRegular() {
		return Result{}, result.New("input", i18n.T("Upload path must be a regular file"))
	}
	preflight, base, err := s.prepareKnowledgeBaseIngest(ctx, identifier, wait.Wait && !dryRun, options)
	if err != nil {
		return Result{Meta: preflight.Meta()}, err
	}
	return s.ingestKnowledgeBaseFile(ctx, identifier, filename, fileInfo.Size(), parserPluginID, dryRun, wait, preflight, base)
}

func (s *Service) ingestKnowledgeBaseFile(ctx context.Context, identifier, filename string, size int64, parserPluginID string, dryRun bool, wait WaitOptions, preflight WritePreflight, base map[string]any) (Result, error) {
	client, target := s.writeClient(preflight)
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
				"size_bytes": size,
			},
		}, Meta: preflight.Meta()}, nil
	}
	file, err := os.Open(filename)
	if err != nil {
		return Result{Data: map[string]any{"step": "validated", "knowledge_base": base}, Meta: preflight.Meta()}, result.New("input", i18n.T("Unable to read upload file"))
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

func (s *Service) KnowledgeBaseList(ctx context.Context, options CheckOptions) (Result, error) {
	preflight, err := s.readPreflight(ctx, "knowledge_base.list", "resource.view", options)
	if err != nil {
		return Result{Meta: preflight.Meta()}, err
	}
	bases, err := (api.Client{Transport: s.deps.Transport}).KnowledgeBases(ctx, readTarget(preflight))
	if err != nil {
		return Result{Meta: preflight.Meta()}, err
	}
	return Result{Data: map[string]any{"bases": bases}, Meta: preflight.Meta()}, nil
}

func (s *Service) KnowledgeBaseGet(ctx context.Context, identifier string, options CheckOptions) (Result, error) {
	if err := api.ValidateResourceID(identifier); err != nil {
		return Result{}, err
	}
	preflight, err := s.readPreflight(ctx, "knowledge_base.get", "resource.view", options)
	if err != nil {
		return Result{Meta: preflight.Meta()}, err
	}
	base, err := (api.Client{Transport: s.deps.Transport}).KnowledgeBase(ctx, readTarget(preflight), identifier)
	if err != nil {
		return Result{Meta: preflight.Meta()}, err
	}
	return Result{Data: map[string]any{"base": base}, Meta: preflight.Meta()}, nil
}

func (s *Service) KnowledgeBaseCreate(ctx context.Context, body map[string]any, dryRun bool, options CheckOptions) (Result, error) {
	if body == nil {
		return Result{}, result.New("input", i18n.T("Request body must be a JSON object"))
	}
	if err := validateBodyFields(body, "name", "description", "knowledge_engine_plugin_id", "creation_settings", "retrieval_settings"); err != nil {
		return Result{}, err
	}
	engineID, ok := body["knowledge_engine_plugin_id"].(string)
	if !ok || strings.TrimSpace(engineID) == "" {
		return Result{}, result.New("input", i18n.T("Knowledge-base creation requires knowledge_engine_plugin_id"))
	}
	preflight, err := s.WritePreflight(ctx, "knowledge_base.create", options)
	if err != nil {
		return Result{Meta: preflight.Meta()}, err
	}
	if dryRun {
		return writePlan(preflight, "knowledge_base.create", ""), nil
	}
	client, target := s.writeClient(preflight)
	written, err := client.KnowledgeBaseCreate(ctx, target, body)
	if err != nil {
		return Result{Meta: preflight.Meta()}, err
	}
	base, err := client.KnowledgeBase(ctx, target, written.UUID)
	data := writeResultData("knowledge_base.create", written.UUID)
	if err != nil {
		return Result{Data: data, Meta: preflight.Meta()}, readbackError(err)
	}
	data["verified"] = true
	data["base"] = base
	return Result{Data: data, Meta: preflight.Meta()}, nil
}

func (s *Service) KnowledgeBaseUpdate(ctx context.Context, identifier string, body map[string]any, dryRun bool, options CheckOptions) (Result, error) {
	if err := api.ValidateResourceID(identifier); err != nil {
		return Result{}, err
	}
	if body == nil {
		return Result{}, result.New("input", i18n.T("Request body must be a JSON object"))
	}
	if err := validateBodyFields(body, "uuid", "name", "description", "retrieval_settings"); err != nil {
		return Result{}, err
	}
	if err := validateBodyUUID(body, identifier); err != nil {
		return Result{}, err
	}
	if len(withoutUUID(body)) == 0 {
		return Result{}, result.New("input", i18n.T("Knowledge-base update contains no editable fields"))
	}
	preflight, err := s.WritePreflight(ctx, "knowledge_base.update", options)
	if err != nil {
		return Result{Meta: preflight.Meta()}, err
	}
	if dryRun {
		return writePlan(preflight, "knowledge_base.update", identifier), nil
	}
	client, target := s.writeClient(preflight)
	if _, err := client.KnowledgeBaseUpdate(ctx, target, identifier, withoutUUID(body)); err != nil {
		return Result{Meta: preflight.Meta()}, err
	}
	base, err := client.KnowledgeBase(ctx, target, identifier)
	data := writeResultData("knowledge_base.update", identifier)
	if err != nil {
		return Result{Data: data, Meta: preflight.Meta()}, readbackError(err)
	}
	data["verified"] = true
	data["base"] = base
	return Result{Data: data, Meta: preflight.Meta()}, nil
}

func (s *Service) KnowledgeBaseDelete(ctx context.Context, identifier string, confirmed, dryRun bool, options CheckOptions) (Result, error) {
	if err := api.ValidateResourceID(identifier); err != nil {
		return Result{}, err
	}
	if !dryRun && !confirmed {
		return Result{}, result.New("input", i18n.T("Deleting a knowledge base requires --yes"))
	}
	preflight, err := s.WritePreflight(ctx, "knowledge_base.delete", options)
	if err != nil {
		return Result{Meta: preflight.Meta()}, err
	}
	if dryRun {
		return writePlan(preflight, "knowledge_base.delete", identifier), nil
	}
	client, target := s.writeClient(preflight)
	if _, err := client.KnowledgeBaseDelete(ctx, target, identifier); err != nil {
		return Result{Meta: preflight.Meta()}, err
	}
	data := writeResultData("knowledge_base.delete", identifier)
	_, err = client.KnowledgeBase(ctx, target, identifier)
	if err != nil && result.AsError(err).Kind == "not_found" {
		data["verified"] = true
		return Result{Data: data, Meta: preflight.Meta()}, nil
	}
	if err != nil {
		return Result{Data: data, Meta: preflight.Meta()}, readbackError(err)
	}
	return Result{Data: data, Meta: preflight.Meta()}, verificationError(i18n.T("Knowledge base is still readable after deletion"))
}

func (s *Service) KnowledgeBaseFileList(ctx context.Context, identifier string, options CheckOptions) (Result, error) {
	if err := api.ValidateResourceID(identifier); err != nil {
		return Result{}, err
	}
	preflight, err := s.readPreflight(ctx, "knowledge_base.file.list", "resource.view", options)
	if err != nil {
		return Result{Meta: preflight.Meta()}, err
	}
	files, err := (api.Client{Transport: s.deps.Transport}).KnowledgeBaseFiles(ctx, readTarget(preflight), identifier)
	if err != nil {
		return Result{Meta: preflight.Meta()}, err
	}
	return Result{Data: map[string]any{"files": files}, Meta: preflight.Meta()}, nil
}

func (s *Service) KnowledgeBaseFileDelete(ctx context.Context, identifier, fileID string, confirmed, dryRun bool, options CheckOptions) (Result, error) {
	if err := api.ValidateResourceID(identifier); err != nil {
		return Result{}, err
	}
	if err := api.ValidateResourceID(fileID); err != nil {
		return Result{}, err
	}
	if !dryRun && !confirmed {
		return Result{}, result.New("input", i18n.T("Deleting a knowledge-base file requires --yes"))
	}
	preflight, err := s.WritePreflight(ctx, "knowledge_base.file.delete", options)
	if err != nil {
		return Result{Meta: preflight.Meta()}, err
	}
	if dryRun {
		return writePlan(preflight, "knowledge_base.file.delete", fileID), nil
	}
	client, target := s.writeClient(preflight)
	if err := client.KnowledgeBaseFileDelete(ctx, target, identifier, fileID); err != nil {
		return Result{Meta: preflight.Meta()}, err
	}
	files, err := client.KnowledgeBaseFiles(ctx, target, identifier)
	data := writeResultData("knowledge_base.file.delete", fileID)
	if err != nil {
		return Result{Data: data, Meta: preflight.Meta()}, readbackError(err)
	}
	for _, file := range files {
		if fileID == fileIdentifier(file) {
			return Result{Data: data, Meta: preflight.Meta()}, verificationError(i18n.T("Knowledge-base file is still readable after deletion"))
		}
	}
	data["verified"] = true
	return Result{Data: data, Meta: preflight.Meta()}, nil
}

func (s *Service) KnowledgeBaseRetrieve(ctx context.Context, identifier string, body map[string]any, options CheckOptions) (Result, error) {
	if err := api.ValidateResourceID(identifier); err != nil {
		return Result{}, err
	}
	if body == nil {
		return Result{}, result.New("input", i18n.T("Request body must be a JSON object"))
	}
	query, ok := body["query"].(string)
	if !ok || strings.TrimSpace(query) == "" {
		return Result{}, result.New("input", i18n.T("Knowledge-base search requires a non-empty query"))
	}
	preflight, err := s.readPreflight(ctx, "knowledge_base.retrieve", "resource.view", options)
	if err != nil {
		return Result{Meta: preflight.Meta()}, err
	}
	data, err := (api.Client{Transport: s.deps.Transport}).KnowledgeBaseRetrieve(ctx, readTarget(preflight), identifier, body)
	if err != nil {
		return Result{Meta: preflight.Meta()}, err
	}
	return Result{Data: data, Meta: preflight.Meta()}, nil
}

func (s *Service) MCPServerList(ctx context.Context, options CheckOptions) (Result, error) {
	preflight, err := s.readPreflight(ctx, "mcp_server.list", "resource.view", options)
	if err != nil {
		return Result{Meta: preflight.Meta()}, err
	}
	servers, err := (api.Client{Transport: s.deps.Transport}).MCPServers(ctx, readTarget(preflight))
	if err != nil {
		return Result{Meta: preflight.Meta()}, err
	}
	return Result{Data: map[string]any{"servers": servers}, Meta: preflight.Meta()}, nil
}

func (s *Service) MCPServerGet(ctx context.Context, name string, options CheckOptions) (Result, error) {
	if err := api.ValidateMCPServerName(name); err != nil {
		return Result{}, err
	}
	preflight, err := s.readPreflight(ctx, "mcp_server.get", "resource.view", options)
	if err != nil {
		return Result{Meta: preflight.Meta()}, err
	}
	server, err := (api.Client{Transport: s.deps.Transport}).MCPServerGet(ctx, readTarget(preflight), name)
	if err != nil {
		return Result{Meta: preflight.Meta()}, err
	}
	return Result{Data: map[string]any{"server": server}, Meta: preflight.Meta()}, nil
}

func (s *Service) MCPServerCreate(ctx context.Context, body map[string]any, dryRun bool, options CheckOptions) (Result, error) {
	if body == nil {
		return Result{}, result.New("input", i18n.T("Request body must be a JSON object"))
	}
	if err := validateBodyFields(body, "name", "enable", "mode", "extra_args", "readme"); err != nil {
		return Result{}, err
	}
	name, ok := body["name"].(string)
	name = strings.TrimSpace(name)
	if !ok || name == "" {
		return Result{}, result.New("input", i18n.T("MCP Server request body must include name"))
	}
	if err := api.ValidateMCPServerName(name); err != nil {
		return Result{}, err
	}
	mode, ok := body["mode"].(string)
	if !ok || strings.TrimSpace(mode) == "" {
		return Result{}, result.New("input", i18n.T("MCP Server creation requires mode"))
	}
	preflight, err := s.WritePreflight(ctx, "mcp_server.create", options)
	if err != nil {
		return Result{Meta: preflight.Meta()}, err
	}
	if dryRun {
		plan := writePlan(preflight, "mcp_server.create", "")
		plan.Data.(map[string]any)["server_name"] = name
		return plan, nil
	}
	client, target := s.writeClient(preflight)
	written, err := client.MCPServerCreate(ctx, target, body)
	if err != nil {
		return Result{Meta: preflight.Meta()}, err
	}
	server, err := client.MCPServerGet(ctx, target, name)
	data := writeResultData("mcp_server.create", written.UUID)
	if err != nil {
		return Result{Data: data, Meta: preflight.Meta()}, readbackError(err)
	}
	if server["uuid"] != written.UUID {
		return Result{Data: data, Meta: preflight.Meta()}, verificationError(i18n.T("MCP Server UUID differs after creation"))
	}
	data["server_name"] = name
	data["verified"] = true
	data["server"] = server
	return Result{Data: data, Meta: preflight.Meta()}, nil
}

func (s *Service) MCPServerUpdate(ctx context.Context, name string, body map[string]any, dryRun bool, options CheckOptions) (Result, error) {
	if err := api.ValidateMCPServerName(name); err != nil {
		return Result{}, err
	}
	if body == nil {
		return Result{}, result.New("input", i18n.T("Request body must be a JSON object"))
	}
	if err := validateBodyFields(body, "name", "enable", "mode", "extra_args", "readme"); err != nil {
		return Result{}, err
	}
	if len(body) == 0 {
		return Result{}, result.New("input", i18n.T("MCP Server update contains no editable fields"))
	}
	newName := name
	if value, exists := body["name"]; exists {
		value, ok := value.(string)
		if !ok || strings.TrimSpace(value) == "" {
			return Result{}, result.New("input", i18n.T("MCP Server name must be a non-empty string"))
		}
		newName = strings.TrimSpace(value)
		if err := api.ValidateMCPServerName(newName); err != nil {
			return Result{}, err
		}
	}
	preflight, err := s.WritePreflight(ctx, "mcp_server.update", options)
	if err != nil {
		return Result{Meta: preflight.Meta()}, err
	}
	if dryRun {
		plan := writePlan(preflight, "mcp_server.update", "")
		plan.Data.(map[string]any)["server_name"] = name
		if newName != name {
			plan.Data.(map[string]any)["new_server_name"] = newName
		}
		return plan, nil
	}
	client, target := s.writeClient(preflight)
	current, err := client.MCPServerGet(ctx, target, name)
	if err != nil {
		return Result{Meta: preflight.Meta()}, err
	}
	if err := client.MCPServerUpdate(ctx, target, name, withoutUUID(body)); err != nil {
		return Result{Meta: preflight.Meta()}, err
	}
	server, err := client.MCPServerGet(ctx, target, newName)
	uuid, _ := current["uuid"].(string)
	data := writeResultData("mcp_server.update", uuid)
	data["server_name"] = newName
	if err != nil {
		return Result{Data: data, Meta: preflight.Meta()}, readbackError(err)
	}
	if server["uuid"] != uuid {
		return Result{Data: data, Meta: preflight.Meta()}, verificationError(i18n.T("MCP Server UUID differs after update"))
	}
	data["verified"] = true
	data["server"] = server
	return Result{Data: data, Meta: preflight.Meta()}, nil
}

func (s *Service) MCPServerDelete(ctx context.Context, name string, confirmed, dryRun bool, options CheckOptions) (Result, error) {
	if err := api.ValidateMCPServerName(name); err != nil {
		return Result{}, err
	}
	if !dryRun && !confirmed {
		return Result{}, result.New("input", i18n.T("Deleting an MCP Server requires --yes"))
	}
	preflight, err := s.WritePreflight(ctx, "mcp_server.delete", options)
	if err != nil {
		return Result{Meta: preflight.Meta()}, err
	}
	if dryRun {
		plan := writePlan(preflight, "mcp_server.delete", "")
		plan.Data.(map[string]any)["server_name"] = name
		return plan, nil
	}
	client, target := s.writeClient(preflight)
	current, err := client.MCPServerGet(ctx, target, name)
	if err != nil {
		return Result{Meta: preflight.Meta()}, err
	}
	if err := client.MCPServerDelete(ctx, target, name); err != nil {
		return Result{Meta: preflight.Meta()}, err
	}
	uuid, _ := current["uuid"].(string)
	data := writeResultData("mcp_server.delete", uuid)
	data["server_name"] = name
	_, err = client.MCPServerGet(ctx, target, name)
	if err != nil && result.AsError(err).Kind == "not_found" {
		data["verified"] = true
		return Result{Data: data, Meta: preflight.Meta()}, nil
	}
	if err != nil {
		return Result{Data: data, Meta: preflight.Meta()}, readbackError(err)
	}
	return Result{Data: data, Meta: preflight.Meta()}, verificationError(i18n.T("MCP Server is still readable after deletion"))
}

func (s *Service) MCPServerResources(ctx context.Context, name string, options CheckOptions) (Result, error) {
	if err := api.ValidateMCPServerName(name); err != nil {
		return Result{}, err
	}
	return s.mcpRead(ctx, name, "mcp_server.resources", "resource.view", func(c api.Client, t api.Target) (any, error) { return c.MCPServerResources(ctx, t, name) }, options)
}

func (s *Service) MCPServerResourceTemplates(ctx context.Context, name string, options CheckOptions) (Result, error) {
	if err := api.ValidateMCPServerName(name); err != nil {
		return Result{}, err
	}
	return s.mcpRead(ctx, name, "mcp_server.resource_templates", "resource.view", func(c api.Client, t api.Target) (any, error) { return c.MCPServerResourceTemplates(ctx, t, name) }, options)
}

func (s *Service) MCPServerResourceRead(ctx context.Context, name string, body map[string]any, options CheckOptions) (Result, error) {
	if err := api.ValidateMCPServerName(name); err != nil {
		return Result{}, err
	}
	if body == nil {
		return Result{}, result.New("input", i18n.T("Request body must be a JSON object"))
	}
	uri, ok := body["uri"].(string)
	if !ok || strings.TrimSpace(uri) == "" {
		return Result{}, result.New("input", i18n.T("MCP resource request must include a non-empty uri"))
	}
	return s.mcpRead(ctx, name, "mcp_server.resource_read", "resource.view", func(c api.Client, t api.Target) (any, error) { return c.MCPServerResourceRead(ctx, t, name, body) }, options)
}

func (s *Service) MCPServerLogs(ctx context.Context, name, level string, limit int, options CheckOptions) (Result, error) {
	if err := api.ValidateMCPServerName(name); err != nil {
		return Result{}, err
	}
	if limit < 1 || limit > 500 {
		return Result{}, result.New("input", i18n.T("MCP log count must be between 1 and 500"))
	}
	return s.mcpRead(ctx, name, "mcp_server.logs", "audit.view", func(c api.Client, t api.Target) (any, error) { return c.MCPServerLogs(ctx, t, name, level, limit) }, options)
}

func (s *Service) mcpRead(ctx context.Context, name, operation, permission string, read func(api.Client, api.Target) (any, error), options CheckOptions) (Result, error) {
	preflight, err := s.readPreflight(ctx, operation, permission, options)
	if err != nil {
		return Result{Meta: preflight.Meta()}, err
	}
	data, err := read(api.Client{Transport: s.deps.Transport}, readTarget(preflight))
	if err != nil {
		return Result{Meta: preflight.Meta()}, err
	}
	return Result{Data: data, Meta: preflight.Meta()}, nil
}

func readTarget(preflight WritePreflight) api.Target {
	return api.Target{Endpoint: preflight.Connection.Endpoint, APIKey: preflight.Connection.APIKey, UserToken: preflight.Connection.UserToken, Timeout: preflight.Connection.Timeout}
}

func fileIdentifier(value any) string {
	if item, ok := value.(map[string]any); ok {
		for _, key := range []string{"id", "file_id", "uuid"} {
			if id, ok := item[key].(string); ok {
				return id
			}
		}
	}
	return ""
}

func pluginConfigValue(value any) (map[string]any, bool) {
	data, ok := value.(map[string]any)
	if !ok {
		return nil, false
	}
	config, ok := data["config"].(map[string]any)
	return config, ok
}

func observableConfigEqual(expected, actual any) bool {
	if value, ok := actual.(string); ok && value == "***" {
		return true
	}
	switch expectedValue := expected.(type) {
	case map[string]any:
		actualValue, ok := actual.(map[string]any)
		if !ok || len(expectedValue) != len(actualValue) {
			return false
		}
		for key, value := range expectedValue {
			actualItem, exists := actualValue[key]
			if !exists || !observableConfigEqual(value, actualItem) {
				return false
			}
		}
		return true
	case []any:
		actualValue, ok := actual.([]any)
		if !ok || len(expectedValue) != len(actualValue) {
			return false
		}
		for index, value := range expectedValue {
			if !observableConfigEqual(value, actualValue[index]) {
				return false
			}
		}
		return true
	default:
		if reflect.DeepEqual(expected, actual) {
			return true
		}
		expectedJSON, expectedErr := json.Marshal(expected)
		actualJSON, actualErr := json.Marshal(actual)
		return expectedErr == nil && actualErr == nil && bytes.Equal(expectedJSON, actualJSON)
	}
}

func observableFieldsEqual(expected, actual map[string]any, fields ...string) bool {
	for _, field := range fields {
		value, exists := expected[field]
		if !exists {
			continue
		}
		actualValue, exists := actual[field]
		if !exists || !observableConfigEqual(value, actualValue) {
			return false
		}
	}
	return true
}

func (s *Service) PluginList(ctx context.Context, options CheckOptions) (Result, error) {
	preflight, err := s.readPreflight(ctx, "plugin.list", "resource.view", options)
	if err != nil {
		return Result{Meta: preflight.Meta()}, err
	}
	plugins, err := (api.Client{Transport: s.deps.Transport}).Plugins(ctx, readTarget(preflight))
	if err != nil {
		return Result{Meta: preflight.Meta()}, err
	}
	return Result{Data: map[string]any{"plugins": plugins}, Meta: preflight.Meta()}, nil
}

func (s *Service) PluginGet(ctx context.Context, author, name string, options CheckOptions) (Result, error) {
	if err := api.ValidateResourceID(author); err != nil {
		return Result{}, err
	}
	if err := api.ValidateResourceID(name); err != nil {
		return Result{}, err
	}
	preflight, err := s.readPreflight(ctx, "plugin.get", "resource.view", options)
	if err != nil {
		return Result{Meta: preflight.Meta()}, err
	}
	plugin, err := (api.Client{Transport: s.deps.Transport}).PluginGet(ctx, readTarget(preflight), author, name)
	if err != nil {
		return Result{Meta: preflight.Meta()}, err
	}
	return Result{Data: map[string]any{"plugin": plugin}, Meta: preflight.Meta()}, nil
}

func (s *Service) PluginConfigGet(ctx context.Context, author, name string, options CheckOptions) (Result, error) {
	if err := api.ValidateResourceID(author); err != nil {
		return Result{}, err
	}
	if err := api.ValidateResourceID(name); err != nil {
		return Result{}, err
	}
	preflight, err := s.readPreflight(ctx, "plugin.config.get", "resource.view", options)
	if err != nil {
		return Result{Meta: preflight.Meta()}, err
	}
	config, err := (api.Client{Transport: s.deps.Transport}).PluginConfig(ctx, readTarget(preflight), author, name)
	if err != nil {
		return Result{Meta: preflight.Meta()}, err
	}
	return Result{Data: config, Meta: preflight.Meta()}, nil
}

func (s *Service) PluginLogs(ctx context.Context, author, name, level string, limit int, options CheckOptions) (Result, error) {
	if err := api.ValidateResourceID(author); err != nil {
		return Result{}, err
	}
	if err := api.ValidateResourceID(name); err != nil {
		return Result{}, err
	}
	if limit <= 0 || limit > 500 {
		return Result{}, result.New("input", i18n.T("limit must be between 1 and 500"))
	}
	preflight, err := s.readPreflight(ctx, "plugin.logs", "audit.view", options)
	if err != nil {
		return Result{Meta: preflight.Meta()}, err
	}
	logs, err := (api.Client{Transport: s.deps.Transport}).PluginLogs(ctx, readTarget(preflight), author, name, level, limit)
	if err != nil {
		return Result{Meta: preflight.Meta()}, err
	}
	return Result{Data: logs, Meta: preflight.Meta()}, nil
}

func (s *Service) PluginConfigUpdate(ctx context.Context, author, name string, body map[string]any, dryRun bool, options CheckOptions) (Result, error) {
	if err := api.ValidateResourceID(author); err != nil {
		return Result{}, err
	}
	if err := api.ValidateResourceID(name); err != nil {
		return Result{}, err
	}
	if body == nil {
		return Result{}, result.New("input", i18n.T("Plugin configuration must be a JSON object"))
	}
	preflight, err := s.WritePreflight(ctx, "plugin.config.update", options)
	if err != nil {
		return Result{Meta: preflight.Meta()}, err
	}
	client, target := s.writeClient(preflight)
	if _, err := client.PluginGet(ctx, target, author, name); err != nil {
		return Result{Meta: preflight.Meta()}, err
	}
	if dryRun {
		plan := writePlan(preflight, "plugin.config.update", "")
		data := plan.Data.(map[string]any)
		data["author"] = author
		data["plugin_name"] = name
		return plan, nil
	}
	if err := client.PluginConfigUpdate(ctx, target, author, name, body); err != nil {
		return Result{Meta: preflight.Meta()}, err
	}
	data := writeResultData("plugin.config.update", "")
	delete(data, "uuid")
	data["author"] = author
	data["plugin_name"] = name
	readbackCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for {
		config, err := client.PluginConfig(readbackCtx, target, author, name)
		if err == nil {
			actual, ok := pluginConfigValue(config)
			if ok && observableConfigEqual(body, actual) {
				data["verified"] = true
				data["config"] = actual
				return Result{Data: data, Meta: preflight.Meta()}, nil
			}
		} else if result.AsError(err).HTTPStatus != http.StatusNotFound {
			return Result{Data: data, Meta: preflight.Meta()}, readbackError(err)
		}
		select {
		case <-readbackCtx.Done():
			if err != nil {
				return Result{Data: data, Meta: preflight.Meta()}, readbackError(err)
			}
			return Result{Data: data, Meta: preflight.Meta()}, verificationError(i18n.T("Plugin configuration differs after update"))
		case <-ticker.C:
		}
	}
}

func (s *Service) PluginDelete(ctx context.Context, author, name string, deleteData, confirmed, dryRun, wait bool, waitOptions WaitOptions, options CheckOptions) (Result, error) {
	if err := api.ValidateResourceID(author); err != nil {
		return Result{}, err
	}
	if err := api.ValidateResourceID(name); err != nil {
		return Result{}, err
	}
	if dryRun && wait {
		return Result{}, result.New("input", i18n.T("dry-run cannot wait for asynchronous tasks"))
	}
	if !dryRun && !confirmed {
		return Result{}, result.New("input", i18n.T("Deleting a Plugin requires --yes"))
	}
	preflight, err := s.WritePreflight(ctx, "plugin.delete", options)
	if err != nil {
		return Result{Meta: preflight.Meta()}, err
	}
	if wait {
		if err := requireCapability(preflight, "task.get"); err != nil {
			return Result{Meta: preflight.Meta()}, err
		}
	}
	client, target := s.writeClient(preflight)
	if _, err := client.PluginGet(ctx, target, author, name); err != nil && result.AsError(err).Kind != "not_found" {
		return Result{Meta: preflight.Meta()}, err
	}
	data := map[string]any{
		"operation": "plugin.delete", "author": author, "plugin_name": name,
		"delete_data": deleteData, "dry_run": dryRun, "server_write": false,
		"preconditions_confirmed": true, "verified": false,
	}
	if dryRun {
		return Result{Data: data, Meta: preflight.Meta()}, nil
	}
	taskID, err := client.PluginDelete(ctx, target, author, name, deleteData)
	if err != nil {
		markAsyncSubmissionFailure(data, err)
		return Result{Data: data, Meta: preflight.Meta()}, err
	}
	finished, err := s.finishTask(ctx, client, target, taskID, wait, waitOptions, data, preflight.Meta())
	if err != nil || !wait {
		return finished, err
	}
	_, err = client.PluginGet(ctx, target, author, name)
	if err != nil && result.AsError(err).Kind == "not_found" {
		data["verified"] = true
		return finished, nil
	}
	if err != nil {
		return finished, readbackError(err)
	}
	return finished, verificationError(i18n.T("Plugin is still readable after the deletion task completed"))
}

func (s *Service) SkillList(ctx context.Context, options CheckOptions) (Result, error) {
	preflight, err := s.readPreflight(ctx, "skill.list", "resource.view", options)
	if err != nil {
		return Result{Meta: preflight.Meta()}, err
	}
	skills, err := (api.Client{Transport: s.deps.Transport}).Skills(ctx, readTarget(preflight))
	if err != nil {
		return Result{Meta: preflight.Meta()}, err
	}
	return Result{Data: map[string]any{"skills": skills}, Meta: preflight.Meta()}, nil
}

func (s *Service) SkillGet(ctx context.Context, name string, options CheckOptions) (Result, error) {
	if err := api.ValidateResourceID(name); err != nil {
		return Result{}, err
	}
	preflight, err := s.readPreflight(ctx, "skill.get", "resource.view", options)
	if err != nil {
		return Result{Meta: preflight.Meta()}, err
	}
	skill, err := (api.Client{Transport: s.deps.Transport}).SkillGet(ctx, readTarget(preflight), name)
	if err != nil {
		return Result{Meta: preflight.Meta()}, err
	}
	return Result{Data: map[string]any{"skill": skill}, Meta: preflight.Meta()}, nil
}

func (s *Service) SkillFiles(ctx context.Context, name, path string, includeHidden bool, options CheckOptions) (Result, error) {
	if err := api.ValidateResourceID(name); err != nil {
		return Result{}, err
	}
	if err := api.ValidateSkillFilePath(path, true); err != nil {
		return Result{}, err
	}
	preflight, err := s.readPreflight(ctx, "skill.files.list", "resource.view", options)
	if err != nil {
		return Result{Meta: preflight.Meta()}, err
	}
	files, err := (api.Client{Transport: s.deps.Transport}).SkillFiles(ctx, readTarget(preflight), name, path, includeHidden)
	if err != nil {
		return Result{Meta: preflight.Meta()}, err
	}
	return Result{Data: files, Meta: preflight.Meta()}, nil
}

func (s *Service) SkillFileRead(ctx context.Context, name, path string, options CheckOptions) (Result, error) {
	if err := api.ValidateResourceID(name); err != nil {
		return Result{}, err
	}
	if err := api.ValidateSkillFilePath(path, false); err != nil {
		return Result{}, err
	}
	preflight, err := s.readPreflight(ctx, "skill.files.read", "resource.view", options)
	if err != nil {
		return Result{Meta: preflight.Meta()}, err
	}
	data, err := (api.Client{Transport: s.deps.Transport}).SkillFileRead(ctx, readTarget(preflight), name, path)
	if err != nil {
		return Result{Meta: preflight.Meta()}, err
	}
	return Result{Data: data, Meta: preflight.Meta()}, nil
}

func (s *Service) SkillPreview(ctx context.Context, name string, options CheckOptions) (Result, error) {
	if err := api.ValidateResourceID(name); err != nil {
		return Result{}, err
	}
	preflight, err := s.readPreflight(ctx, "skill.preview", "resource.view", options)
	if err != nil {
		return Result{Meta: preflight.Meta()}, err
	}
	data, err := (api.Client{Transport: s.deps.Transport}).SkillPreview(ctx, readTarget(preflight), name)
	if err != nil {
		return Result{Meta: preflight.Meta()}, err
	}
	return Result{Data: data, Meta: preflight.Meta()}, nil
}

func (s *Service) SkillCreate(ctx context.Context, body map[string]any, dryRun bool, options CheckOptions) (Result, error) {
	if body == nil {
		return Result{}, result.New("input", i18n.T("Skill request body must be a JSON object"))
	}
	if err := validateBodyFields(body, "name", "display_name", "description", "instructions"); err != nil {
		return Result{}, err
	}
	rawName, ok := body["name"].(string)
	name := strings.TrimSpace(rawName)
	if !ok || name == "" {
		return Result{}, result.New("input", i18n.T("Skill creation requires name"))
	}
	if rawName != name {
		return Result{}, result.New("input", i18n.T("Skill name cannot have leading or trailing whitespace"))
	}
	if err := api.ValidateResourceID(name); err != nil {
		return Result{}, err
	}
	preflight, err := s.WritePreflight(ctx, "skill.create", options)
	if err != nil {
		return Result{Meta: preflight.Meta()}, err
	}
	if dryRun {
		plan := writePlan(preflight, "skill.create", "")
		plan.Data.(map[string]any)["skill_name"] = name
		return plan, nil
	}
	client, target := s.writeClient(preflight)
	written, err := client.SkillCreate(ctx, target, body)
	if err != nil {
		return Result{Meta: preflight.Meta()}, err
	}
	if written["name"] != name {
		return Result{Meta: preflight.Meta()}, verificationError(i18n.T("Skill name in creation response does not match"))
	}
	skill, err := client.SkillGet(ctx, target, name)
	data := writeResultData("skill.create", "")
	delete(data, "uuid")
	data["skill_name"] = name
	if err != nil {
		return Result{Data: data, Meta: preflight.Meta()}, readbackError(err)
	}
	if !observableFieldsEqual(body, skill, "name", "display_name", "description", "instructions") {
		return Result{Data: data, Meta: preflight.Meta()}, verificationError(i18n.T("Skill content differs after creation"))
	}
	data["verified"] = true
	data["skill"] = skill
	return Result{Data: data, Meta: preflight.Meta()}, nil
}

func (s *Service) SkillUpdate(ctx context.Context, name string, body map[string]any, dryRun bool, options CheckOptions) (Result, error) {
	if err := api.ValidateResourceID(name); err != nil {
		return Result{}, err
	}
	if body == nil {
		return Result{}, result.New("input", i18n.T("Skill request body must be a JSON object"))
	}
	if err := validateBodyFields(body, "name", "display_name", "description", "instructions", "base_revision"); err != nil {
		return Result{}, err
	}
	_, hasRevision := body["base_revision"]
	if len(body) == 0 || (len(body) == 1 && hasRevision) {
		return Result{}, result.New("input", i18n.T("Skill update contains no editable fields"))
	}
	if revision, exists := body["base_revision"]; exists {
		if value, ok := revision.(string); !ok || strings.TrimSpace(value) == "" {
			return Result{}, result.New("input", i18n.T("Skill base_revision must be a non-empty string"))
		}
	}
	if value, exists := body["name"]; exists {
		bodyName, ok := value.(string)
		if !ok || bodyName != name {
			return Result{}, result.New("input", i18n.T("Skill updates cannot rename; name must match the command argument"))
		}
	}
	preflight, err := s.WritePreflight(ctx, "skill.update", options)
	if err != nil {
		return Result{Meta: preflight.Meta()}, err
	}
	client, target := s.writeClient(preflight)
	current, err := client.SkillGet(ctx, target, name)
	if err != nil {
		return Result{Meta: preflight.Meta()}, err
	}
	body = maps.Clone(body)
	if revision, ok := current["revision"].(string); !hasRevision && ok && strings.TrimSpace(revision) != "" {
		body["base_revision"] = revision
	}
	if dryRun {
		plan := writePlan(preflight, "skill.update", "")
		plan.Data.(map[string]any)["skill_name"] = name
		return plan, nil
	}
	if _, err := client.SkillUpdate(ctx, target, name, body); err != nil {
		return Result{Meta: preflight.Meta()}, err
	}
	skill, err := client.SkillGet(ctx, target, name)
	data := writeResultData("skill.update", "")
	delete(data, "uuid")
	data["skill_name"] = name
	if err != nil {
		return Result{Data: data, Meta: preflight.Meta()}, readbackError(err)
	}
	if !observableFieldsEqual(body, skill, "name", "display_name", "description", "instructions") {
		return Result{Data: data, Meta: preflight.Meta()}, verificationError(i18n.T("Skill content differs after update"))
	}
	data["verified"] = true
	data["skill"] = skill
	return Result{Data: data, Meta: preflight.Meta()}, nil
}

func (s *Service) SkillDelete(ctx context.Context, name string, confirmed, dryRun bool, options CheckOptions) (Result, error) {
	if err := api.ValidateResourceID(name); err != nil {
		return Result{}, err
	}
	if !dryRun && !confirmed {
		return Result{}, result.New("input", i18n.T("Deleting a Skill requires --yes"))
	}
	preflight, err := s.WritePreflight(ctx, "skill.delete", options)
	if err != nil {
		return Result{Meta: preflight.Meta()}, err
	}
	client, target := s.writeClient(preflight)
	if _, err := client.SkillGet(ctx, target, name); err != nil {
		return Result{Meta: preflight.Meta()}, err
	}
	if dryRun {
		plan := writePlan(preflight, "skill.delete", "")
		plan.Data.(map[string]any)["skill_name"] = name
		return plan, nil
	}
	if err := client.SkillDelete(ctx, target, name); err != nil {
		return Result{Meta: preflight.Meta()}, err
	}
	data := writeResultData("skill.delete", "")
	delete(data, "uuid")
	data["skill_name"] = name
	_, err = client.SkillGet(ctx, target, name)
	if err != nil && result.AsError(err).Kind == "not_found" {
		data["verified"] = true
		return Result{Data: data, Meta: preflight.Meta()}, nil
	}
	if err != nil {
		return Result{Data: data, Meta: preflight.Meta()}, readbackError(err)
	}
	return Result{Data: data, Meta: preflight.Meta()}, verificationError(i18n.T("Skill is still readable after deletion"))
}

func (s *Service) SkillFileWrite(ctx context.Context, name, path, content string, dryRun bool, options CheckOptions, baseRevision ...string) (Result, error) {
	if len(baseRevision) > 1 {
		return Result{}, result.New("input", i18n.T("Skill file write accepts at most one base_revision"))
	}
	if err := api.ValidateResourceID(name); err != nil {
		return Result{}, err
	}
	if err := api.ValidateSkillFilePath(path, false); err != nil {
		return Result{}, err
	}
	preflight, err := s.WritePreflight(ctx, "skill.files.write", options)
	if err != nil {
		return Result{Meta: preflight.Meta()}, err
	}
	client, target := s.writeClient(preflight)
	current, err := client.SkillGet(ctx, target, name)
	if err != nil {
		return Result{Meta: preflight.Meta()}, err
	}
	revision, _ := current["revision"].(string)
	if len(baseRevision) == 1 && strings.TrimSpace(baseRevision[0]) != "" {
		revision = baseRevision[0]
	}
	if dryRun {
		plan := writePlan(preflight, "skill.files.write", "")
		data := plan.Data.(map[string]any)
		data["skill_name"] = name
		data["path"] = path
		return plan, nil
	}
	if _, err := client.SkillFileWrite(ctx, target, name, path, content, revision); err != nil {
		return Result{Meta: preflight.Meta()}, err
	}
	file, matches, err := client.SkillFileMatches(ctx, target, name, path, content)
	data := writeResultData("skill.files.write", "")
	delete(data, "uuid")
	data["skill_name"] = name
	data["path"] = path
	if err != nil {
		return Result{Data: data, Meta: preflight.Meta()}, readbackError(err)
	}
	if !matches {
		return Result{Data: data, Meta: preflight.Meta()}, verificationError(i18n.T("Skill file content differs after write"))
	}
	data["verified"] = true
	data["file"] = file
	return Result{Data: data, Meta: preflight.Meta()}, nil
}

func (s *Service) PluginInstallGitHub(ctx context.Context, body map[string]any, dryRun, wait bool, waitOptions WaitOptions, options CheckOptions) (Result, error) {
	return s.pluginInstall(ctx, "plugin.install.github", body, "", dryRun, wait, waitOptions, options)
}

// PluginInstallMarketplace 安装 Marketplace 插件。
func (s *Service) PluginInstallMarketplace(ctx context.Context, body map[string]any, dryRun, wait bool, waitOptions WaitOptions, options CheckOptions) (Result, error) {
	return s.pluginInstall(ctx, "plugin.install.marketplace", body, "", dryRun, wait, waitOptions, options)
}

// PluginInstallLocal 安装本地插件包。
func (s *Service) PluginInstallLocal(ctx context.Context, filename string, dryRun, wait bool, waitOptions WaitOptions, options CheckOptions) (Result, error) {
	if err := validateRegularFile(filename); err != nil {
		return Result{}, err
	}
	return s.pluginInstall(ctx, "plugin.install.local", nil, filename, dryRun, wait, waitOptions, options)
}

func (s *Service) pluginInstall(ctx context.Context, operation string, body map[string]any, filename string, dryRun, wait bool, waitOptions WaitOptions, options CheckOptions) (Result, error) {
	if waitOptions.ConfirmPermissions && !dryRun {
		wait = true
	}
	if dryRun && wait {
		return Result{}, result.New("input", i18n.T("dry-run cannot wait for asynchronous tasks"))
	}
	preflight, err := s.WritePreflight(ctx, operation, options)
	if err != nil {
		return Result{Meta: preflight.Meta()}, err
	}
	if wait {
		if err := requireCapability(preflight, "task.get"); err != nil {
			return Result{Meta: preflight.Meta()}, err
		}
	}
	data := map[string]any{
		"operation":               operation,
		"dry_run":                 dryRun,
		"server_write":            false,
		"preconditions_confirmed": true,
		"business_validation":     "not_run",
	}
	if dryRun {
		if filename != "" {
			if info, statErr := os.Stat(filename); statErr == nil {
				data["file"] = map[string]any{"name": filepath.Base(filename), "size_bytes": info.Size()}
			}
		}
		return Result{Data: data, Meta: preflight.Meta()}, nil
	}
	client, target := s.writeClient(preflight)
	var pluginAuthor, pluginName string
	if waitOptions.ConfirmPermissions {
		if !preflight.Capabilities.Operations["operation.invoke"] || !preflight.Capabilities.Operations["plugin.permissions.confirm_api_key"] {
			return Result{Data: data, Meta: preflight.Meta()}, result.New("incompatible", i18n.T("Post-install permission confirmation requires Core support for operation.invoke and plugin.permissions.confirm_api_key"))
		}
		if operation == "plugin.install.github" {
			body, err = resolveGitHubPluginAsset(ctx, s.deps.Transport, body)
			if err != nil {
				return Result{Data: data, Meta: preflight.Meta()}, err
			}
		}
		pluginAuthor, pluginName, err = s.pluginInstallIdentity(ctx, operation, body, filename)
		if err != nil {
			return Result{Data: data, Meta: preflight.Meta()}, err
		}
	}
	var taskID string
	if filename != "" {
		file, openErr := os.Open(filename)
		if openErr != nil {
			return Result{Data: data, Meta: preflight.Meta()}, result.New("input", i18n.T("Unable to read plugin package"))
		}
		defer file.Close()
		taskID, err = client.PluginInstallLocal(ctx, target, filepath.Base(filename), file)
	} else if operation == "plugin.install.github" {
		body, err = resolveGitHubPluginAsset(ctx, s.deps.Transport, body)
		if err != nil {
			return Result{Data: data, Meta: preflight.Meta()}, err
		}
		taskID, err = client.PluginInstallGitHub(ctx, target, body)
	} else if waitOptions.UseOperations {
		if !preflight.Capabilities.Operations["operation.invoke"] {
			return Result{Data: data, Meta: preflight.Meta()}, result.New("incompatible", i18n.T("Requires LangBot Core with operation.invoke"))
		}
		var value any
		value, err = client.OperationCall(ctx, target, operation, body)
		if err == nil {
			object, ok := value.(map[string]any)
			if !ok || object["task_id"] == nil {
				err = result.New("incompatible", i18n.T("Installation did not return task_id"))
			} else {
				taskID = fmt.Sprint(object["task_id"])
			}
		}
	} else {
		taskID, err = client.PluginInstallMarketplace(ctx, target, body)
	}
	if err != nil {
		markAsyncSubmissionFailure(data, err)
		return Result{Data: data, Meta: preflight.Meta()}, err
	}
	finished, err := s.finishTask(ctx, client, target, taskID, wait, waitOptions, data, preflight.Meta())
	if err != nil || !waitOptions.ConfirmPermissions {
		return finished, err
	}
	permissions, err := operationSession(preflight, client, target).ConfirmPlugin(ctx, pluginAuthor, pluginName, nil, nil)
	data["permissions"] = permissions
	data["enabled"] = err == nil
	return finished, err
}

// PluginUpgrade 升级已安装插件。
func (s *Service) PluginUpgrade(ctx context.Context, author, name string, dryRun, wait bool, waitOptions WaitOptions, options CheckOptions) (Result, error) {
	if waitOptions.ConfirmPermissions && !dryRun {
		wait = true
	}
	if dryRun && wait {
		return Result{}, result.New("input", i18n.T("dry-run cannot wait for asynchronous tasks"))
	}
	preflight, err := s.WritePreflight(ctx, "plugin.upgrade", options)
	if err != nil {
		return Result{Meta: preflight.Meta()}, err
	}
	if err := requireCapability(preflight, "plugin.get"); err != nil {
		return Result{Meta: preflight.Meta()}, err
	}
	if waitOptions.ConfirmPermissions && preflight.Connection.UserToken == "" {
		if err := requireCapability(preflight, "operation.invoke"); err != nil {
			return Result{Meta: preflight.Meta()}, err
		}
		if err := requireCapability(preflight, "plugin.permissions.confirm_api_key"); err != nil {
			return Result{Meta: preflight.Meta()}, err
		}
	}
	if wait {
		if err := requireCapability(preflight, "task.get"); err != nil {
			return Result{Meta: preflight.Meta()}, err
		}
	}
	client, target := api.Client{Transport: s.deps.Transport}, readTarget(preflight)
	data := map[string]any{
		"operation":               "plugin.upgrade",
		"author":                  author,
		"name":                    name,
		"dry_run":                 dryRun,
		"server_write":            false,
		"preconditions_confirmed": true,
		"business_validation":     "not_run",
	}
	plugin, getErr := client.PluginGet(ctx, target, author, name)
	if getErr != nil {
		return Result{Data: data, Meta: preflight.Meta()}, getErr
	}
	data["plugin"] = plugin
	data["business_validation"] = "target_exists"
	if dryRun {
		return Result{Data: data, Meta: preflight.Meta()}, nil
	}
	taskID, err := client.PluginUpgrade(ctx, target, author, name)
	if err != nil {
		markAsyncSubmissionFailure(data, err)
		return Result{Data: data, Meta: preflight.Meta()}, err
	}
	limit := waitOptions.WaitTimeout
	if limit <= 0 {
		limit = 5 * time.Minute
	}
	deadline := time.Now().Add(limit)
	finished, err := s.finishTask(ctx, client, target, taskID, wait, waitOptions, data, preflight.Meta())
	if !wait || err != nil {
		return finished, err
	}
	ctx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	data["previous_version"] = plugin["version"]
	err = operationSession(preflight, client, target).verifyUpgradedPlugin(ctx, author, name, waitOptions, data)
	return finished, err
}

// SkillInstallGitHub 安装 GitHub Skill；dry-run 使用服务端预览接口。
func (s *Service) SkillInstallGitHub(ctx context.Context, body map[string]any, dryRun bool, options CheckOptions) (Result, error) {
	preflight, err := s.WritePreflight(ctx, "skill.install.github", options)
	if err != nil {
		return Result{Meta: preflight.Meta()}, err
	}
	client, target := s.writeClient(preflight)
	var data map[string]any
	if dryRun {
		data, err = client.SkillPreviewGitHub(ctx, target, body)
	} else {
		data, err = client.SkillInstallGitHub(ctx, target, body)
	}
	if err != nil {
		return Result{Data: skillFailureData("skill.install.github", dryRun, err), Meta: preflight.Meta()}, err
	}
	if data == nil {
		data = map[string]any{}
	}
	data["operation"] = "skill.install.github"
	data["dry_run"] = dryRun
	data["server_write"] = !dryRun
	if dryRun {
		data["business_validation"] = "server_preview"
	}
	return Result{Data: data, Meta: preflight.Meta()}, nil
}

// SkillInstallUpload 安装 ZIP Skill，并保留 source_paths 多值参数。
func (s *Service) SkillInstallUpload(ctx context.Context, filename string, sourcePaths []string, dryRun bool, options CheckOptions) (Result, error) {
	if err := validateRegularFile(filename); err != nil {
		return Result{}, err
	}
	preflight, err := s.WritePreflight(ctx, "skill.install.upload", options)
	if err != nil {
		return Result{Meta: preflight.Meta()}, err
	}
	file, err := os.Open(filename)
	if err != nil {
		return Result{Meta: preflight.Meta()}, result.New("input", i18n.T("Unable to read Skill ZIP package"))
	}
	defer file.Close()
	client, target := s.writeClient(preflight)
	var data map[string]any
	if dryRun {
		data, err = client.SkillPreviewUpload(ctx, target, filepath.Base(filename), file, sourcePaths)
	} else {
		data, err = client.SkillInstallUpload(ctx, target, filepath.Base(filename), file, sourcePaths)
	}
	if err != nil {
		return Result{Data: skillFailureData("skill.install.upload", dryRun, err), Meta: preflight.Meta()}, err
	}
	if data == nil {
		data = map[string]any{}
	}
	data["operation"] = "skill.install.upload"
	data["dry_run"] = dryRun
	data["server_write"] = !dryRun
	if dryRun {
		data["business_validation"] = "server_preview"
	}
	return Result{Data: data, Meta: preflight.Meta()}, nil
}

// MCPServerTest 提交 MCP Server 测试任务。
func (s *Service) MCPServerTest(ctx context.Context, name string, body map[string]any, dryRun, wait bool, waitOptions WaitOptions, options CheckOptions) (Result, error) {
	if dryRun && wait {
		return Result{}, result.New("input", i18n.T("dry-run cannot wait for asynchronous tasks"))
	}
	preflight, err := s.WritePreflight(ctx, "mcp_server.test", options)
	if err != nil {
		return Result{Meta: preflight.Meta()}, err
	}
	if wait {
		if err := requireCapability(preflight, "task.get"); err != nil {
			return Result{Meta: preflight.Meta()}, err
		}
	}
	client, target := s.writeClient(preflight)
	data := map[string]any{
		"operation":               "mcp_server.test",
		"name":                    name,
		"dry_run":                 dryRun,
		"server_write":            false,
		"preconditions_confirmed": true,
		"business_validation":     "not_run",
	}
	if name != "_" {
		if err := requireCapability(preflight, "mcp_server.get"); err != nil {
			return Result{Meta: preflight.Meta()}, err
		}
		server, getErr := client.MCPServerGet(ctx, target, name)
		if getErr != nil {
			return Result{Data: data, Meta: preflight.Meta()}, getErr
		}
		data["server"] = server
		data["business_validation"] = "target_exists"
	}
	if dryRun {
		return Result{Data: data, Meta: preflight.Meta()}, nil
	}
	taskID, err := client.MCPServerTest(ctx, target, name, body)
	if err != nil {
		markAsyncSubmissionFailure(data, err)
		return Result{Data: data, Meta: preflight.Meta()}, err
	}
	return s.finishTask(ctx, client, target, taskID, wait, waitOptions, data, preflight.Meta())
}

func (s *Service) finishTask(ctx context.Context, client api.Client, target api.Target, taskID string, wait bool, waitOptions WaitOptions, data map[string]any, meta map[string]any) (Result, error) {
	data["server_write"] = true
	data["submitted"] = true
	data["task_id"] = taskID
	data["step"] = "submitted"
	if !wait {
		return Result{Data: data, Meta: meta}, nil
	}
	task, waitErr := waitForTask(ctx, client, target, taskID, waitOptions)
	data["task"] = task
	if waitErr != nil {
		return Result{Data: data, Meta: meta}, waitErr
	}
	data["step"] = "completed"
	return Result{Data: data, Meta: meta}, nil
}

func markAsyncSubmissionFailure(data map[string]any, err error) {
	if result.AsError(err).Type == "result_unknown" {
		data["submitted"] = "unknown"
		data["step"] = "submission_unknown"
		return
	}
	data["submitted"] = false
	data["step"] = "submission_failed"
}

func skillFailureData(operation string, dryRun bool, err error) map[string]any {
	data := map[string]any{
		"operation":    operation,
		"dry_run":      dryRun,
		"server_write": false,
	}
	if dryRun {
		data["step"] = "preview_failed"
		data["business_validation"] = "failed"
		return data
	}
	data["server_write"] = "unconfirmed"
	data["step"] = "install_failed"
	if result.AsError(err).Type == "result_unknown" {
		data["server_write"] = "unknown"
		data["step"] = "install_unknown"
	}
	return data
}

func validateRegularFile(filename string) error {
	if strings.TrimSpace(filename) == "" || filename == "-" {
		return result.New("input", i18n.T("A local regular file is required"))
	}
	info, err := os.Stat(filename)
	if err != nil {
		return result.New("input", i18n.T("Unable to read upload file"))
	}
	if !info.Mode().IsRegular() {
		return result.New("input", i18n.T("Upload path must be a regular file"))
	}
	if info.Size() > maxExtensionUploadBytes {
		return result.New("input", i18n.T("Upload file is too large"))
	}
	return nil
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
					return lastTask, taskStatusError("wait_cancelled", i18n.T("Wait cancelled; server task was not cancelled"))
				}
				return lastTask, taskStatusError("wait_timeout", i18n.T("Timed out waiting for asynchronous task"))
			}
			failure := result.AsError(err)
			if failure.Kind == "not_found" {
				failure.Type = "task_not_found"
				failure.Message = i18n.T("Asynchronous task record does not exist or was removed")
			}
			return nil, err
		}
		taskValue := &task
		lastTask = taskValue
		switch task.Status {
		case "succeeded":
			return taskValue, nil
		case "failed":
			return taskValue, taskStatusError("task_failed", i18n.T("Asynchronous task failed"))
		case "cancelled":
			return taskValue, taskStatusError("task_cancelled", i18n.T("Asynchronous task cancelled"))
		}
		timer := time.NewTimer(interval)
		select {
		case <-waitCtx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			if ctx.Err() != nil {
				return taskValue, taskStatusError("wait_cancelled", i18n.T("Wait cancelled; server task was not cancelled"))
			}
			return taskValue, taskStatusError("wait_timeout", i18n.T("Timed out waiting for asynchronous task"))
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
	err := result.New(failure.Kind, i18n.T("File uploaded but knowledge-base ingestion submission failed"))
	err.Type = "partial_write"
	err.HTTPStatus = failure.HTTPStatus
	err.ServerCode = failure.ServerCode
	err.RequestID = failure.RequestID
	return err
}

func (s *Service) BotCreate(ctx context.Context, body map[string]any, dryRun bool, options CheckOptions) (Result, error) {
	if _, exists := body["uuid"]; exists {
		return Result{}, result.New("input", i18n.T("Cannot specify uuid when creating a Bot"))
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
	data["bot"] = bot
	return verifyResourceWrite(preflight, data, body, bot)
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
	data["bot"] = bot
	return verifyResourceWrite(preflight, data, withoutUUID(body), bot)
}

func (s *Service) BotDelete(ctx context.Context, uuid string, confirmed, dryRun bool, options CheckOptions) (Result, error) {
	if err := api.ValidateResourceID(uuid); err != nil {
		return Result{}, err
	}
	if !dryRun && !confirmed {
		return Result{}, result.New("input", i18n.T("Deleting a Bot requires --yes"))
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
	return Result{Data: data, Meta: preflight.Meta()}, verificationError(i18n.T("Resource is still readable after deletion"))
}

func (s *Service) PipelineApply(ctx context.Context, body map[string]any, dryRun bool, options CheckOptions) (Result, error) {
	if body == nil {
		return Result{}, result.New("input", i18n.T("Request body must be a JSON object"))
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
	data["pipeline"] = pipeline
	return verifyResourceWrite(preflight, data, payload, pipeline)
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
	source, err := client.Pipeline(ctx, target, uuid)
	if err != nil {
		return Result{Meta: preflight.Meta()}, err
	}
	written, err := client.PipelineCopy(ctx, target, uuid)
	if err != nil {
		return Result{Meta: preflight.Meta()}, err
	}
	data := writeResultData("pipeline.copy", written.UUID)
	if written.UUID == uuid {
		return Result{Data: data, Meta: preflight.Meta()}, verificationError(i18n.T("Copy response did not return a new Pipeline UUID"))
	}
	pipeline, err := client.Pipeline(ctx, target, written.UUID)
	if err != nil {
		return Result{Data: data, Meta: preflight.Meta()}, readbackError(err)
	}
	data["pipeline"] = pipeline
	expected := map[string]any{}
	for _, field := range []string{"description", "config", "extensions_preferences"} {
		if value, exists := source[field]; exists {
			expected[field] = value
		}
	}
	finished, verifyErr := verifyResourceWrite(preflight, data, expected, pipeline)
	data["verification"].(map[string]any)["scope"] = "copied_fields"
	return finished, verifyErr
}

func (s *Service) PipelineDelete(ctx context.Context, uuid string, confirmed, dryRun bool, options CheckOptions) (Result, error) {
	if err := api.ValidateResourceID(uuid); err != nil {
		return Result{}, err
	}
	if !dryRun && !confirmed {
		return Result{}, result.New("input", i18n.T("Deleting a Pipeline requires --yes"))
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
	return Result{Data: data, Meta: preflight.Meta()}, verificationError(i18n.T("Resource is still readable after deletion"))
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
		"business_validation":     "not_run",
		"validation_scope":        "identity_permissions_capabilities",
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
		return result.New("precondition", i18n.T("Server has not explicitly confirmed support for this operation"))
	}
	if !supported {
		return result.New("precondition", i18n.T("Server explicitly does not support this operation"))
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
		return "", false, result.New("input", i18n.T("uuid must be a non-empty string"))
	}
	return uuid, true, nil
}

func validateBodyUUID(body map[string]any, expected string) error {
	uuid, exists, err := optionalBodyUUID(body)
	if err != nil {
		return err
	}
	if exists && uuid != expected {
		return result.New("input", i18n.T("Request body uuid does not match the command argument"))
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

func validateBodyFields(body map[string]any, allowed ...string) error {
	allowedSet := make(map[string]struct{}, len(allowed))
	for _, field := range allowed {
		allowedSet[field] = struct{}{}
	}
	unknown := make([]string, 0)
	for field := range body {
		if _, ok := allowedSet[field]; !ok {
			unknown = append(unknown, field)
		}
	}
	if len(unknown) == 0 {
		return nil
	}
	sort.Strings(unknown)
	return result.New("input", i18n.T("Request body contains unsupported field: ")+strings.Join(unknown, ", "))
}

func readbackError(cause error) *result.Error {
	failure := result.AsError(cause)
	err := result.New(failure.Kind, i18n.T("Write succeeded but readback verification failed"))
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
	err := result.New(failure.Kind, i18n.T("Pipeline created, but full configuration was not applied"))
	err.Type = "partial_write"
	err.HTTPStatus = failure.HTTPStatus
	err.ServerCode = failure.ServerCode
	err.RequestID = failure.RequestID
	return err
}

func (s *Service) resourceConnection(ctx context.Context, options CheckOptions) (config.Connection, api.Context, error) {
	conn, identity, err := s.connectionWithIdentity(ctx, options)
	if err != nil {
		return conn, identity, err
	}
	if !hasPermission(identity, "resource.view") {
		return conn, identity, result.New("permission", i18n.T("Current API Key lacks resource.view permission"))
	}
	return conn, identity, nil
}

// readPreflight 校验读取操作声明的权限和服务端能力，并保留解析后的连接快照。
func (s *Service) readPreflight(ctx context.Context, operation, permission string, options CheckOptions) (WritePreflight, error) {
	conn, identity, err := s.connectionWithIdentity(ctx, options)
	preflight := WritePreflight{Connection: conn, Identity: identity}
	if err != nil {
		return preflight, err
	}
	if permission != "" && !hasPermission(identity, permission) {
		return preflight, result.New("permission", i18n.T("Current API Key lacks ")+permission+i18n.T(" permission"))
	}
	capabilities, err := s.capabilities(ctx, conn)
	if err != nil {
		return preflight, err
	}
	preflight.Capabilities = capabilities
	if err := s.probeAudioModel(ctx, &preflight, operation); err != nil {
		return preflight, err
	}
	if err := requireCapability(preflight, operation); err != nil {
		return preflight, err
	}
	return preflight, nil
}

func (s *Service) connectionWithIdentity(ctx context.Context, options CheckOptions) (config.Connection, api.Context, error) {
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
	return conn, identity, nil
}

// WritePreflight 只允许已保存且绑定 Workspace 的命名 context 执行写操作。
func (s *Service) WritePreflight(ctx context.Context, operation string, options CheckOptions) (WritePreflight, error) {
	return s.writePreflight(ctx, operation, "resource.manage", options)
}

// writePreflight 校验写操作所需的权限、能力和连接绑定。
func (s *Service) writePreflight(ctx context.Context, operation, permission string, options CheckOptions) (WritePreflight, error) {
	if options.EndpointSet {
		return WritePreflight{}, result.New("precondition", i18n.T("Writes cannot use a temporary endpoint"))
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
		return WritePreflight{}, result.New("precondition", i18n.T("Writes require a saved context"))
	}
	if strings.TrimSpace(saved.ExpectedWorkspaceUUID) == "" {
		return WritePreflight{}, result.New("precondition", i18n.T("Writes require a Workspace-bound context"))
	}
	conn, err := config.Resolve(file, configOptions, s.deps.LookupEnv)
	if err != nil {
		return WritePreflight{Connection: conn}, err
	}
	if conn.Temporary {
		return WritePreflight{Connection: conn}, result.New("precondition", i18n.T("Writes cannot use a temporary connection"))
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
	if permission != "" && !hasPermission(identity, permission) {
		return WritePreflight{Connection: conn, Identity: identity}, result.New("permission", i18n.T("Current API Key lacks ")+permission+i18n.T(" permission"))
	}
	readPermission := "resource.view"
	if operation == "workspace.sandbox.update" || operation == "workspace.sandbox.reset" {
		readPermission = "workspace.view"
	} else if operation == "workspace.sandbox.check" || operation == "workspace.sandbox.test" {
		readPermission = ""
	}
	if readPermission != "" && !hasPermission(identity, readPermission) {
		return WritePreflight{Connection: conn, Identity: identity}, result.New("permission", i18n.T("Current API Key lacks the readback permission: ")+readPermission+i18n.T(" permission"))
	}
	capabilities, err := s.capabilities(ctx, conn)
	if err != nil {
		return WritePreflight{Connection: conn, Identity: identity}, err
	}
	preflight := WritePreflight{Connection: conn, Identity: identity, Capabilities: capabilities}
	if err := s.probeAudioModel(ctx, &preflight, operation); err != nil {
		return preflight, err
	}
	if err := requireCapability(preflight, operation); err != nil {
		return preflight, err
	}
	if readback := readbackCapability(operation); readback != "" {
		if err := requireCapability(preflight, readback); err != nil {
			return preflight, result.New("precondition", i18n.T("Server has not explicitly confirmed read-after-write support"))
		}
	}
	return preflight, nil
}

func readbackCapability(operation string) string {
	switch {
	case operation == "workspace.sandbox.update" || operation == "workspace.sandbox.reset":
		return "workspace.sandbox.get"
	case strings.HasPrefix(operation, "bot."):
		return "bot.get"
	case operation == "pipeline.extensions.update":
		return "pipeline.extensions.get"
	case strings.HasPrefix(operation, "pipeline."):
		return "pipeline.get"
	case operation == "knowledge_base.file.delete":
		return "knowledge_base.file.list"
	case strings.HasPrefix(operation, "knowledge_base."):
		return "knowledge_base.get"
	case strings.HasPrefix(operation, "mcp_server.") && operation != "mcp_server.test":
		return "mcp_server.get"
	case operation == "plugin.config.update":
		return "plugin.config.get"
	case operation == "provider.create" || operation == "provider.update" || operation == "provider.delete":
		return "provider.get"
	case operation == "provider.scan_models":
		return "provider.get"
	case strings.HasPrefix(operation, "model.") && (strings.HasSuffix(operation, ".create") || strings.HasSuffix(operation, ".update") || strings.HasSuffix(operation, ".delete")):
		modelType := strings.Split(operation, ".")[1]
		return "model." + modelType + ".get"
	case strings.HasPrefix(operation, "model.") && strings.HasSuffix(operation, ".test"):
		modelType := strings.Split(operation, ".")[1]
		return "model." + modelType + ".get"
	case operation == "plugin.delete":
		return "plugin.get"
	case operation == "skill.files.write":
		return "skill.files.read"
	case operation == "skill.create" || operation == "skill.update" || operation == "skill.delete":
		return "skill.get"
	default:
		return ""
	}
}

func writeContextName(file config.File, options CheckOptions, lookup func(string) (string, bool)) (string, error) {
	if options.ContextSet {
		if strings.TrimSpace(options.Context) == "" {
			return "", result.New("input", i18n.T("context must not be empty"))
		}
		return options.Context, nil
	}
	if lookup != nil {
		if value, present := lookup("LANGBOT_CONTEXT"); present {
			if strings.TrimSpace(value) == "" {
				return "", result.New("input", i18n.T("LANGBOT_CONTEXT is empty"))
			}
			return value, nil
		}
	}
	if strings.TrimSpace(file.CurrentContext) == "" {
		return "", result.New("precondition", i18n.T("Writes require a saved context"))
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
	if config.IsMember(ctx) {
		return conn.WithMember(file, s.deps.LookupEnv)
	}
	return s.withCredential(ctx, conn, options)
}

func (s *Service) withCredential(ctx context.Context, conn config.Connection, options config.Options) (config.Connection, error) {
	if config.IsMember(ctx) {
		file, err := s.load()
		if err != nil {
			return conn, err
		}
		return conn.WithMember(file, s.deps.LookupEnv)
	}
	credentialInput := s.deps.In
	if options.APIKeyStdin {
		inputParent := ctx
		if inputParent == nil {
			inputParent = context.Background()
		}
		inputTimeout := conn.Timeout
		if inputTimeout <= 0 {
			inputTimeout = 30 * time.Second
		}
		inputCtx, cancelInput := context.WithTimeout(inputParent, inputTimeout)
		value, readErr := readCredential(inputCtx, credentialInput)
		cancelInput()
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
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			failure := result.New("network", i18n.T("Timed out reading API Key; provide the complete Key via stdin and close the stream, or use an environment variable"))
			failure.Reason = "stdin_timeout"
			return "", failure
		}
		return "", result.New("network", i18n.T("Request cancelled"))
	case read := <-completed:
		if read.err != nil {
			return "", result.New("auth", i18n.T("Failed to read API Key"))
		}
		return read.value, nil
	}
}

func (s *Service) info(ctx context.Context, conn config.Connection) (api.Info, error) {
	client := api.Client{Transport: s.deps.Transport}
	return client.Info(ctx, api.Target{Endpoint: conn.Endpoint, APIKey: conn.APIKey, UserToken: conn.UserToken, Timeout: conn.Timeout})
}

func (s *Service) identity(ctx context.Context, conn config.Connection) (api.Context, error) {
	if conn.UserToken != "" {
		return (api.Client{Transport: s.deps.Transport}).MemberContext(ctx, api.Target{Endpoint: conn.Endpoint, UserToken: conn.UserToken, Timeout: conn.Timeout})
	}
	if strings.TrimSpace(conn.APIKey) == "" {
		return api.Context{}, result.AuthSetupError("missing_api_key", "LANGBOT_API_KEY")
	}
	client := api.Client{Transport: s.deps.Transport}
	identity, err := client.Context(ctx, api.Target{Endpoint: conn.Endpoint, APIKey: conn.APIKey, UserToken: conn.UserToken, Timeout: conn.Timeout})
	if err != nil && result.AsError(err).Reason == "authentication_rejected" && strings.HasPrefix(conn.CredentialSource, "env:") {
		result.AddAuthGuidance(result.AsError(err), "authentication_rejected", strings.TrimPrefix(conn.CredentialSource, "env:"))
	}
	return identity, err
}

func (s *Service) capabilities(ctx context.Context, conn config.Connection) (api.Capabilities, error) {
	client := api.Client{Transport: s.deps.Transport}
	if conn.UserToken != "" {
		return client.MemberCapabilities(ctx, api.Target{Endpoint: conn.Endpoint, UserToken: conn.UserToken, Timeout: conn.Timeout})
	}
	return client.Capabilities(ctx, api.Target{Endpoint: conn.Endpoint, APIKey: conn.APIKey, UserToken: conn.UserToken, Timeout: conn.Timeout})
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
		return result.New("input", i18n.T("timeout must not be empty"))
	}
	duration, err := time.ParseDuration(value)
	if err != nil || duration <= 0 {
		return result.New("input", i18n.T("Invalid timeout"))
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
		return config.File{}, result.New("input", i18n.T("Unable to determine configuration file path"))
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
		return result.New("input", i18n.T("Unable to determine configuration file path"))
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
	err := result.New("precondition", i18n.T("Server Workspace does not match context expectation"))
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
		if ctx.Credential.Type == "local_deployment" {
			view["credential_source"] = "local_deployment"
		}
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
		return result.New("input", i18n.T("Invalid context name"))
	}
	return nil
}

func (s *Service) credentialState(file config.File, name string) string {
	conn, err := config.Resolve(file, config.Options{Context: name, ContextSet: true}, s.deps.LookupEnv)
	if err != nil {
		return "invalid_configuration"
	}
	return s.connectionCredentialState(conn)
}

func (s *Service) connectionCredentialState(conn config.Connection) string {
	if strings.HasPrefix(conn.CredentialSource, "env:") {
		value, _ := s.deps.LookupEnv(strings.TrimPrefix(conn.CredentialSource, "env:"))
		if strings.TrimSpace(value) == "" {
			return "missing"
		}
		return "available"
	}
	if conn.CredentialSource == "stdin" {
		return "stdin_required"
	}
	if conn.CredentialSource == "local_deployment" {
		if _, err := conn.WithCredential(config.Options{}, s.deps.LookupEnv, nil); err != nil {
			return "missing"
		}
		return "available"
	}
	return "not_configured"
}
