package app

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/langbot-app/langbot-cli/internal/api"
	"github.com/langbot-app/langbot-cli/internal/config"
	"github.com/langbot-app/langbot-cli/internal/deploy"
	endpointutil "github.com/langbot-app/langbot-cli/internal/endpoint"
	"github.com/langbot-app/langbot-cli/internal/result"
)

const latestReleaseURL = "https://api.github.com/repos/langbot-app/LangBot/releases/latest"

var ansiEscapePattern = regexp.MustCompile(`\x1b\[[0-9;]*[A-Za-z]`)

type InstallOptions struct {
	Dir        string
	Version    string
	Profile    string
	Port       int
	DryRun     bool
	Timeout    string
	TimeoutSet bool
}

type AdoptOptions struct {
	Dir            string
	ComposeFile    string
	ComposeProject string
	Endpoint       string
	Profile        string
	Timeout        string
	TimeoutSet     bool
	APIKeyStdin    bool
}

type LifecycleOptions struct {
	CheckOptions
}

type LocalLogsOptions struct {
	CheckOptions
	Service string
	Tail    int
	Since   string
	Follow  bool
	Format  string
}

func (s *Service) Install(ctx context.Context, options InstallOptions) (Result, error) {
	timeout, err := operationTimeout(options.Timeout, options.TimeoutSet, 10*time.Minute)
	if err != nil {
		return Result{}, err
	}
	operationCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	dir, err := s.resolveLocalDir(options.Dir)
	if err != nil {
		return Result{}, result.New("input", "部署目录无效")
	}
	version := strings.TrimSpace(options.Version)
	if version == "" {
		version, err = s.latestRelease(operationCtx)
		if err != nil {
			return Result{}, err
		}
	}
	assets, err := deploy.NewInstallAssets(dir, version, options.Profile, options.Port)
	if err != nil {
		return Result{}, result.New("input", err.Error())
	}
	if err := s.rejectOtherManagedDeployment(assets.Dir); err != nil {
		return Result{}, err
	}
	runner := s.localRunner()
	checks, preflightErr := deploy.PreflightInstall(operationCtx, assets.Dir, assets.Port, runner)
	plan := installPlanData(assets, checks)
	if preflightErr != nil {
		return Result{Data: plan}, mapLocalError(preflightErr)
	}
	if options.DryRun {
		plan["dry_run"] = true
		return Result{Data: plan}, nil
	}

	var response Result
	locator := deploy.Locator{Path: s.deps.LocalLocatorPath}
	err = locator.WithLock(operationCtx, func() error {
		return deploy.WithMutationLock(operationCtx, assets.Dir, func() error {
			if err := s.rejectOtherManagedDeployment(assets.Dir); err != nil {
				return err
			}
			if err := deploy.ValidateInstallTarget(assets.Dir, true); err != nil {
				return result.New("precondition", "目标目录不为空，拒绝覆盖")
			}
			occupied, occupiedErr := deploy.InstallProjectOccupied(operationCtx, runner)
			if occupiedErr != nil || occupied {
				return result.New("precondition", "受管 Compose project 不可用")
			}
			pull, pullErr := runner.Docker(operationCtx, "pull", assets.Image)
			if pullErr != nil {
				return mapDockerCommandError(pull, pullErr, "拉取官方镜像失败")
			}
			imageDigest := ""
			if inspected, inspectErr := runner.Docker(operationCtx, "image", "inspect", "--format", "{{index .RepoDigests 0}}", assets.Image); inspectErr == nil {
				imageDigest = repositoryDigest(inspected.Stdout)
			}
			deploymentID, idErr := deploy.NewDeploymentID()
			if idErr != nil {
				return result.New("internal", "生成部署标识失败")
			}
			record := assets.Record(deploymentID, imageDigest)
			store := deploy.Store{Dir: assets.Dir}
			if err := store.Write(operationCtx, record); err != nil {
				return mapStoreWriteError(err)
			}
			if err := assets.Write(); err != nil {
				return result.New("server", "写入部署资产失败")
			}
			if err := locator.Write(assets.Dir); err != nil {
				return result.New("server", "写入本机部署定位记录失败")
			}
			command, commandErr := runner.Compose(operationCtx, record, "up", "-d")
			if commandErr != nil {
				readbackCtx, readbackCancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
				defer readbackCancel()
				diagnostics := deploy.Diagnostics{Dir: assets.Dir, Store: store, Runner: runner, HTTP: s.deps.LocalHTTP}
				status, _ := diagnostics.Status(readbackCtx)
				if status.Status == "running" {
					response = Result{Data: map[string]any{"action": "install", "changed": true, "record": record, "status": status}}
					return nil
				}
				response = Result{Data: map[string]any{"action": "install", "changed": true, "step": "compose_up_failed", "record": record, "status": status}}
				return uncertainDockerError(command, commandErr, "安装命令结果无法确认")
			}
			if err := s.waitForLocalHTTP(operationCtx, record.Endpoint); err != nil {
				response = Result{Data: lifecycleFailureData("install", record, "health_check_failed")}
				return result.New("result_unknown", "容器已启动，但 LangBot HTTP 健康状态未确认")
			}
			diagnostics := deploy.Diagnostics{Dir: assets.Dir, Store: store, Runner: runner, HTTP: s.deps.LocalHTTP}
			status, statusErr := diagnostics.Status(operationCtx)
			response = Result{Data: map[string]any{"action": "install", "changed": true, "record": record, "status": status}}
			if statusErr != nil || status.Status != "running" {
				return result.New("result_unknown", "安装已执行，但最终运行状态未确认")
			}
			return nil
		})
	})
	if err != nil {
		if response.Data == nil {
			response.Data = plan
		}
		return response, normalizeLocalMutationError(err)
	}
	return response, nil
}

func (s *Service) Adopt(ctx context.Context, options AdoptOptions) (Result, error) {
	timeout, err := operationTimeout(options.Timeout, options.TimeoutSet, 2*time.Minute)
	if err != nil {
		return Result{}, err
	}
	operationCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	dir, err := s.resolveLocalDir(options.Dir)
	if err != nil {
		return Result{}, result.New("input", "部署记录目录无效")
	}
	if err := s.rejectOtherManagedDeployment(dir); err != nil {
		return Result{}, err
	}
	composeFile, err := resolveComposeFile(options.ComposeFile)
	if err != nil {
		return Result{}, err
	}
	project := strings.TrimSpace(options.ComposeProject)
	if !validComposeProject(project) {
		return Result{}, result.New("input", "Compose project 名称无效")
	}
	endpoint, err := endpointutil.Normalize(strings.TrimSpace(options.Endpoint))
	if err != nil {
		return Result{}, err
	}
	profile := strings.ToLower(strings.TrimSpace(options.Profile))
	if profile == "" {
		profile = "basic"
	}
	if profile != "basic" && profile != "all" {
		return Result{}, result.New("input", "profile 只支持 basic 或 all")
	}
	if !loopbackEndpoint(endpoint) {
		return Result{}, result.New("precondition", "adopt 只允许绑定本机 loopback endpoint")
	}
	target := api.Target{Endpoint: endpoint, Timeout: timeout}
	if options.APIKeyStdin {
		key, keyErr := readCredential(operationCtx, s.deps.In)
		if keyErr != nil {
			return Result{}, keyErr
		}
		credential, credentialErr := (config.Connection{}).WithCredential(config.Options{APIKeyStdin: true}, s.deps.LookupEnv, strings.NewReader(key))
		if credentialErr != nil {
			return Result{}, credentialErr
		}
		target.APIKey = credential.APIKey
	}
	client := api.Client{Transport: s.deps.Transport}
	info, err := client.Info(operationCtx, target)
	if err != nil {
		return Result{}, err
	}
	if environmentType(info.Edition) == "cloud" {
		return Result{}, result.New("precondition", "Cloud 服务不能绑定为本机生命周期")
	}
	record := deploy.Record{Driver: deploy.DriverDockerCompose, CoreVersion: info.Version, Profile: profile, ComposeProject: project, ComposeFile: composeFile, DataDir: filepath.Join(filepath.Dir(composeFile), "data"), Endpoint: endpoint, Port: endpointPort(endpoint)}
	if target.APIKey != "" {
		identity, identityErr := client.Context(operationCtx, target)
		if identityErr != nil {
			return Result{}, identityErr
		}
		record.InstanceUUID = identity.InstanceUUID
	}
	services, commandErr := s.localRunner().Compose(operationCtx, record, "config", "--services")
	if commandErr != nil {
		return Result{}, result.New("precondition", "无法验证 Compose 项目")
	}
	if missing := missingRequiredServices(services.Stdout, profile); len(missing) != 0 {
		return Result{Data: map[string]any{"missing_services": missing}}, result.New("precondition", "Compose 项目缺少 LangBot 必需服务")
	}
	published, commandErr := s.localRunner().Compose(operationCtx, record, "port", "langbot", "5300")
	if commandErr != nil || !publishedPortMatches(published.Stdout, record.Port) {
		return Result{}, result.New("precondition", "Compose 项目的 langbot 服务未发布到 endpoint 端口")
	}
	composeData, err := os.ReadFile(composeFile)
	if err != nil {
		return Result{}, result.New("precondition", "无法读取 Compose 文件")
	}
	sum := sha256.Sum256(composeData)
	record.Asset = deploy.AssetInfo{Name: filepath.Base(composeFile), Digest: "sha256:" + hex.EncodeToString(sum[:])}

	var changed bool
	locator := deploy.Locator{Path: s.deps.LocalLocatorPath}
	err = locator.WithLock(operationCtx, func() error {
		if err := deploy.WithMutationLock(operationCtx, dir, func() error {
			if err := s.rejectOtherManagedDeployment(dir); err != nil {
				return err
			}
			store := deploy.Store{Dir: dir}
			existing, readErr := store.Read()
			if readErr == nil {
				if sameDeployment(existing, record) {
					record = existing
					return nil
				}
				return result.New("precondition", "已存在其他 lbctl 受管部署")
			}
			if !errors.Is(readErr, deploy.ErrNotInstalled) && !errors.Is(readErr, deploy.ErrNotManaged) {
				return mapStoreWriteError(readErr)
			}
			if errors.Is(readErr, deploy.ErrNotManaged) {
				if targetErr := deploy.ValidateInstallTarget(dir, true); targetErr != nil {
					return result.New("precondition", "部署记录目录不为空，拒绝覆盖")
				}
			}
			deploymentID, idErr := deploy.NewDeploymentID()
			if idErr != nil {
				return result.New("internal", "生成部署标识失败")
			}
			record.DeploymentID = deploymentID
			if err := store.Write(operationCtx, record); err != nil {
				return mapStoreWriteError(err)
			}
			changed = true
			return nil
		}); err != nil {
			return err
		}
		if err := locator.Write(dir); err != nil {
			return result.New("server", "写入本机部署定位记录失败")
		}
		return nil
	})
	if err != nil {
		return Result{}, normalizeLocalMutationError(err)
	}
	return Result{Data: map[string]any{"action": "adopt", "changed": changed, "record": record}}, nil
}

func (s *Service) Start(ctx context.Context, options LifecycleOptions) (Result, error) {
	return s.runLifecycle(ctx, "start", options)
}

func (s *Service) Stop(ctx context.Context, options LifecycleOptions) (Result, error) {
	return s.runLifecycle(ctx, "stop", options)
}

func (s *Service) Restart(ctx context.Context, options LifecycleOptions) (Result, error) {
	return s.runLifecycle(ctx, "restart", options)
}

func (s *Service) LocalLogs(ctx context.Context, options LocalLogsOptions) (Result, error) {
	record, diagnostics, err := s.lifecycleTarget(ctx, options.CheckOptions)
	if err != nil {
		return Result{}, err
	}
	if err := validateLogsOptions(options, record); err != nil {
		return Result{}, err
	}
	timeout, err := operationTimeout(options.Timeout, options.TimeoutSet, 30*time.Second)
	if err != nil {
		return Result{}, err
	}
	operationCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	args := logArguments(options, false)
	command, commandErr := diagnostics.Runner.Compose(operationCtx, record, args...)
	if commandErr != nil {
		return Result{}, mapDockerCommandError(command, commandErr, "读取日志失败")
	}
	lines := splitLogLines(command.Stdout, readLogSecrets(record))
	return Result{Data: map[string]any{"service": selectedLogService(options.Service), "lines": lines, "count": len(lines)}}, nil
}

func (s *Service) FollowLocalLogs(ctx context.Context, options LocalLogsOptions) error {
	record, diagnostics, err := s.lifecycleTarget(ctx, options.CheckOptions)
	if err != nil {
		return err
	}
	if err := validateLogsOptions(options, record); err != nil {
		return err
	}
	operationCtx := ctx
	cancel := func() {}
	if options.TimeoutSet {
		timeout, timeoutErr := operationTimeout(options.Timeout, true, 0)
		if timeoutErr != nil {
			return timeoutErr
		}
		operationCtx, cancel = context.WithTimeout(ctx, timeout)
	}
	defer cancel()
	stream, ok := diagnostics.Runner.(deploy.StreamRunner)
	if !ok {
		return result.New("incompatible", "当前 Docker runner 不支持日志跟踪")
	}
	out := s.deps.LocalLogOut
	if out == nil {
		out = io.Discard
	}
	secrets := readLogSecrets(record)
	if options.Format == "json" {
		out = newJSONLineWriter(out, selectedLogService(options.Service), secrets)
	} else {
		out = newSanitizedLineWriter(out, secrets)
	}
	errOut := s.deps.LocalLogErr
	if errOut == nil {
		errOut = io.Discard
	}
	safeErr := newSanitizedLineWriter(errOut, secrets)
	streamErr := stream.ComposeStream(operationCtx, record, out, safeErr, logArguments(options, true)...)
	_ = safeErr.Flush()
	if streamErr != nil {
		if errors.Is(operationCtx.Err(), context.Canceled) {
			return result.New("network", "日志跟踪已取消")
		}
		if errors.Is(operationCtx.Err(), context.DeadlineExceeded) {
			return result.New("network", "日志跟踪超时")
		}
		return result.New("server", "日志跟踪失败")
	}
	switch writer := out.(type) {
	case *jsonLineWriter:
		return writer.Flush()
	case *sanitizedLineWriter:
		return writer.Flush()
	}
	return nil
}

func (s *Service) runLifecycle(ctx context.Context, action string, options LifecycleOptions) (Result, error) {
	record, diagnostics, err := s.lifecycleTarget(ctx, options.CheckOptions)
	if err != nil {
		return Result{}, err
	}
	timeout, err := operationTimeout(options.Timeout, options.TimeoutSet, lifecycleTimeout(action))
	if err != nil {
		return Result{}, err
	}
	operationCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var response Result
	err = deploy.WithMutationLock(operationCtx, diagnostics.Dir, func() error {
		if err := deploy.CheckUpgradeInterrupted(diagnostics.Dir); err != nil {
			return result.New("recovery_required", err.Error())
		}
		current, readErr := diagnostics.Store.Read()
		if readErr != nil || current.DeploymentID != record.DeploymentID {
			return result.New("precondition", "本机部署绑定已变更")
		}
		before, beforeErr := diagnostics.Status(operationCtx)
		if beforeErr != nil || before.Status == "unknown" {
			return result.New("precondition", "无法确认本机部署状态，请先运行 doctor")
		}
		if (action == "start" && before.Status == "running") || (action == "stop" && before.Status == "stopped") {
			response = Result{Data: map[string]any{"action": action, "changed": false, "status": before}}
			return nil
		}
		if action == "restart" && before.Status == "stopped" {
			return result.New("precondition", "部署未运行，请使用 start")
		}
		args := map[string][]string{"start": {"up", "-d"}, "stop": {"stop"}, "restart": {"restart"}}[action]
		command, commandErr := diagnostics.Runner.Compose(operationCtx, current, args...)
		if commandErr == nil && (action == "start" || action == "restart") {
			_ = s.waitForLocalHTTP(operationCtx, current.Endpoint)
		}
		readbackCtx, readbackCancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer readbackCancel()
		after, statusErr := diagnostics.Status(readbackCtx)
		response = Result{Data: map[string]any{"action": action, "changed": true, "status": after}}
		desired := after.Status == "running"
		if action == "stop" {
			desired = after.Status == "stopped"
		}
		if action == "restart" && commandErr != nil {
			return uncertainDockerError(command, commandErr, "重启命令结果无法确认")
		}
		if desired {
			return nil
		}
		if commandErr != nil {
			return uncertainDockerError(command, commandErr, "命令可能已执行，但结果无法确认")
		}
		if statusErr != nil {
			return result.New("result_unknown", "命令已执行，但状态回读失败")
		}
		return result.New("result_unknown", "命令已执行，但未达到期望状态")
	})
	return response, normalizeLocalMutationError(err)
}

func (s *Service) lifecycleTarget(ctx context.Context, options CheckOptions) (deploy.Record, deploy.Diagnostics, error) {
	resolved, discoveryErr := s.discoverActiveEnvironment(ctx, options, false)
	if discoveryErr != nil && result.AsError(discoveryErr).Kind != "network" {
		return deploy.Record{}, deploy.Diagnostics{}, discoveryErr
	}
	if resolved.Environment.Identity.Status == "mismatch" || resolved.Environment.Connection.Authentication == "failed" {
		if discoveryErr != nil {
			return deploy.Record{}, deploy.Diagnostics{}, discoveryErr
		}
		return deploy.Record{}, deploy.Diagnostics{}, result.New("precondition", "当前 context 身份或 Workspace 绑定未通过")
	}
	if !resolved.LocalMatch || resolved.Diagnostics == nil {
		return deploy.Record{}, deploy.Diagnostics{}, result.New("precondition", "当前 Active Environment 未绑定本机受管部署")
	}
	if resolved.Environment.Type == "cloud" || !sameLocalEndpoint(resolved.Record.Endpoint, resolved.Connection.Endpoint) {
		return deploy.Record{}, deploy.Diagnostics{}, result.New("precondition", "当前 Active Environment 与本机部署 endpoint 不一致")
	}
	if resolved.Record.Driver != deploy.DriverDockerCompose {
		return deploy.Record{}, deploy.Diagnostics{}, result.New("incompatible", "当前本机部署不支持 Docker Compose 生命周期操作")
	}
	return resolved.Record, *resolved.Diagnostics, nil
}

func sameLocalEndpoint(left, right string) bool {
	first, firstErr := endpointutil.Normalize(left)
	second, secondErr := endpointutil.Normalize(right)
	return firstErr == nil && secondErr == nil && first == second
}

func (s *Service) rejectOtherManagedDeployment(dir string) error {
	if strings.TrimSpace(s.deps.LocalDir) != "" {
		if existing, err := deploy.ResolveDir(s.deps.LocalDir); err == nil && existing != dir {
			if _, readErr := (deploy.Store{Dir: existing}).Read(); readErr == nil {
				return result.New("precondition", "本机已有其他 lbctl 受管部署")
			}
		}
	}
	if s.deps.LocalDir != "" {
		return nil
	}
	existing, err := (deploy.Locator{Path: s.deps.LocalLocatorPath}).Resolve("")
	if err != nil {
		return result.New("precondition", "本机部署定位记录不可用")
	}
	if existing != dir {
		if _, readErr := (deploy.Store{Dir: existing}).Read(); readErr == nil {
			return result.New("precondition", "本机已有其他 lbctl 受管部署")
		} else if readErr != nil && !errors.Is(readErr, deploy.ErrNotInstalled) {
			return result.New("precondition", "已有本机部署记录，请先诊断")
		}
	}
	return nil
}

func (s *Service) localRunner() deploy.Runner {
	if s.deps.LocalRunner != nil {
		return s.deps.LocalRunner
	}
	return deploy.DockerRunner{}
}

func (s *Service) latestRelease(ctx context.Context) (string, error) {
	if s.deps.LatestRelease != nil {
		return s.deps.LatestRelease(ctx)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, latestReleaseURL, nil)
	if err != nil {
		return "", result.New("internal", "创建 Release 请求失败")
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "lbctl")
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", result.New("network", "无法查询 LangBot 最新稳定版本")
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", result.New("server", "LangBot Release 服务返回异常状态")
	}
	var payload struct {
		Tag        string `json:"tag_name"`
		Draft      bool   `json:"draft"`
		Prerelease bool   `json:"prerelease"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&payload); err != nil || payload.Draft || payload.Prerelease {
		return "", result.New("incompatible", "LangBot Release 响应无效")
	}
	return strings.TrimSpace(payload.Tag), nil
}

func (s *Service) waitForLocalHTTP(ctx context.Context, endpoint string) error {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		_, err := (api.Client{Transport: s.deps.Transport}).Info(ctx, api.Target{Endpoint: endpoint, Timeout: 5 * time.Second})
		if err == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func operationTimeout(value string, set bool, fallback time.Duration) (time.Duration, error) {
	if !set {
		return fallback, nil
	}
	timeout, err := time.ParseDuration(strings.TrimSpace(value))
	if err != nil || timeout <= 0 {
		return 0, result.New("input", "timeout 无效")
	}
	return timeout, nil
}

func lifecycleTimeout(action string) time.Duration {
	if action == "stop" {
		return time.Minute
	}
	return 2 * time.Minute
}

func installPlanData(assets deploy.InstallAssets, checks []deploy.Check) map[string]any {
	return map[string]any{
		"action": "install", "version": assets.Version, "image": assets.Image, "profile": assets.Profile,
		"dir": assets.Dir, "endpoint": fmt.Sprintf("http://127.0.0.1:%d", assets.Port), "compose_project": assets.Project,
		"compose_file": assets.ComposeFile, "data_dir": assets.DataDir, "asset_digest": assets.Digest, "checks": checks,
	}
}

func lifecycleFailureData(action string, record deploy.Record, step string) map[string]any {
	return map[string]any{"action": action, "changed": true, "step": step, "record": record}
}

func repositoryDigest(value string) string {
	value = strings.TrimSpace(value)
	if index := strings.LastIndex(value, "@sha256:"); index >= 0 {
		return value[index+1:]
	}
	return ""
}

func mapDockerCommandError(command deploy.CommandResult, err error, message string) error {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return result.New("result_unknown", message)
	}
	lower := strings.ToLower(command.Stderr)
	if strings.Contains(lower, "permission denied") || strings.Contains(lower, "access denied") {
		return result.New("permission", "当前用户无权访问 Docker daemon")
	}
	if strings.Contains(lower, "no such host") || strings.Contains(lower, "timeout") || strings.Contains(lower, "connection") {
		return result.New("network", message)
	}
	return result.New("server", message)
}

func uncertainDockerError(command deploy.CommandResult, err error, message string) error {
	return result.New("result_unknown", message)
}

func mapStoreWriteError(err error) error {
	if errors.Is(err, deploy.ErrNotManaged) || errors.Is(err, deploy.ErrCorrupt) {
		return result.New("precondition", "本机部署记录目录不可用")
	}
	return result.New("server", "写入本机部署记录失败")
}

func normalizeLocalMutationError(err error) error {
	if err == nil {
		return nil
	}
	var typed *result.Error
	if errors.As(err, &typed) {
		return typed
	}
	return mapLocalError(err)
}

func resolveComposeFile(value string) (string, error) {
	path, err := filepath.Abs(filepath.Clean(strings.TrimSpace(value)))
	if err != nil || strings.TrimSpace(value) == "" {
		return "", result.New("input", "Compose 文件路径无效")
	}
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return "", result.New("precondition", "Compose 文件不存在或不可用")
	}
	return path, nil
}

func validComposeProject(value string) bool {
	if value == "" || len(value) > 63 {
		return false
	}
	for index, char := range value {
		if index == 0 && !((char >= 'a' && char <= 'z') || (char >= '0' && char <= '9')) {
			return false
		}
		if (char >= 'a' && char <= 'z') || (char >= '0' && char <= '9') || char == '-' || char == '_' {
			continue
		}
		return false
	}
	return true
}

func loopbackEndpoint(endpoint string) bool {
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return false
	}
	host := strings.ToLower(parsed.Hostname())
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func publishedPortMatches(value string, port int) bool {
	for _, field := range strings.Fields(value) {
		if strings.HasSuffix(strings.TrimSpace(field), ":"+strconv.Itoa(port)) {
			return true
		}
	}
	return false
}

func endpointPort(endpoint string) int {
	parsed, err := url.Parse(endpoint)
	if err == nil && parsed.Port() != "" {
		if port, parseErr := strconv.Atoi(parsed.Port()); parseErr == nil {
			return port
		}
	}
	if parsed != nil && parsed.Scheme == "https" {
		return 443
	}
	return 80
}

func missingRequiredServices(raw, profile string) []string {
	available := map[string]bool{}
	for _, line := range strings.Fields(raw) {
		available[strings.TrimSpace(line)] = true
	}
	required := []string{"langbot", "langbot_plugin_runtime"}
	if profile == "all" {
		required = append(required, "langbot_box")
	}
	missing := make([]string, 0)
	for _, service := range required {
		if !available[service] {
			missing = append(missing, service)
		}
	}
	return missing
}

func sameDeployment(left, right deploy.Record) bool {
	if left.InstanceUUID != "" && right.InstanceUUID != "" && left.InstanceUUID != right.InstanceUUID {
		return false
	}
	return left.Driver == right.Driver && left.Profile == right.Profile && left.ComposeProject == right.ComposeProject && filepath.Clean(left.ComposeFile) == filepath.Clean(right.ComposeFile) && left.Endpoint == right.Endpoint
}

func validateLogsOptions(options LocalLogsOptions, record deploy.Record) error {
	if options.Tail < 1 || options.Tail > 5000 {
		return result.New("input", "tail 必须在 1-5000 之间")
	}
	service := strings.TrimSpace(options.Service)
	if service == "" {
		return nil
	}
	allowed := map[string]bool{"langbot": true, "langbot_plugin_runtime": true}
	if record.Profile == "all" || record.Profile == "box" {
		allowed["langbot_box"] = true
	}
	if !allowed[service] {
		return result.New("input", "service 不属于当前部署")
	}
	return nil
}

func logArguments(options LocalLogsOptions, follow bool) []string {
	args := []string{"logs", "--no-color", "--tail", strconv.Itoa(options.Tail)}
	if strings.TrimSpace(options.Since) != "" {
		args = append(args, "--since", strings.TrimSpace(options.Since))
	}
	if follow {
		args = append(args, "--follow")
	}
	if service := strings.TrimSpace(options.Service); service != "" {
		args = append(args, service)
	}
	return args
}

func selectedLogService(service string) string {
	if strings.TrimSpace(service) == "" {
		return "all"
	}
	return strings.TrimSpace(service)
}

func splitLogLines(value string, secrets []string) []string {
	scanner := bufio.NewScanner(strings.NewReader(value))
	lines := make([]string, 0)
	for scanner.Scan() {
		lines = append(lines, sanitizeLogLine(scanner.Text(), secrets))
	}
	return lines
}

type jsonLineWriter struct {
	mu      sync.Mutex
	out     io.Writer
	service string
	secrets []string
	buffer  strings.Builder
}

func newJSONLineWriter(out io.Writer, service string, secrets []string) *jsonLineWriter {
	return &jsonLineWriter{out: out, service: service, secrets: secrets}
}

func (w *jsonLineWriter) Write(data []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.buffer.Write(data)
	for {
		value := w.buffer.String()
		index := strings.IndexByte(value, '\n')
		if index < 0 {
			break
		}
		if err := w.writeLine(value[:index]); err != nil {
			return 0, err
		}
		w.buffer.Reset()
		w.buffer.WriteString(value[index+1:])
	}
	return len(data), nil
}

func (w *jsonLineWriter) Flush() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.buffer.Len() == 0 {
		return nil
	}
	line := w.buffer.String()
	w.buffer.Reset()
	return w.writeLine(line)
}

func (w *jsonLineWriter) writeLine(line string) error {
	data, err := json.Marshal(map[string]any{"service": w.service, "line": sanitizeLogLine(line, w.secrets)})
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(w.out, string(data))
	return err
}

type sanitizedLineWriter struct {
	mu      sync.Mutex
	out     io.Writer
	secrets []string
	buffer  strings.Builder
}

func newSanitizedLineWriter(out io.Writer, secrets []string) *sanitizedLineWriter {
	return &sanitizedLineWriter{out: out, secrets: secrets}
}

func (w *sanitizedLineWriter) Write(data []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.buffer.Write(data)
	for {
		value := w.buffer.String()
		index := strings.IndexByte(value, '\n')
		if index < 0 {
			break
		}
		if _, err := fmt.Fprintln(w.out, sanitizeLogLine(value[:index], w.secrets)); err != nil {
			return 0, err
		}
		w.buffer.Reset()
		w.buffer.WriteString(value[index+1:])
	}
	return len(data), nil
}

func (w *sanitizedLineWriter) Flush() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.buffer.Len() == 0 {
		return nil
	}
	line := sanitizeLogLine(w.buffer.String(), w.secrets)
	w.buffer.Reset()
	_, err := fmt.Fprintln(w.out, line)
	return err
}

func sanitizeLogLine(line string, secrets []string) string {
	line = strings.ReplaceAll(line, `\u001b`, "\x1b")
	line = ansiEscapePattern.ReplaceAllString(line, "")
	for _, secret := range secrets {
		if secret != "" {
			line = strings.ReplaceAll(line, secret, "[REDACTED]")
		}
	}
	return line
}

func readLogSecrets(record deploy.Record) []string {
	if strings.TrimSpace(record.EnvFile) == "" {
		return nil
	}
	data, err := os.ReadFile(record.EnvFile)
	if err != nil {
		return nil
	}
	secrets := make([]string, 0, 2)
	for _, line := range strings.Split(string(data), "\n") {
		name, value, ok := strings.Cut(line, "=")
		if !ok || !strings.HasSuffix(strings.TrimSpace(name), "_TOKEN") {
			continue
		}
		value = strings.TrimSpace(value)
		if len(value) >= 8 {
			secrets = append(secrets, value)
		}
	}
	return secrets
}
