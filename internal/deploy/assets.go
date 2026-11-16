package deploy

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/gofrs/flock"
	"github.com/langbot-app/langbot-cli/internal/i18n"
)

const (
	DefaultImageRepository = "rockchin/langbot"
	DefaultComposeProject  = "langbot-lbctl"
)

var releaseVersionPattern = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+$`)

type InstallAssets struct {
	Dir         string
	Version     string
	Profile     string
	Image       string
	Port        int
	Project     string
	Ports       InstallPorts
	ComposeFile string
	EnvFile     string
	DataDir     string
	Compose     []byte
	Environment []byte
	Digest      string
}

func PreflightInstall(ctx context.Context, assets InstallAssets, runner Runner) ([]Check, error) {
	dir := assets.Dir
	ports, err := assets.Ports.hostPorts(assets.Port)
	if err != nil || !ValidComposeProject(assets.Project) {
		return nil, Problem{Kind: "input", Msg: i18n.T("Invalid installation port mapping or Compose project")}
	}
	checks := make([]Check, 0, 6)
	add := func(name, status, reason string, detail any) {
		checks = append(checks, Check{Name: name, Status: status, Reason: reason, Detail: detail})
	}
	if runner == nil {
		return checks, Problem{Kind: "internal", Msg: i18n.T("Local Docker runner is not configured")}
	}
	if _, err := runner.Docker(ctx, "--version"); err != nil {
		add("docker_cli", "error", i18n.T("No usable Docker CLI found"), nil)
	} else {
		add("docker_cli", "ok", i18n.T("Docker CLI is available"), nil)
	}
	if result, err := runner.Docker(ctx, "info", "--format", "{{.ServerVersion}}"); err != nil {
		problem := dockerProblem(result, err)
		add("docker_daemon", "error", problem.Msg, nil)
	} else {
		add("docker_daemon", "ok", i18n.T("Docker daemon is available"), nil)
	}
	if result, err := runner.Docker(ctx, "compose", "version", "--short"); err != nil || !isComposeV2(result.Stdout) {
		add("compose_v2", "error", i18n.T("Docker Compose v2 or newer is required"), strings.TrimSpace(result.Stdout))
	} else {
		add("compose_v2", "ok", i18n.T("Docker Compose plugin is available"), strings.TrimSpace(result.Stdout))
	}
	if occupied, err := InstallProjectOccupied(ctx, runner, assets.Project); err != nil {
		add("compose_project", "error", i18n.T("Unable to inspect managed Compose project"), assets.Project)
	} else if occupied {
		add("compose_project", "error", i18n.T("Managed Compose project already exists"), assets.Project)
	} else {
		add("compose_project", "ok", i18n.T("Managed Compose project is not in use"), assets.Project)
	}
	if err := ValidateInstallTarget(dir, true); err != nil {
		add("install_dir", "error", i18n.T("Target directory is not empty or unavailable"), dir)
	} else {
		add("install_dir", "ok", i18n.T("Target directory available"), dir)
	}
	diskPath := dir
	if _, err := os.Stat(diskPath); err != nil {
		if ancestor, ancestorErr := nearestExistingDir(dir); ancestorErr == nil {
			diskPath = ancestor
		}
	}
	if free, err := availableBytes(diskPath); err != nil {
		add("disk_space", "error", i18n.T("Unable to read available disk space"), nil)
	} else if free < 2<<30 {
		add("disk_space", "error", i18n.T("Available disk space is below 2 GiB"), free)
	} else {
		add("disk_space", "ok", i18n.T("Available disk space is sufficient"), free)
	}
	for _, candidate := range ports {
		if available, err := portAvailable(candidate); err != nil {
			add("port", "error", i18n.T("Unable to check port"), candidate)
		} else if !available {
			add("port", "error", i18n.T("Required installation ports are in use"), candidate)
		} else {
			add("port", "ok", i18n.T("Port available"), candidate)
		}
	}
	for _, check := range checks {
		if check.Status == "error" {
			return checks, Problem{Kind: "precondition", Msg: i18n.T("Installation preflight failed")}
		}
	}
	return checks, nil
}

func InstallProjectOccupied(ctx context.Context, runner Runner, project string) (bool, error) {
	if !ValidComposeProject(project) {
		return false, errors.New(i18n.T("Invalid Compose project name"))
	}
	filter := "label=com.docker.compose.project=" + project
	for _, args := range [][]string{
		{"ps", "-a", "--filter", filter, "--format", "{{.ID}}"},
		{"network", "ls", "--filter", filter, "--format", "{{.ID}}"},
	} {
		found, err := runner.Docker(ctx, args...)
		if err != nil {
			return false, err
		}
		if strings.TrimSpace(found.Stdout) != "" {
			return true, nil
		}
	}
	return false, nil
}

func NewInstallAssets(dir, version, profile string, port int, network InstallNetworkOptions) (InstallAssets, error) {
	resolved, err := ResolveDir(dir)
	if err != nil {
		return InstallAssets{}, err
	}
	version = strings.TrimSpace(version)
	if !releaseVersionPattern.MatchString(version) {
		return InstallAssets{}, errors.New(i18n.T("Version must be a stable Release tag in vX.Y.Z format"))
	}
	profile = strings.ToLower(strings.TrimSpace(profile))
	if profile == "" {
		profile = "basic"
	}
	if profile != "basic" && profile != "all" {
		return InstallAssets{}, errors.New(i18n.T("profile only supports basic or all"))
	}
	if port == 0 {
		port = defaultPort
	}
	if port < 1 || port > 65535 {
		return InstallAssets{}, errors.New(i18n.T("Port must be between 1 and 65535"))
	}
	project, ports, err := resolveInstallNetwork(port, network)
	if err != nil {
		return InstallAssets{}, err
	}
	image := DefaultImageRepository + ":" + version
	compose := []byte(renderCompose(image, port, filepath.Join(resolved, "data", "box"), ports))
	compose = composeAutostart(compose, new(true))
	sum := sha256.Sum256(compose)
	pluginToken, err := randomToken()
	if err != nil {
		return InstallAssets{}, err
	}
	boxToken, err := randomToken()
	if err != nil {
		return InstallAssets{}, err
	}
	boxEnabled := "false"
	if profile == "all" {
		boxEnabled = "true"
	}
	environment := deployEnvironment([]byte(fmt.Sprintf("LANGBOT_PLUGIN_RUNTIME_CONTROL_TOKEN=%s\nLANGBOT_BOX_CONTROL_TOKEN=%s\nLANGBOT_BOX_ENABLED=%s\n", pluginToken, boxToken, boxEnabled)), "dev")
	return InstallAssets{
		Dir: resolved, Version: version, Profile: profile, Image: image, Port: port, Project: project, Ports: ports,
		ComposeFile: filepath.Join(resolved, "compose.yaml"), EnvFile: filepath.Join(resolved, ".env"), DataDir: filepath.Join(resolved, "data"),
		Compose: compose, Environment: environment, Digest: "sha256:" + hex.EncodeToString(sum[:]),
	}, nil
}

func (a InstallAssets) Record(deploymentID, imageDigest string) Record {
	ports := a.Ports
	ports.AdapterPorts = append([]string{}, ports.AdapterPorts...)
	return Record{
		Autostart:    new(true),
		DeploymentID: deploymentID, Driver: DriverDockerCompose, CoreVersion: a.Version, Profile: a.Profile,
		ComposeProject: a.Project, ComposeFile: a.ComposeFile, EnvFile: a.EnvFile, DataDir: a.DataDir,
		Endpoint: fmt.Sprintf("http://127.0.0.1:%d", a.Port), Port: a.Port,
		Asset:        AssetInfo{Name: a.Image, Digest: imageDigest},
		InstallPorts: &ports,
	}
}

func (a *InstallAssets) SetImageRepository(repository string) error {
	if !regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._:/-]*$`).MatchString(repository) {
		return errors.New(i18n.T("invalid image repository"))
	}
	a.Image = repository + ":" + a.Version
	a.Compose = []byte(renderCompose(a.Image, a.Port, filepath.Join(a.Dir, "data", "box"), a.Ports))
	a.Compose = composeAutostart(a.Compose, new(true))
	a.Digest = fileDigest(a.Compose)
	return nil
}

func (a *InstallAssets) SetPackageIndex(index string) {
	a.Compose = composePackageIndex(a.Compose, index)
	a.Digest = fileDigest(a.Compose)
}

func composePackageIndex(content []byte, index string) []byte {
	if index == "" {
		return content
	}
	line := "      LANGBOT_PLUGIN_RUNTIME_CONTROL_TOKEN: ${LANGBOT_PLUGIN_RUNTIME_CONTROL_TOKEN}\n"
	return []byte(strings.ReplaceAll(string(content), line, line+"      LANGBOT_PLUGIN_PYPI_INDEX_URL: "+strconv.Quote(index)+"\n"))
}

func (a InstallAssets) Write() error {
	if err := os.MkdirAll(a.DataDir, 0o700); err != nil {
		return err
	}
	if err := writeAtomic(a.ComposeFile, a.Compose, 0o600); err != nil {
		return err
	}
	return writeAtomic(a.EnvFile, a.Environment, 0o600)
}

func ValidateInstallTarget(dir string, allowOperationLock bool) error {
	info, err := os.Lstat(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return ErrNotManaged
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if allowOperationLock && entry.Name() == ".operation.lock" {
			continue
		}
		return ErrNotManaged
	}
	return nil
}

func WithMutationLock(ctx context.Context, dir string, fn func() error) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if info, err := os.Lstat(dir); err == nil && (info.Mode()&os.ModeSymlink != 0 || !info.IsDir()) {
		return ErrNotManaged
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	lock := flock.New(filepath.Join(dir, ".operation.lock"))
	lockCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	locked, err := lock.TryLockContext(lockCtx, 25*time.Millisecond)
	if err != nil || !locked {
		if err == nil {
			err = context.DeadlineExceeded
		}
		return fmt.Errorf(i18n.T("Failed to acquire local deployment lock: %w"), err)
	}
	defer func() { _ = lock.Unlock() }()
	return fn()
}

func NewDeploymentID() (string, error) {
	value := make([]byte, 8)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return "local-" + hex.EncodeToString(value), nil
}

func randomToken() (string, error) {
	value := make([]byte, 32)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return hex.EncodeToString(value), nil
}

func renderCompose(image string, port int, boxRoot string, ports InstallPorts) string {
	quotedBoxRoot := strconv.Quote(boxRoot)
	quotedBoxMount := strconv.Quote(boxRoot + ":" + boxRoot)
	debugPublication, debugEnvironment := "", ""
	if ports.PluginDebugPort > 0 {
		debugPublication = fmt.Sprintf("    ports:\n      - \"%d:5401\"\n", ports.PluginDebugPort)
	}
	if ports.PluginDebugPort != 5401 {
		debugURL := ""
		if ports.PluginDebugPort > 0 {
			debugURL = fmt.Sprintf("ws://localhost:%d/plugin/debug/ws", ports.PluginDebugPort)
		}
		debugEnvironment = "      PLUGIN__DISPLAY_PLUGIN_DEBUG_URL: " + strconv.Quote(debugURL) + "\n"
	}
	adapterPublications := ""
	for _, mapping := range ports.AdapterPorts {
		adapterPublications += "      - " + strconv.Quote(mapping) + "\n"
	}
	return fmt.Sprintf(`services:
  langbot_plugin_runtime:
    image: %s
    volumes:
      - ./data/plugins:/app/data/plugins
%s    restart: on-failure
    environment:
      TZ: Asia/Shanghai
      LANGBOT_PLUGIN_RUNTIME_CONTROL_TOKEN: ${LANGBOT_PLUGIN_RUNTIME_CONTROL_TOKEN}
    command: ["uv", "run", "--no-sync", "-m", "langbot_plugin.cli.__init__", "rt"]
    networks: [langbot_network]

  langbot_box:
    image: %s
    profiles: ["all"]
    volumes:
      - %s
      - /var/run/docker.sock:/var/run/docker.sock
    restart: on-failure
    environment:
      TZ: Asia/Shanghai
      LANGBOT_BOX_CONTROL_TOKEN: ${LANGBOT_BOX_CONTROL_TOKEN}
    command: ["uv", "run", "--no-sync", "-m", "langbot_plugin.cli.__init__", "box"]
    networks: [langbot_network]

  langbot:
    image: %s
    volumes:
      - ./data:/app/data
    restart: on-failure
    environment:
      TZ: Asia/Shanghai
      LANGBOT_DEPLOY_SOURCE: ${LANGBOT_DEPLOY_SOURCE}
      LANGBOT_DEPLOY_TOOL_VERSION: ${LANGBOT_DEPLOY_TOOL_VERSION}
      LANGBOT_PLUGIN_RUNTIME_CONTROL_TOKEN: ${LANGBOT_PLUGIN_RUNTIME_CONTROL_TOKEN}
      LANGBOT_BOX_CONTROL_TOKEN: ${LANGBOT_BOX_CONTROL_TOKEN}
%s      BOX__ENABLED: ${LANGBOT_BOX_ENABLED}
      BOX__LOCAL__HOST_ROOT: %s
      BOX__LOCAL__DEFAULT_WORKSPACE: default
      BOX__LOCAL__SKILLS_ROOT: skills
      BOX__LOCAL__ALLOWED_MOUNT_ROOTS: %s
    ports:
      - "%d:5300"
%s    networks: [langbot_network]

networks:
  langbot_network:
    driver: bridge
`, image, debugPublication, image, quotedBoxMount, image, debugEnvironment, quotedBoxRoot, quotedBoxRoot, port, adapterPublications)
}
