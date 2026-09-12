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
	ComposeFile string
	EnvFile     string
	DataDir     string
	Compose     []byte
	Environment []byte
	Digest      string
}

func PreflightInstall(ctx context.Context, dir string, port int, runner Runner) ([]Check, error) {
	checks := make([]Check, 0, 6)
	add := func(name, status, reason string, detail any) {
		checks = append(checks, Check{Name: name, Status: status, Reason: reason, Detail: detail})
	}
	if runner == nil {
		return checks, Problem{Kind: "internal", Msg: "本地 Docker runner 未配置"}
	}
	if _, err := runner.Docker(ctx, "--version"); err != nil {
		add("docker_cli", "error", "找不到可用的 Docker CLI", nil)
	} else {
		add("docker_cli", "ok", "Docker CLI 可用", nil)
	}
	if result, err := runner.Docker(ctx, "info", "--format", "{{.ServerVersion}}"); err != nil {
		problem := dockerProblem(result, err)
		add("docker_daemon", "error", problem.Msg, nil)
	} else {
		add("docker_daemon", "ok", "Docker daemon 可用", nil)
	}
	if result, err := runner.Docker(ctx, "compose", "version", "--short"); err != nil || !isComposeV2(result.Stdout) {
		add("compose_v2", "error", "需要 Docker Compose v2 或更高版本", strings.TrimSpace(result.Stdout))
	} else {
		add("compose_v2", "ok", "Docker Compose 插件可用", strings.TrimSpace(result.Stdout))
	}
	if occupied, err := InstallProjectOccupied(ctx, runner); err != nil {
		add("compose_project", "error", "无法检查受管 Compose project", DefaultComposeProject)
	} else if occupied {
		add("compose_project", "error", "受管 Compose project 已存在", DefaultComposeProject)
	} else {
		add("compose_project", "ok", "受管 Compose project 未被占用", DefaultComposeProject)
	}
	if err := ValidateInstallTarget(dir, true); err != nil {
		add("install_dir", "error", "目标目录不为空或不可用", dir)
	} else {
		add("install_dir", "ok", "目标目录可用", dir)
	}
	diskPath := dir
	if _, err := os.Stat(diskPath); err != nil {
		if ancestor, ancestorErr := nearestExistingDir(dir); ancestorErr == nil {
			diskPath = ancestor
		}
	}
	if free, err := availableBytes(diskPath); err != nil {
		add("disk_space", "error", "无法读取可用磁盘空间", nil)
	} else if free < 2<<30 {
		add("disk_space", "error", "可用磁盘空间低于 2 GiB", free)
	} else {
		add("disk_space", "ok", "可用磁盘空间充足", free)
	}
	ports := []int{port, 5401, 2280, 2281, 2282, 2283, 2284, 2285}
	checked := make(map[int]bool, len(ports))
	for _, candidate := range ports {
		if checked[candidate] {
			continue
		}
		checked[candidate] = true
		if available, err := portAvailable(candidate); err != nil {
			add("port", "error", "无法检查端口", candidate)
		} else if !available {
			add("port", "error", "安装所需端口已被占用", candidate)
		} else {
			add("port", "ok", "端口可用", candidate)
		}
	}
	for _, check := range checks {
		if check.Status == "error" {
			return checks, Problem{Kind: "precondition", Msg: "安装前置检查未通过"}
		}
	}
	return checks, nil
}

func InstallProjectOccupied(ctx context.Context, runner Runner) (bool, error) {
	filter := "label=com.docker.compose.project=" + DefaultComposeProject
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

func NewInstallAssets(dir, version, profile string, port int) (InstallAssets, error) {
	resolved, err := ResolveDir(dir)
	if err != nil {
		return InstallAssets{}, err
	}
	version = strings.TrimSpace(version)
	if !releaseVersionPattern.MatchString(version) {
		return InstallAssets{}, errors.New("版本必须为 vX.Y.Z 形式的稳定 Release tag")
	}
	profile = strings.ToLower(strings.TrimSpace(profile))
	if profile == "" {
		profile = "basic"
	}
	if profile != "basic" && profile != "all" {
		return InstallAssets{}, errors.New("profile 只支持 basic 或 all")
	}
	if port == 0 {
		port = defaultPort
	}
	if port < 1 || port > 65535 {
		return InstallAssets{}, errors.New("端口必须在 1-65535 之间")
	}
	if port == 5401 || (port >= 2280 && port <= 2285) {
		return InstallAssets{}, errors.New("端口与 LangBot 运行时必需端口冲突")
	}
	image := DefaultImageRepository + ":" + version
	compose := []byte(renderCompose(image, port, filepath.Join(resolved, "data", "box")))
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
	environment := []byte(fmt.Sprintf("LANGBOT_PLUGIN_RUNTIME_CONTROL_TOKEN=%s\nLANGBOT_BOX_CONTROL_TOKEN=%s\nLANGBOT_BOX_ENABLED=%s\n", pluginToken, boxToken, boxEnabled))
	return InstallAssets{
		Dir: resolved, Version: version, Profile: profile, Image: image, Port: port, Project: DefaultComposeProject,
		ComposeFile: filepath.Join(resolved, "compose.yaml"), EnvFile: filepath.Join(resolved, ".env"), DataDir: filepath.Join(resolved, "data"),
		Compose: compose, Environment: environment, Digest: "sha256:" + hex.EncodeToString(sum[:]),
	}, nil
}

func (a InstallAssets) Record(deploymentID, imageDigest string) Record {
	return Record{
		DeploymentID: deploymentID, Driver: DriverDockerCompose, CoreVersion: a.Version, Profile: a.Profile,
		ComposeProject: a.Project, ComposeFile: a.ComposeFile, EnvFile: a.EnvFile, DataDir: a.DataDir,
		Endpoint: fmt.Sprintf("http://127.0.0.1:%d", a.Port), Port: a.Port,
		Asset: AssetInfo{Name: a.Image, Digest: imageDigest},
	}
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
		return fmt.Errorf("等待本地部署锁失败: %w", err)
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

func renderCompose(image string, port int, boxRoot string) string {
	quotedBoxRoot := strconv.Quote(boxRoot)
	quotedBoxMount := strconv.Quote(boxRoot + ":" + boxRoot)
	return fmt.Sprintf(`services:
  langbot_plugin_runtime:
    image: %s
    volumes:
      - ./data/plugins:/app/data/plugins
    ports:
      - "5401:5401"
    restart: on-failure
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
      LANGBOT_PLUGIN_RUNTIME_CONTROL_TOKEN: ${LANGBOT_PLUGIN_RUNTIME_CONTROL_TOKEN}
      LANGBOT_BOX_CONTROL_TOKEN: ${LANGBOT_BOX_CONTROL_TOKEN}
      BOX__ENABLED: ${LANGBOT_BOX_ENABLED}
      BOX__LOCAL__HOST_ROOT: %s
      BOX__LOCAL__DEFAULT_WORKSPACE: default
      BOX__LOCAL__SKILLS_ROOT: skills
      BOX__LOCAL__ALLOWED_MOUNT_ROOTS: %s
    ports:
      - "%d:5300"
      - "2280-2285:2280-2285"
    networks: [langbot_network]

networks:
  langbot_network:
    driver: bridge
`, image, image, quotedBoxMount, image, quotedBoxRoot, quotedBoxRoot, port)
}
