package app

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
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
	data := checkData(conn, info, err)
	if err != nil {
		return Result{Data: data, Meta: connectionMeta(conn)}, err
	}
	return Result{Data: data, Meta: connectionMeta(conn)}, nil
}

func (s *Service) Status(ctx context.Context, options CheckOptions) (Result, error) {
	return s.Check(ctx, options)
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

func (s *Service) Identity(kind string) (Result, error) {
	return Result{}, result.New("precondition", kind+" 尚未接入可信服务端发现接口")
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
	credentialInput := s.deps.In
	if options.APIKeyStdin {
		value, readErr := readCredential(ctx, credentialInput)
		if readErr != nil {
			return conn, readErr
		}
		credentialInput = strings.NewReader(value)
	}
	conn, err = conn.WithCredential(options, s.deps.LookupEnv, credentialInput)
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
					data := checkData(conn, api.Info{}, resolveErr)
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
				results[index] = item{name: name, data: checkData(conn, info, infoErr), err: infoErr}
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

func checkData(conn config.Connection, info api.Info, err error) map[string]any {
	diagnosticOK := err == nil
	reachable := diagnosticOK
	if err != nil {
		reachable = result.AsError(err).HTTPStatus != 0
	}
	data := map[string]any{
		"context":       conn.Context,
		"endpoint":      conn.Endpoint,
		"reachable":     reachable,
		"diagnostic_ok": diagnosticOK,
		"identity":      "unknown",
		"capabilities":  "unknown",
	}
	if err == nil {
		data["server_version"] = info.Version
		data["server_edition"] = info.Edition
	}
	if err != nil {
		data["error"] = result.AsError(err)
	}
	return data
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
