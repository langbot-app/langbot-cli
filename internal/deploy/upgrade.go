package deploy

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
)

const upgradeStateFile = "upgrade-state.yaml"

type UpgradeState struct {
	SchemaVersion    int         `json:"schema_version" yaml:"schema_version"`
	DeploymentID     string      `json:"deployment_id" yaml:"deployment_id"`
	SourceVersion    string      `json:"source_version" yaml:"source_version"`
	TargetVersion    string      `json:"target_version" yaml:"target_version"`
	SourceImage      string      `json:"source_image" yaml:"source_image"`
	TargetImage      string      `json:"target_image" yaml:"target_image"`
	SourceCompose    string      `json:"source_compose_digest" yaml:"source_compose_digest"`
	TargetCompose    string      `json:"target_compose_digest" yaml:"target_compose_digest"`
	TargetDigest     string      `json:"target_image_digest,omitempty" yaml:"target_image_digest,omitempty"`
	Profile          string      `json:"profile" yaml:"profile"`
	Database         string      `json:"database" yaml:"database"`
	DataDir          string      `json:"data_dir" yaml:"data_dir"`
	BackupDir        string      `json:"backup_dir,omitempty" yaml:"backup_dir,omitempty"`
	DiskFreeBytes    uint64      `json:"disk_free_bytes" yaml:"disk_free_bytes"`
	DataBytes        int64       `json:"data_bytes" yaml:"data_bytes"`
	BeforeStatus     string      `json:"before_status" yaml:"before_status"`
	BeforeContainers []Container `json:"before_containers,omitempty" yaml:"before_containers,omitempty"`
	Phase            string      `json:"phase" yaml:"phase"`
	FailureStep      string      `json:"failure_step,omitempty" yaml:"failure_step,omitempty"`
	UpdatedAt        time.Time   `json:"updated_at" yaml:"updated_at"`
}

type BackupFile struct {
	Path   string `json:"path" yaml:"path"`
	Size   int64  `json:"size" yaml:"size"`
	SHA256 string `json:"sha256" yaml:"sha256"`
}

type BackupManifest struct {
	SourceVersion string       `json:"source_version" yaml:"source_version"`
	ComposeDigest string       `json:"compose_digest" yaml:"compose_digest"`
	Files         []BackupFile `json:"files" yaml:"files"`
}

func CompareReleaseVersions(current, target string) (int, error) {
	if !releaseVersionPattern.MatchString(current) || !releaseVersionPattern.MatchString(target) {
		return 0, errors.New("当前版本和目标版本都必须是 vX.Y.Z 稳定 Release tag")
	}
	left, right := strings.Split(current[1:], "."), strings.Split(target[1:], ".")
	for i := range left {
		a, aErr := strconv.ParseUint(left[i], 10, 64)
		b, bErr := strconv.ParseUint(right[i], 10, 64)
		if aErr != nil || bErr != nil {
			return 0, errors.New("Release tag 版本号超出支持范围")
		}
		if a < b {
			return -1, nil
		}
		if a > b {
			return 1, nil
		}
	}
	return 0, nil
}

func ComposeForUpgrade(record Record, dir, version string) ([]byte, string, error) {
	if record.Driver != DriverDockerCompose || record.ComposeProject != DefaultComposeProject ||
		record.ComposeFile != filepath.Join(dir, "compose.yaml") || record.EnvFile != filepath.Join(dir, ".env") ||
		record.DataDir != filepath.Join(dir, "data") || record.Asset.Name != DefaultImageRepository+":"+record.CoreVersion ||
		record.Endpoint != fmt.Sprintf("http://127.0.0.1:%d", record.Port) ||
		!regularFile(record.EnvFile) || !releaseVersionPattern.MatchString(record.CoreVersion) {
		return nil, "", errors.New("仅支持 lbctl 安装的默认 Compose 部署自动升级")
	}
	if record.Profile != "basic" && record.Profile != "all" {
		return nil, "", errors.New("部署 profile 不支持自动升级")
	}
	current := []byte(renderCompose(record.Asset.Name, record.Port, filepath.Join(dir, "data", "box")))
	actual, err := os.ReadFile(record.ComposeFile)
	if err != nil || !equalDigest(actual, current) {
		return nil, "", errors.New("Compose 资产已变更，拒绝自动升级")
	}
	if !releaseVersionPattern.MatchString(version) {
		return nil, "", errors.New("版本必须为 vX.Y.Z 形式的稳定 Release tag")
	}
	image := DefaultImageRepository + ":" + version
	updated := []byte(renderCompose(image, record.Port, filepath.Join(dir, "data", "box")))
	return updated, fileDigest(updated), nil
}

func InspectUpgradeData(record Record) (int64, uint64, error) {
	configPath := filepath.Join(record.DataDir, "config.yaml")
	if !regularFile(configPath) {
		return 0, 0, errors.New("缺少可读取的本地配置，无法确认备份范围")
	}
	data, err := os.ReadFile(configPath)
	if err != nil {
		return 0, 0, err
	}
	var config struct {
		Database struct {
			Use    string `yaml:"use"`
			SQLite struct {
				Path string `yaml:"path"`
			} `yaml:"sqlite"`
		} `yaml:"database"`
		VDB struct {
			Use string `yaml:"use"`
		} `yaml:"vdb"`
		Storage struct {
			Use string `yaml:"use"`
		} `yaml:"storage"`
	}
	if err := yaml.Unmarshal(data, &config); err != nil {
		return 0, 0, errors.New("无法解析本地配置")
	}
	if config.Database.Use != "sqlite" || config.VDB.Use != "chroma" || config.Storage.Use != "local" ||
		(config.Database.SQLite.Path != "data/langbot.db" && config.Database.SQLite.Path != "./data/langbot.db") {
		return 0, 0, errors.New("当前持久化配置不在可完整备份的默认本地模式内")
	}
	var total int64
	err = filepath.WalkDir(record.DataDir, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 || (!info.IsDir() && !info.Mode().IsRegular()) {
			return fmt.Errorf("数据目录包含不支持的路径: %s", path)
		}
		if info.Mode().IsRegular() {
			total += info.Size()
		}
		return nil
	})
	if err != nil {
		return 0, 0, err
	}
	free, err := availableBytes(record.DataDir)
	if err != nil {
		return 0, 0, err
	}
	if total < 0 || free < uint64(total)+1<<30 {
		return total, free, errors.New("备份所需空间不足，至少额外保留 1 GiB")
	}
	return total, free, nil
}

func ReadUpgradeState(dir string) (UpgradeState, error) {
	path := filepath.Join(dir, upgradeStateFile)
	info, err := os.Lstat(path)
	if err != nil {
		return UpgradeState{}, err
	}
	if !info.Mode().IsRegular() {
		return UpgradeState{}, ErrCorrupt
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return UpgradeState{}, err
	}
	var state UpgradeState
	if err := yaml.Unmarshal(data, &state); err != nil || state.SchemaVersion != 1 || state.DeploymentID == "" || state.Phase == "" {
		return UpgradeState{}, ErrCorrupt
	}
	return state, nil
}

func WriteUpgradeState(dir string, state UpgradeState) error {
	if state.DeploymentID == "" || state.Phase == "" {
		return ErrCorrupt
	}
	state.SchemaVersion = 1
	state.UpdatedAt = time.Now().UTC()
	data, err := yaml.Marshal(state)
	if err != nil {
		return err
	}
	return writeAtomic(filepath.Join(dir, upgradeStateFile), data, 0o600)
}

func WriteUpgradeCompose(record Record, content []byte) error {
	if !regularFile(record.ComposeFile) {
		return ErrNotManaged
	}
	return writeAtomic(record.ComposeFile, content, 0o600)
}

func CreateUpgradeBackup(ctx context.Context, record Record, state UpgradeState) (string, error) {
	root := filepath.Join(filepath.Dir(record.DataDir), "backups")
	if err := os.MkdirAll(root, 0o700); err != nil {
		return "", err
	}
	if info, err := os.Lstat(root); err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", ErrNotManaged
	}
	tmp, err := os.MkdirTemp(root, ".upgrade-*")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(tmp)
	if err := os.Chmod(tmp, 0o700); err != nil {
		return "", err
	}
	manifest := BackupManifest{SourceVersion: state.SourceVersion, ComposeDigest: state.SourceCompose, Files: []BackupFile{}}
	for _, source := range []struct{ path, dest string }{{record.DataDir, "data"}, {record.ComposeFile, "compose.yaml"}, {record.EnvFile, ".env"}, {filepath.Join(filepath.Dir(record.DataDir), recordFileName), recordFileName}} {
		err := filepath.WalkDir(source.path, func(path string, entry fs.DirEntry, walkErr error) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			if walkErr != nil {
				return walkErr
			}
			rel, err := filepath.Rel(source.path, path)
			if err != nil {
				return err
			}
			name := filepath.Join(source.dest, rel)
			info, err := os.Lstat(path)
			if err != nil {
				return err
			}
			if info.Mode()&os.ModeSymlink != 0 || (!info.IsDir() && !info.Mode().IsRegular()) {
				return errors.New("备份源包含不支持的文件类型")
			}
			dest := filepath.Join(tmp, name)
			if info.IsDir() {
				return os.MkdirAll(dest, 0o700)
			}
			if err := os.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
				return err
			}
			size, digest, err := copyAndHash(ctx, path, dest)
			if err != nil {
				return err
			}
			manifest.Files = append(manifest.Files, BackupFile{Path: name, Size: size, SHA256: digest})
			return nil
		})
		if err != nil {
			return "", err
		}
	}
	encoded, err := yaml.Marshal(manifest)
	if err != nil {
		return "", err
	}
	if err := writeAtomic(filepath.Join(tmp, "manifest.yaml"), encoded, 0o600); err != nil {
		return "", err
	}
	if err := verifyUpgradeBackup(ctx, tmp); err != nil {
		return "", err
	}
	final := filepath.Join(root, "upgrade-"+time.Now().UTC().Format("20060102T150405.000000000"))
	if err := os.Rename(tmp, final); err != nil {
		return "", err
	}
	return final, nil
}

func VerifyUpgradeBackup(dir string) error {
	return verifyUpgradeBackup(context.Background(), dir)
}

func verifyUpgradeBackup(ctx context.Context, dir string) error {
	data, err := os.ReadFile(filepath.Join(dir, "manifest.yaml"))
	if err != nil {
		return err
	}
	var manifest BackupManifest
	if err := yaml.Unmarshal(data, &manifest); err != nil || len(manifest.Files) == 0 {
		return ErrCorrupt
	}
	for _, file := range manifest.Files {
		if err := ctx.Err(); err != nil {
			return err
		}
		clean := filepath.Clean(file.Path)
		if filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
			return ErrCorrupt
		}
		path := filepath.Join(dir, clean)
		if !regularFile(path) {
			return ErrCorrupt
		}
		info, err := os.Stat(path)
		if err != nil || info.Size() != file.Size || fileDigestPathContext(ctx, path) != file.SHA256 {
			return ErrCorrupt
		}
	}
	return nil
}

func copyAndHash(ctx context.Context, source, destination string) (int64, string, error) {
	in, err := os.Open(source)
	if err != nil {
		return 0, "", err
	}
	defer in.Close()
	out, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return 0, "", err
	}
	defer out.Close()
	hash := sha256.New()
	size, err := io.Copy(io.MultiWriter(out, hash), contextReader{ctx: ctx, Reader: in})
	if err != nil {
		return 0, "", err
	}
	if err := out.Sync(); err != nil {
		return 0, "", err
	}
	return size, "sha256:" + hex.EncodeToString(hash.Sum(nil)), nil
}

func fileDigestPath(path string) string {
	return fileDigestPathContext(context.Background(), path)
}

func fileDigestPathContext(ctx context.Context, path string) string {
	file, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, contextReader{ctx: ctx, Reader: file}); err != nil {
		return ""
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil))
}

type contextReader struct {
	ctx context.Context
	io.Reader
}

func (r contextReader) Read(data []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.Reader.Read(data)
}

func fileDigest(data []byte) string {
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func equalDigest(left, right []byte) bool { return fileDigest(left) == fileDigest(right) }

func CheckUpgradeInterrupted(dir string) error {
	state, err := ReadUpgradeState(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if state.Phase != "completed" {
		return errors.New("存在未完成的升级，请先运行 status 和 doctor 诊断")
	}
	return nil
}
