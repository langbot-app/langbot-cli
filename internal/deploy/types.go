package deploy

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/gofrs/flock"
	"go.yaml.in/yaml/v3"
)

const (
	RecordSchemaVersion  = 1
	DriverDockerCompose  = "docker_compose"
	DriverNativeProcess  = "native_process"
	localContextRequired = "当前 Docker context 指向远程 daemon；本地生命周期只允许本机 daemon"
	managedMarker        = "lbctl-managed"
	markerFileName       = ".lbctl-managed"
	recordFileName       = "deployment.yaml"
	defaultPort          = 5300
)

var (
	ErrNotInstalled = errors.New("本地部署尚未安装")
	ErrNotManaged   = errors.New("目标目录不是 lbctl 管理的部署")
	ErrCorrupt      = errors.New("本地部署记录损坏")
)

// Record 是本地部署的唯一持久化描述，不保存 Workspace API Key。
type Record struct {
	SchemaVersion  int       `yaml:"schema_version" json:"schema_version"`
	ManagedBy      string    `yaml:"managed_by" json:"managed_by"`
	DeploymentID   string    `yaml:"deployment_id" json:"deployment_id"`
	InstanceUUID   string    `yaml:"instance_uuid,omitempty" json:"instance_uuid,omitempty"`
	Driver         string    `yaml:"driver" json:"driver"`
	CoreVersion    string    `yaml:"core_version" json:"core_version"`
	Profile        string    `yaml:"profile" json:"profile"`
	ComposeProject string    `yaml:"compose_project" json:"compose_project"`
	ComposeFile    string    `yaml:"compose_file" json:"compose_file"`
	DataDir        string    `yaml:"data_dir" json:"data_dir"`
	Endpoint       string    `yaml:"endpoint" json:"endpoint"`
	Port           int       `yaml:"port" json:"port"`
	Asset          AssetInfo `yaml:"asset" json:"asset"`
	UpdatedAt      time.Time `yaml:"updated_at" json:"updated_at"`
}

type AssetInfo struct {
	Name   string `yaml:"name" json:"name"`
	Digest string `yaml:"digest" json:"digest"`
}

type Check struct {
	Name   string `json:"name" yaml:"name"`
	Status string `json:"status" yaml:"status"`
	Reason string `json:"reason" yaml:"reason"`
	Detail any    `json:"detail,omitempty" yaml:"detail,omitempty"`
}

type Container struct {
	Name    string `json:"name" yaml:"name"`
	Service string `json:"service" yaml:"service"`
	State   string `json:"state" yaml:"state"`
	Health  string `json:"health,omitempty" yaml:"health,omitempty"`
}

type CommandResult struct {
	Stdout   string
	Stderr   string
	ExitCode int
}

// Runner 只允许调用固定的 docker/docker compose 可执行文件。
type Runner interface {
	Docker(context.Context, ...string) (CommandResult, error)
	Compose(context.Context, Record, ...string) (CommandResult, error)
}

type ExecFunc func(context.Context, string, ...string) (CommandResult, error)

type DockerRunner struct {
	Exec ExecFunc
}

func (r DockerRunner) Docker(ctx context.Context, args ...string) (CommandResult, error) {
	if len(args) == 0 {
		return CommandResult{}, errors.New("docker 命令不能为空")
	}
	if len(args) == 1 && args[0] == "--version" {
		return r.exec(ctx, "docker", "--version")
	}
	contextName, result, err := r.localContext(ctx)
	if err != nil {
		return result, err
	}
	return r.exec(ctx, append([]string{"docker", "--context", contextName}, args...)...)
}

func (r DockerRunner) Compose(ctx context.Context, record Record, args ...string) (CommandResult, error) {
	if len(args) == 0 {
		return CommandResult{}, errors.New("docker compose 命令不能为空")
	}
	contextName, result, err := r.localContext(ctx)
	if err != nil {
		return result, err
	}
	base := []string{"docker", "--context", contextName, "compose"}
	if record.ComposeProject != "" {
		base = append(base, "--project-name", record.ComposeProject)
	}
	if record.ComposeFile != "" {
		base = append(base, "--file", record.ComposeFile)
	}
	return r.exec(ctx, append(base, args...)...)
}

func (r DockerRunner) localContext(ctx context.Context) (string, CommandResult, error) {
	shown, err := r.exec(ctx, "docker", "context", "show")
	if err != nil {
		return "", shown, err
	}
	name := strings.TrimSpace(shown.Stdout)
	if name == "" {
		return "", shown, errors.New("Docker context 为空")
	}
	inspected, err := r.exec(ctx, "docker", "context", "inspect", name, "--format", "{{.Endpoints.docker.Host}}")
	if err != nil {
		return "", inspected, err
	}
	host := strings.TrimSpace(inspected.Stdout)
	if !isLocalDockerEndpoint(host) {
		inspected.Stderr = localContextRequired
		return "", inspected, errors.New(inspected.Stderr)
	}
	return name, CommandResult{}, nil
}

func isLocalDockerEndpoint(endpoint string) bool {
	endpoint = strings.ToLower(strings.TrimSpace(endpoint))
	return strings.HasPrefix(endpoint, "unix://") || strings.HasPrefix(endpoint, "npipe://")
}

func (r DockerRunner) exec(ctx context.Context, args ...string) (CommandResult, error) {
	if len(args) == 0 {
		return CommandResult{}, errors.New("docker 命令不能为空")
	}
	if r.Exec != nil {
		return r.Exec(ctx, args[0], args[1:]...)
	}
	command := exec.CommandContext(ctx, args[0], args[1:]...)
	command.Env = localDockerEnvironment()
	var stdout, stderr strings.Builder
	command.Stdout, command.Stderr = &stdout, &stderr
	err := command.Run()
	result := CommandResult{Stdout: stdout.String(), Stderr: stderr.String()}
	if err == nil {
		return result, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		result.ExitCode = exitErr.ExitCode()
	}
	return result, err
}

func localDockerEnvironment() []string {
	env := os.Environ()
	filtered := make([]string, 0, len(env))
	for _, item := range env {
		name := item
		if index := strings.IndexByte(item, '='); index >= 0 {
			name = item[:index]
		}
		switch strings.ToUpper(name) {
		case "DOCKER_HOST", "DOCKER_CONTEXT", "DOCKER_TLS_VERIFY", "DOCKER_CERT_PATH":
			continue
		}
		filtered = append(filtered, item)
	}
	return filtered
}

type Store struct {
	Dir string
	Now func() time.Time
}

func DefaultDir() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil || strings.TrimSpace(base) == "" {
		return "", errors.New("无法确定用户数据目录")
	}
	return filepath.Join(base, "langbot", "deployment"), nil
}

func ResolveDir(value string) (string, error) {
	if strings.TrimSpace(value) == "" {
		return DefaultDir()
	}
	path, err := filepath.Abs(filepath.Clean(value))
	if err != nil {
		return "", fmt.Errorf("解析部署目录失败: %w", err)
	}
	return filepath.Clean(path), nil
}

func (s Store) Read() (Record, error) {
	if strings.TrimSpace(s.Dir) == "" {
		return Record{}, errors.New("部署目录不能为空")
	}
	info, err := os.Lstat(s.Dir)
	if errors.Is(err, os.ErrNotExist) {
		return Record{}, ErrNotInstalled
	} else if err != nil {
		return Record{}, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return Record{}, ErrNotManaged
	}
	markerPath := filepath.Join(s.Dir, markerFileName)
	markerInfo, err := os.Lstat(markerPath)
	if err == nil && markerInfo.Mode()&os.ModeSymlink != 0 {
		return Record{}, ErrNotManaged
	}
	marker, err := os.ReadFile(markerPath)
	if errors.Is(err, os.ErrNotExist) {
		return Record{}, ErrNotManaged
	}
	if err != nil || strings.TrimSpace(string(marker)) != managedMarker {
		return Record{}, ErrNotManaged
	}
	recordPath := filepath.Join(s.Dir, recordFileName)
	recordInfo, err := os.Lstat(recordPath)
	if err == nil && recordInfo.Mode()&os.ModeSymlink != 0 {
		return Record{}, ErrCorrupt
	}
	data, err := os.ReadFile(recordPath)
	if errors.Is(err, os.ErrNotExist) {
		return Record{}, ErrCorrupt
	}
	if err != nil {
		return Record{}, err
	}
	var record Record
	if err := yaml.Unmarshal(data, &record); err != nil || record.SchemaVersion != RecordSchemaVersion || record.ManagedBy != managedMarker {
		return Record{}, ErrCorrupt
	}
	if record.Port == 0 {
		record.Port = defaultPort
	}
	if record.Endpoint == "" {
		record.Endpoint = fmt.Sprintf("http://127.0.0.1:%d", record.Port)
	}
	if record.Driver == "" {
		record.Driver = DriverDockerCompose
	}
	if record.Driver != DriverDockerCompose && record.Driver != DriverNativeProcess {
		return Record{}, ErrCorrupt
	}
	return record, nil
}

func (s Store) Write(ctx context.Context, record Record) error {
	if strings.TrimSpace(s.Dir) == "" {
		return errors.New("部署目录不能为空")
	}
	if record.SchemaVersion == 0 {
		record.SchemaVersion = RecordSchemaVersion
	}
	if record.ManagedBy == "" {
		record.ManagedBy = managedMarker
	}
	if record.SchemaVersion != RecordSchemaVersion || record.ManagedBy != managedMarker {
		return ErrCorrupt
	}
	if record.Port == 0 {
		record.Port = defaultPort
	}
	if record.Endpoint == "" {
		record.Endpoint = fmt.Sprintf("http://127.0.0.1:%d", record.Port)
	}
	if record.Driver == "" {
		record.Driver = DriverDockerCompose
	}
	if record.Driver != DriverDockerCompose && record.Driver != DriverNativeProcess {
		return ErrCorrupt
	}
	now := time.Now
	if s.Now != nil {
		now = s.Now
	}
	record.UpdatedAt = now().UTC()
	if info, err := os.Lstat(s.Dir); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return ErrNotManaged
	}
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return err
	}
	markerPath := filepath.Join(s.Dir, markerFileName)
	if markerInfo, err := os.Lstat(markerPath); err == nil {
		if markerInfo.Mode()&os.ModeSymlink != 0 {
			return ErrNotManaged
		}
		marker, readErr := os.ReadFile(markerPath)
		if readErr != nil || strings.TrimSpace(string(marker)) != managedMarker {
			return ErrNotManaged
		}
	} else if errors.Is(err, os.ErrNotExist) {
		entries, readErr := os.ReadDir(s.Dir)
		if readErr != nil {
			return readErr
		}
		if hasUnexpectedEntries(entries) {
			return ErrNotManaged
		}
	} else {
		return err
	}
	lock := flock.New(filepath.Join(s.Dir, ".lock"))
	if ctx == nil {
		ctx = context.Background()
	}
	lockCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	locked, err := lock.TryLockContext(lockCtx, 25*time.Millisecond)
	if err != nil || !locked {
		if err == nil {
			err = context.DeadlineExceeded
		}
		return fmt.Errorf("等待部署记录锁失败: %w", err)
	}
	defer func() { _ = lock.Unlock() }()
	if markerInfo, err := os.Lstat(markerPath); err == nil {
		if markerInfo.Mode()&os.ModeSymlink != 0 {
			return ErrNotManaged
		}
		marker, readErr := os.ReadFile(markerPath)
		if readErr != nil || strings.TrimSpace(string(marker)) != managedMarker {
			return ErrNotManaged
		}
	} else if errors.Is(err, os.ErrNotExist) {
		entries, readErr := os.ReadDir(s.Dir)
		if readErr != nil || hasUnexpectedEntries(entries) {
			if readErr != nil {
				return readErr
			}
			return ErrNotManaged
		}
	} else {
		return err
	}
	if err := os.WriteFile(markerPath, []byte(managedMarker+"\n"), 0o600); err != nil {
		return err
	}
	data, err := yaml.Marshal(record)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(s.Dir, ".deployment-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, filepath.Join(s.Dir, recordFileName)); err != nil {
		return err
	}
	if dirFile, err := os.Open(s.Dir); err == nil {
		_ = dirFile.Sync()
		_ = dirFile.Close()
	}
	return nil
}

func hasUnexpectedEntries(entries []os.DirEntry) bool {
	for _, entry := range entries {
		if entry.Name() != ".lock" {
			return true
		}
	}
	return false
}

func CurrentPlatform() string { return runtime.GOOS + "/" + runtime.GOARCH }
