package deploy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"
)

type Problem struct {
	Kind string
	Msg  string
}

func (p Problem) Error() string { return p.Msg }

type StatusResult struct {
	Status           string         `json:"status" yaml:"status"`
	Dir              string         `json:"dir" yaml:"dir"`
	RequiredServices []string       `json:"required_services,omitempty" yaml:"required_services,omitempty"`
	Record           any            `json:"record,omitempty" yaml:"record,omitempty"`
	Containers       []Container    `json:"containers,omitempty" yaml:"containers,omitempty"`
	HTTP             map[string]any `json:"http,omitempty" yaml:"http,omitempty"`
	Evidence         map[string]any `json:"evidence" yaml:"evidence"`
}

type Diagnostics struct {
	Dir    string
	Store  Store
	Runner Runner
	HTTP   *http.Client
}

func (d Diagnostics) Status(ctx context.Context) (StatusResult, error) {
	result := StatusResult{Status: "unknown", Dir: d.Dir, Evidence: map[string]any{}}
	record, err := d.Store.Read()
	if errors.Is(err, ErrNotInstalled) {
		result.Status = "not_installed"
		result.Evidence["record"] = "missing"
		return result, nil
	}
	if errors.Is(err, ErrNotManaged) {
		result.Evidence["record"] = "unmanaged"
		return result, Problem{Kind: "precondition", Msg: "目标目录不是 lbctl 管理的部署"}
	}
	if errors.Is(err, ErrCorrupt) {
		result.Evidence["record"] = "corrupt"
		return result, Problem{Kind: "precondition", Msg: "本地部署记录损坏"}
	}
	if err != nil {
		result.Evidence["record"] = "unreadable"
		return result, Problem{Kind: "precondition", Msg: "无法读取本地部署记录"}
	}
	result.Record = record
	result.RequiredServices = requiredServices(record)
	result.Evidence["record"] = "valid"
	if !regularFile(record.ComposeFile) {
		result.Evidence["compose_asset"] = "missing_or_invalid"
		return result, Problem{Kind: "precondition", Msg: "Compose 部署文件不存在或不可用"}
	}
	result.Evidence["compose_asset"] = "valid"
	if d.Runner == nil {
		return result, Problem{Kind: "internal", Msg: "本地 Docker runner 未配置"}
	}
	dockerResult, err := d.Runner.Docker(ctx, "info", "--format", "{{.ServerVersion}}")
	if err != nil {
		result.Evidence["docker_daemon"] = "unavailable"
		return result, dockerProblem(dockerResult, err)
	}
	result.Evidence["docker_daemon"] = "available"
	command, err := d.Runner.Compose(ctx, record, "ps", "--format", "json")
	if err != nil {
		result.Evidence["compose"] = "unavailable"
		return result, Problem{Kind: "incompatible", Msg: "无法读取 Compose 服务状态"}
	}
	containers, err := ParseContainers(command.Stdout)
	if err != nil {
		result.Evidence["compose"] = "invalid"
		return result, Problem{Kind: "incompatible", Msg: "Compose 服务状态格式无效"}
	}
	result.Containers = containers
	result.Evidence["compose"] = "available"
	health, healthErr := d.probeHTTP(ctx, record)
	result.HTTP = health
	if healthErr != nil {
		result.Evidence["http"] = "unavailable"
	} else {
		result.Evidence["http"] = "healthy"
	}
	result.Status = inferStatus(record, containers, health, healthErr)
	if result.Status == "unknown" {
		return result, Problem{Kind: "network", Msg: "无法确认本地部署状态"}
	}
	return result, nil
}

func (d Diagnostics) Doctor(ctx context.Context) ([]Check, error) {
	checks := make([]Check, 0, 12)
	add := func(name, status, reason string, detail any) {
		checks = append(checks, Check{Name: name, Status: status, Reason: reason, Detail: detail})
	}
	if d.Runner == nil {
		return nil, Problem{Kind: "internal", Msg: "本地 Docker runner 未配置"}
	}
	if _, err := d.Runner.Docker(ctx, "--version"); err != nil {
		add("docker_cli", "error", "找不到可用的 Docker CLI", nil)
	} else {
		add("docker_cli", "ok", "Docker CLI 可用", nil)
	}
	daemonResult, daemonErr := d.Runner.Docker(ctx, "info", "--format", "{{.ServerVersion}}")
	if daemonErr != nil {
		if strings.Contains(daemonResult.Stderr, localContextRequired) {
			add("docker_daemon", "error", "当前 Docker context 指向远程 daemon", nil)
			add("permission", "warn", "未访问本机 Docker daemon，无法验证当前用户权限", nil)
		} else if isPermissionError(daemonResult.Stderr) {
			add("docker_daemon", "error", "当前用户无权访问 Docker daemon", nil)
			add("permission", "error", "Docker daemon 权限检查失败", nil)
		} else {
			add("docker_daemon", "error", "Docker daemon 不可达", nil)
			add("permission", "warn", "Docker daemon 不可达，无法验证当前用户权限", nil)
		}
	} else {
		add("docker_daemon", "ok", "Docker daemon 可用", nil)
		if currentUserIsRoot() {
			add("permission", "warn", "当前进程以 root 运行", nil)
		} else {
			add("permission", "ok", "当前用户可访问 Docker daemon", nil)
		}
	}
	compose, composeErr := d.Runner.Docker(ctx, "compose", "version", "--short")
	if composeErr != nil || !isComposeV2(compose.Stdout) {
		add("compose_v2", "error", "需要 Docker Compose v2 或更高版本", strings.TrimSpace(compose.Stdout))
	} else {
		add("compose_v2", "ok", "Docker Compose 插件可用", strings.TrimSpace(compose.Stdout))
	}
	if supportedPlatform() {
		add("platform", "ok", "当前平台可执行只读预检", CurrentPlatform())
	} else {
		add("platform", "error", "当前平台不在 lbctl 支持范围内", CurrentPlatform())
	}

	diskPath := d.Dir
	if stat, err := os.Stat(d.Dir); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			ancestor, ancestorErr := nearestExistingDir(d.Dir)
			if ancestorErr == nil {
				diskPath = ancestor
				add("data_dir", "ok", "部署目录尚未创建，已找到现有上级目录", map[string]any{"dir": d.Dir, "ancestor": ancestor})
			} else {
				add("data_dir", "error", "无法访问部署目录的上级目录", d.Dir)
			}
		} else {
			add("data_dir", "error", "部署目录不可访问", d.Dir)
		}
	} else if !stat.IsDir() {
		add("data_dir", "error", "部署路径不是目录", d.Dir)
	} else {
		add("data_dir", "ok", "部署目录可访问", d.Dir)
	}
	if free, err := availableBytes(diskPath); err != nil {
		add("disk_space", "warn", "无法读取可用磁盘空间", nil)
	} else {
		status, reason := "ok", "可用磁盘空间充足"
		if free < 1<<30 {
			status, reason = "warn", "可用磁盘空间低于 1 GiB"
		}
		add("disk_space", status, reason, free)
	}
	port := defaultPort
	record, recordErr := d.Store.Read()
	if recordErr == nil && record.Port != 0 {
		port = record.Port
	}
	switch {
	case errors.Is(recordErr, ErrNotInstalled):
		add("deployment_record", "warn", "尚未发现受管部署记录", nil)
	case errors.Is(recordErr, ErrNotManaged):
		if asset := findComposeAsset(d.Dir); asset != "" {
			add("deployment_record", "warn", "发现非受管 Compose 目录；仅提供只读诊断，接管需显式操作", map[string]any{"status": "unmanaged", "compose_file": asset})
		} else {
			add("deployment_record", "warn", "目标目录不是 lbctl 受管部署", map[string]any{"status": "unmanaged"})
		}
	case recordErr != nil:
		add("deployment_record", "error", "部署记录损坏或版本不兼容", nil)
	default:
		add("deployment_record", "ok", "部署记录有效", record)
	}
	if recordErr == nil {
		if !regularFile(record.ComposeFile) {
			add("compose_assets", "error", "Compose 文件不存在", record.ComposeFile)
		} else {
			add("compose_assets", "ok", "Compose 文件可访问", record.ComposeFile)
		}
		composeResult, err := d.Runner.Compose(ctx, record, "ps", "--format", "json")
		if err != nil {
			add("container_visibility", "error", "无法读取受管容器状态", nil)
		} else {
			add("container_visibility", "ok", "受管容器状态可读取", nil)
			portStatus, portReason := d.portCheckForRecord(ctx, record, composeResult.Stdout)
			add("default_port", portStatus, portReason, port)
		}
	} else {
		if available, err := portAvailable(port); err != nil {
			add("default_port", "warn", "默认端口检查失败", port)
		} else if !available {
			add("default_port", "warn", "默认端口已被占用", port)
		} else {
			add("default_port", "ok", "默认端口可用", port)
		}
		if asset := findComposeAsset(d.Dir); asset != "" {
			add("compose_assets", "warn", "发现非受管 Compose 资产，仅做只读识别", asset)
			add("container_visibility", "warn", "非受管部署未绑定 Compose project，不读取容器", nil)
		} else {
			add("compose_assets", "warn", "尚未安装，暂无 Compose 资产", nil)
			add("container_visibility", "warn", "尚未安装，暂无受管容器", nil)
		}
	}
	for _, check := range checks {
		if check.Status != "error" {
			continue
		}
		kind := "precondition"
		if check.Name == "compose_v2" {
			kind = "incompatible"
		}
		return checks, Problem{Kind: kind, Msg: "本机预检存在错误项"}
	}
	return checks, nil
}

func findComposeAsset(dir string) string {
	for _, name := range []string{"docker-compose.yaml", "docker-compose.yml", "compose.yaml", "compose.yml"} {
		path := filepath.Join(dir, name)
		if info, err := os.Lstat(path); err == nil && info.Mode().IsRegular() {
			return path
		}
	}
	return ""
}

func regularFile(path string) bool {
	if strings.TrimSpace(path) == "" {
		return false
	}
	info, err := os.Lstat(path)
	return err == nil && info.Mode().IsRegular()
}

func isComposeV2(value string) bool {
	value = strings.TrimPrefix(strings.TrimSpace(value), "v")
	if value == "" {
		return false
	}
	major, _, _ := strings.Cut(value, ".")
	version, err := strconv.Atoi(major)
	return err == nil && version >= 2
}

func isPermissionError(stderr string) bool {
	value := strings.ToLower(stderr)
	return strings.Contains(value, "permission denied") || strings.Contains(value, "access is denied") || strings.Contains(value, "access denied")
}

func dockerProblem(command CommandResult, err error) Problem {
	var executableError *exec.Error
	if errors.As(err, &executableError) || errors.Is(err, exec.ErrNotFound) {
		return Problem{Kind: "precondition", Msg: "找不到可用的 Docker CLI"}
	}
	if isPermissionError(command.Stderr) {
		return Problem{Kind: "permission", Msg: "当前用户无权访问 Docker daemon"}
	}
	if strings.Contains(command.Stderr, localContextRequired) {
		return Problem{Kind: "precondition", Msg: localContextRequired}
	}
	return Problem{Kind: "network", Msg: "Docker daemon 不可达"}
}

func supportedPlatform() bool {
	if runtime.GOARCH != "amd64" && runtime.GOARCH != "arm64" {
		return false
	}
	return runtime.GOOS == "darwin" || runtime.GOOS == "linux" || runtime.GOOS == "windows"
}

func nearestExistingDir(path string) (string, error) {
	current := filepath.Clean(path)
	for {
		info, err := os.Stat(current)
		if err == nil {
			if !info.IsDir() {
				return "", errors.New("上级路径不是目录")
			}
			return current, nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", os.ErrNotExist
		}
		current = parent
	}
}

func portAvailable(port int) (bool, error) {
	if port <= 0 || port > 65535 {
		return false, errors.New("端口无效")
	}
	listener, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		if errors.Is(err, syscall.EADDRINUSE) {
			return false, nil
		}
		return false, err
	}
	return true, listener.Close()
}

func (d Diagnostics) probeHTTP(ctx context.Context, record Record) (map[string]any, error) {
	client := d.HTTP
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Second}
	}
	endpoint := record.Endpoint
	if endpoint == "" {
		endpoint = fmt.Sprintf("http://127.0.0.1:%d", record.Port)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(endpoint, "/")+"/api/v1/system/info", nil)
	if err != nil {
		return map[string]any{"reachable": false, "endpoint": endpoint}, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return map[string]any{"reachable": false, "endpoint": endpoint}, err
	}
	defer resp.Body.Close()
	data := map[string]any{"reachable": true, "http_status": resp.StatusCode, "endpoint": endpoint}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return data, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return data, nil
}

func inferStatus(record Record, containers []Container, health map[string]any, healthErr error) string {
	if len(containers) == 0 {
		return "stopped"
	}
	services := make(map[string]Container, len(containers))
	for _, container := range containers {
		key := container.Service
		if key == "" {
			key = container.Name
		}
		services[key] = container
	}
	required := requiredServices(record)
	missing := 0
	for _, service := range required {
		if _, ok := services[service]; !ok {
			missing++
		}
	}
	running, transitional, stopped, unhealthy, observed := 0, 0, 0, 0, 0
	for _, service := range required {
		container, exists := services[service]
		if !exists {
			continue
		}
		observed++
		state := strings.ToLower(container.State)
		containerHealth := strings.ToLower(container.Health)
		switch {
		case strings.Contains(state, "running") || strings.Contains(state, "up"):
			if strings.Contains(containerHealth, "unhealthy") {
				unhealthy++
			} else if strings.Contains(containerHealth, "starting") {
				transitional++
			} else {
				running++
			}
		case strings.Contains(state, "created"), strings.Contains(state, "restarting"), strings.Contains(state, "starting"):
			transitional++
		default:
			stopped++
		}
	}
	if unhealthy > 0 || (stopped > 0 && (running > 0 || transitional > 0)) {
		return "degraded"
	}
	if transitional > 0 {
		return "starting"
	}
	if missing == 0 && running == len(required) {
		if healthErr == nil && health["reachable"] == true {
			return "running"
		}
		return "degraded"
	}
	if running == 0 && (observed == 0 || stopped == observed) {
		return "stopped"
	}
	return "degraded"
}

func requiredServices(record Record) []string {
	services := []string{"langbot", "langbot_plugin_runtime"}
	profile := strings.ToLower(strings.TrimSpace(record.Profile))
	if profile == "all" || profile == "box" {
		services = append(services, "langbot_box")
	}
	return services
}

func (d Diagnostics) portCheckForRecord(ctx context.Context, record Record, raw string) (string, string) {
	containers, err := ParseContainers(raw)
	if err == nil {
		health, healthErr := d.probeHTTP(ctx, record)
		if healthErr == nil && health["reachable"] == true && serviceRunning(containers, "langbot") {
			return "ok", "默认端口由受管部署使用"
		}
	}
	available, err := portAvailable(record.Port)
	if err != nil {
		return "warn", "默认端口检查失败"
	}
	if !available {
		return "warn", "默认端口已被其他进程占用"
	}
	return "ok", "默认端口可用"
}

func serviceRunning(containers []Container, wanted string) bool {
	for _, container := range containers {
		service := container.Service
		if service == "" {
			service = container.Name
		}
		if service == wanted && (strings.Contains(strings.ToLower(container.State), "running") || strings.Contains(strings.ToLower(container.State), "up")) {
			return true
		}
	}
	return false
}

func ParseContainers(raw string) ([]Container, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return []Container{}, nil
	}
	var rows []map[string]any
	if strings.HasPrefix(raw, "[") {
		if err := json.Unmarshal([]byte(raw), &rows); err != nil {
			return nil, err
		}
	} else {
		for _, line := range strings.Split(raw, "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			var row map[string]any
			if err := json.Unmarshal([]byte(line), &row); err != nil {
				return nil, err
			}
			rows = append(rows, row)
		}
	}
	containers := make([]Container, 0, len(rows))
	for _, row := range rows {
		containers = append(containers, Container{Name: stringValue(row, "Name", "name"), Service: stringValue(row, "Service", "service"), State: stringValue(row, "State", "state"), Health: stringValue(row, "Health", "health")})
	}
	return containers, nil
}

func stringValue(row map[string]any, keys ...string) string {
	for _, key := range keys {
		if value, ok := row[key].(string); ok {
			return value
		}
	}
	return ""
}
