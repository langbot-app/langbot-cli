package deploy

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

func PurgeData(dir string, record Record) (bool, string, error) {
	current, err := validatedPurgeRecord(dir, record)
	if err != nil {
		return false, "", err
	}
	dataDir := filepath.Clean(current.DataDir)
	info, err := os.Lstat(dataDir)
	if errors.Is(err, os.ErrNotExist) {
		return false, "", nil
	}
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return false, "", errors.New("数据目录在删除前发生变化")
	}
	quarantine := filepath.Join(dir, ".purge-data-"+current.DeploymentID)
	if _, err := os.Lstat(quarantine); !errors.Is(err, os.ErrNotExist) {
		return false, quarantine, errors.New("发现未完成的数据清理现场")
	}
	if err := os.Rename(dataDir, quarantine); err != nil {
		return false, "", err
	}
	if err := os.RemoveAll(quarantine); err != nil {
		return true, quarantine, err
	}
	return true, "", nil
}

func ValidatePurgeData(dir string, record Record) error {
	_, err := validatedPurgeRecord(dir, record)
	return err
}

func validatedPurgeRecord(dir string, record Record) (Record, error) {
	current, err := (Store{Dir: dir}).Read()
	if err != nil || !samePurgeTarget(current, record) {
		return Record{}, errors.New("部署记录已变更或受管标记无效")
	}
	if err := ValidateOwnedLayout(current, dir); err != nil {
		return Record{}, errors.New("只有 lbctl 安装的默认部署可以自动清理数据")
	}
	dataDir := filepath.Clean(current.DataDir)
	if err := validateRemovalBoundary(dir, dataDir); err != nil {
		return Record{}, err
	}
	info, err := os.Lstat(dataDir)
	if errors.Is(err, os.ErrNotExist) {
		return current, nil
	}
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return Record{}, errors.New("数据目录不存在、不是目录或为符号链接")
	}
	if err := filepath.WalkDir(dataDir, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return errors.New("数据目录包含符号链接，拒绝自动清理")
		}
		return nil
	}); err != nil {
		return Record{}, err
	}
	quarantine := filepath.Join(dir, ".purge-data-"+current.DeploymentID)
	if _, err := os.Lstat(quarantine); !errors.Is(err, os.ErrNotExist) {
		return Record{}, errors.New("发现未完成的数据清理现场")
	}
	return current, nil
}

func samePurgeTarget(current, requested Record) bool {
	return current.DeploymentID == requested.DeploymentID && current.InstanceUUID == requested.InstanceUUID &&
		current.Driver == requested.Driver && current.CoreVersion == requested.CoreVersion && current.Profile == requested.Profile &&
		current.ComposeProject == requested.ComposeProject && samePath(current.ComposeFile, requested.ComposeFile) &&
		samePath(current.EnvFile, requested.EnvFile) && samePath(current.DataDir, requested.DataDir) &&
		current.Endpoint == requested.Endpoint && current.Port == requested.Port && current.Asset == requested.Asset
}

func validateRemovalBoundary(dir, target string) error {
	dir, err := filepath.Abs(filepath.Clean(dir))
	if err != nil {
		return errors.New("部署目录无效")
	}
	target, err = filepath.Abs(filepath.Clean(target))
	if err != nil {
		return errors.New("数据目录无效")
	}
	home, _ := os.UserHomeDir()
	if filepath.Dir(target) == target || (home != "" && samePath(target, home)) {
		return errors.New("拒绝删除文件系统根目录或用户主目录")
	}
	if samePath(target, dir) || !samePath(target, filepath.Join(dir, "data")) {
		return errors.New("数据目录不在受管部署的固定边界内")
	}
	rel, err := filepath.Rel(dir, target)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return errors.New("数据目录逃逸受管部署边界")
	}
	return nil
}

func samePath(left, right string) bool {
	return filepath.Clean(left) == filepath.Clean(right)
}
