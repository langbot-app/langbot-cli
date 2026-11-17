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

	"github.com/langbot-app/langbot-cli/internal/clientidentity"
	"github.com/langbot-app/langbot-cli/internal/i18n"
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
	Upgrade          *UpgradeState  `json:"upgrade,omitempty" yaml:"upgrade,omitempty"`
}

type Diagnostics struct {
	Region string
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
		return result, Problem{Kind: "precondition", Msg: i18n.T("Target directory is not managed by lbctl")}
	}
	if errors.Is(err, ErrCorrupt) {
		result.Evidence["record"] = "corrupt"
		return result, Problem{Kind: "precondition", Msg: i18n.T("Local deployment record is corrupt")}
	}
	if err != nil {
		result.Evidence["record"] = "unreadable"
		return result, Problem{Kind: "precondition", Msg: i18n.T("Unable to read local deployment record")}
	}
	result.Record = record
	if record.Driver == DriverNative {
		result.Status = "stopped"
		result.Evidence["service_manager"] = record.ServiceManager
		if NativeRunning(d.Dir) {
			result.Status = "running"
			if err := NativeHealth(ctx, record, d.HTTP); err != nil {
				result.Status = "degraded"
				result.Evidence["health"] = err.Error()
			}
		}
		return result, nil
	}
	if upgrade, upgradeErr := ReadUpgradeState(d.Dir); upgradeErr == nil {
		if upgrade.DeploymentID != record.DeploymentID {
			return result, Problem{Kind: "precondition", Msg: i18n.T("Upgrade record does not match the deployment")}
		}
		if upgrade.Phase == "completed" && upgrade.TargetVersion != record.CoreVersion {
			return result, Problem{Kind: "precondition", Msg: i18n.T("Completed upgrade record does not match Core version")}
		}
		result.Upgrade = &upgrade
	} else if !errors.Is(upgradeErr, os.ErrNotExist) {
		return result, Problem{Kind: "precondition", Msg: i18n.T("Upgrade record is corrupt or unreadable")}
	}
	result.RequiredServices = requiredServices(record)
	result.Evidence["record"] = "valid"
	if !regularFile(record.ComposeFile) {
		result.Evidence["compose_asset"] = "missing_or_invalid"
		return result, Problem{Kind: "precondition", Msg: i18n.T("Compose deployment file does not exist or is unavailable")}
	}
	result.Evidence["compose_asset"] = "valid"
	if d.Runner == nil {
		return result, Problem{Kind: "internal", Msg: i18n.T("Local Docker runner is not configured")}
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
		return result, Problem{Kind: "incompatible", Msg: i18n.T("Unable to read Compose service status")}
	}
	containers, err := ParseContainers(command.Stdout)
	if err != nil {
		result.Evidence["compose"] = "invalid"
		return result, Problem{Kind: "incompatible", Msg: i18n.T("Invalid Compose service status format")}
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
		return result, Problem{Kind: "network", Msg: i18n.T("Unable to confirm local deployment status")}
	}
	return result, nil
}

func (d Diagnostics) Doctor(ctx context.Context) ([]Check, error) {
	if record, err := d.Store.Read(); err == nil && record.Driver == DriverNative {
		checks := NativeCapabilities()
		status, err := d.Status(ctx)
		checks = append(checks, Check{Name: "native_service", Status: status.Status, Reason: record.ServiceManager})
		if record.ServiceManager == "systemd-user" {
			checks = append(checks, Check{Name: "linger", Status: "info", Reason: "Run loginctl enable-linger to keep LangBot running after logout"})
		}
		return checks, err
	}
	checks := make([]Check, 0, 12)
	imageRecord, _ := d.Store.Read()
	if imageRecord.Region == "" {
		region, err := ResolveRegion(ctx, d.Region, d.HTTP)
		if err != nil {
			return nil, err
		}
		imageRecord.Region = region
	}
	tag := imageRecord.CoreVersion
	if tag == "" {
		tag = "latest"
	}
	repositories := append([]string{DefaultImageRepository}, PublicImageRepositories()...)
	repositories = append(repositories, CanonicalImageRepository)
	probes := (ImageSources{HTTP: d.HTTP}).ProbeAll(ctx, repositories, tag)
	selected := CanonicalImageRepository
	bestLatency := int64(1<<63 - 1)
	for _, probe := range probes {
		status := "warn"
		if probe.Available {
			status = "ok"
			if probe.Repository != DefaultImageRepository && probe.Repository != CanonicalImageRepository && probe.LatencyMS < bestLatency {
				selected, bestLatency = probe.Repository, probe.LatencyMS
			}
		}
		checks = append(checks, Check{Name: probe.Repository, Status: status, Reason: fmt.Sprintf(i18n.T("%s (%d ms)"), probe.Reason, probe.LatencyMS), Detail: probe})
	}
	if imageRecord.Region != "cn" && probes[0].Available {
		selected = DefaultImageRepository
	}
	for _, probe := range probes {
		if probe.Repository == imageRecord.ImageRepository && probe.Available {
			selected = imageRecord.ImageRepository
		}
	}
	if ExplicitImageRepository(imageRecord) {
		selected = ImageRepository(imageRecord)
	}
	checks = append(checks, Check{Name: i18n.T("Docker image repositories"), Status: "info", Reason: fmt.Sprintf(i18n.T("Selected image repository: %s"), selected), Detail: map[string]any{"selected": selected, "probes": probes}})
	add := func(name, status, reason string, detail any) {
		checks = append(checks, Check{Name: name, Status: status, Reason: reason, Detail: detail})
	}
	if d.Runner == nil {
		return nil, Problem{Kind: "internal", Msg: i18n.T("Local Docker runner is not configured")}
	}
	if _, err := d.Runner.Docker(ctx, "--version"); err != nil {
		add("docker_cli", "error", i18n.T("No usable Docker CLI found"), nil)
	} else {
		add("docker_cli", "ok", i18n.T("Docker CLI is available"), nil)
	}
	daemonResult, daemonErr := d.Runner.Docker(ctx, "info", "--format", "{{.ServerVersion}}")
	if daemonErr != nil {
		if strings.Contains(daemonResult.Stderr, i18n.T(localContextRequired)) {
			add("docker_daemon", "error", i18n.T("Current Docker context points to a remote daemon"), nil)
			add("permission", "warn", i18n.T("Local Docker daemon was not accessed; unable to verify user permissions"), nil)
		} else if isPermissionError(daemonResult.Stderr) {
			add("docker_daemon", "error", i18n.T("Current user cannot access Docker daemon"), nil)
			add("permission", "error", i18n.T("Docker daemon permission check failed"), nil)
		} else {
			add("docker_daemon", "error", i18n.T("Docker daemon is unreachable"), nil)
			add("permission", "warn", i18n.T("Docker daemon is unreachable; cannot verify current user permissions"), nil)
		}
	} else {
		add("docker_daemon", "ok", i18n.T("Docker daemon is available"), nil)
		if currentUserIsRoot() {
			add("permission", "warn", i18n.T("Current process runs as root"), nil)
		} else {
			add("permission", "ok", i18n.T("Current user can access Docker daemon"), nil)
		}
	}
	compose, composeErr := d.Runner.Docker(ctx, "compose", "version", "--short")
	if composeErr != nil || !isComposeV2(compose.Stdout) {
		add("compose_v2", "error", i18n.T("Docker Compose v2 or newer is required"), strings.TrimSpace(compose.Stdout))
	} else {
		add("compose_v2", "ok", i18n.T("Docker Compose plugin is available"), strings.TrimSpace(compose.Stdout))
	}
	if supportedPlatform() {
		add("platform", "ok", i18n.T("Current platform supports read-only preflight"), CurrentPlatform())
	} else {
		add("platform", "error", i18n.T("Current platform is not supported by lbctl"), CurrentPlatform())
	}

	diskPath := d.Dir
	if stat, err := os.Stat(d.Dir); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			ancestor, ancestorErr := nearestExistingDir(d.Dir)
			if ancestorErr == nil {
				diskPath = ancestor
				add("data_dir", "ok", i18n.T("Deployment directory not created yet; existing parent found"), map[string]any{"dir": d.Dir, "ancestor": ancestor})
			} else {
				add("data_dir", "error", i18n.T("Unable to access deployment parent directory"), d.Dir)
			}
		} else {
			add("data_dir", "error", i18n.T("Deployment directory inaccessible"), d.Dir)
		}
	} else if !stat.IsDir() {
		add("data_dir", "error", i18n.T("Deployment path is not a directory"), d.Dir)
	} else {
		add("data_dir", "ok", i18n.T("Deployment directory accessible"), d.Dir)
	}
	if free, err := availableBytes(diskPath); err != nil {
		add("disk_space", "warn", i18n.T("Unable to read available disk space"), nil)
	} else {
		status, reason := "ok", i18n.T("Available disk space is sufficient")
		if free < 1<<30 {
			status, reason = "warn", i18n.T("Available disk space is below 1 GiB")
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
		add("deployment_record", "warn", i18n.T("No managed deployment records found"), nil)
	case errors.Is(recordErr, ErrNotManaged):
		if asset := findComposeAsset(d.Dir); asset != "" {
			add("deployment_record", "warn", i18n.T("Found an unmanaged Compose directory; read-only diagnostics only, adoption requires an explicit action"), map[string]any{"status": "unmanaged", "compose_file": asset})
		} else {
			add("deployment_record", "warn", i18n.T("Target directory is not an lbctl-managed deployment"), map[string]any{"status": "unmanaged"})
		}
	case recordErr != nil:
		add("deployment_record", "error", i18n.T("Deployment record corrupt or version incompatible"), nil)
	default:
		add("deployment_record", "ok", i18n.T("Deployment record valid"), record)
	}
	if recordErr == nil {
		if upgrade, upgradeErr := ReadUpgradeState(d.Dir); upgradeErr == nil {
			if upgrade.DeploymentID != record.DeploymentID || upgrade.Phase != "completed" || upgrade.TargetVersion != record.CoreVersion {
				add("upgrade", "error", i18n.T("An upgrade is unfinished; inspect recovery state manually"), upgrade)
			} else {
				add("upgrade", "ok", i18n.T("Previous upgrade completed"), upgrade)
			}
		} else if !errors.Is(upgradeErr, os.ErrNotExist) {
			add("upgrade", "error", i18n.T("Upgrade record is corrupt or unreadable"), nil)
		}
		if !regularFile(record.ComposeFile) {
			add("compose_assets", "error", i18n.T("Compose file does not exist"), record.ComposeFile)
		} else {
			add("compose_assets", "ok", i18n.T("Compose file is accessible"), record.ComposeFile)
		}
		composeResult, err := d.Runner.Compose(ctx, record, "ps", "--format", "json")
		if err != nil {
			add("container_visibility", "error", i18n.T("Unable to read managed container status"), nil)
		} else {
			add("container_visibility", "ok", i18n.T("Managed container status is readable"), nil)
			portStatus, portReason := d.portCheckForRecord(ctx, record, composeResult.Stdout)
			add("default_port", portStatus, portReason, port)
		}
	} else {
		if available, err := portAvailable(port); err != nil {
			add("default_port", "warn", i18n.T("Default port check failed"), port)
		} else if !available {
			add("default_port", "warn", i18n.T("Default port in use"), port)
		} else {
			add("default_port", "ok", i18n.T("Default port available"), port)
		}
		if asset := findComposeAsset(d.Dir); asset != "" {
			add("compose_assets", "warn", i18n.T("Found unmanaged Compose assets; read-only identification only"), asset)
			add("container_visibility", "warn", i18n.T("Unmanaged deployment has no Compose project binding; containers not read"), nil)
		} else {
			add("compose_assets", "warn", i18n.T("Not installed; no Compose assets"), nil)
			add("container_visibility", "warn", i18n.T("Not installed; no managed containers"), nil)
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
		return checks, Problem{Kind: kind, Msg: i18n.T("Local preflight has errors")}
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
		return Problem{Kind: "precondition", Msg: i18n.T("No usable Docker CLI found")}
	}
	if isPermissionError(command.Stderr) {
		return Problem{Kind: "permission", Msg: i18n.T("Current user cannot access Docker daemon")}
	}
	if strings.Contains(command.Stderr, i18n.T(localContextRequired)) {
		return Problem{Kind: "precondition", Msg: i18n.T(localContextRequired)}
	}
	return Problem{Kind: "network", Msg: i18n.T("Docker daemon is unreachable")}
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
				return "", errors.New(i18n.T("Parent path is not a directory"))
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
		return false, errors.New(i18n.T("Invalid port"))
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
	clientidentity.Apply(req)
	resp, err := client.Do(req)
	if err != nil {
		return map[string]any{"reachable": false, "endpoint": endpoint}, err
	}
	defer resp.Body.Close()
	data := map[string]any{"reachable": true, "http_status": resp.StatusCode, "endpoint": endpoint}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return data, fmt.Errorf(i18n.T("HTTP %d"), resp.StatusCode)
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
			return "ok", i18n.T("Default port used by managed deployment")
		}
	}
	available, err := portAvailable(record.Port)
	if err != nil {
		return "warn", i18n.T("Default port check failed")
	}
	if !available {
		return "warn", i18n.T("Default port occupied by another process")
	}
	return "ok", i18n.T("Default port available")
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
