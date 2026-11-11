package deploy

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gofrs/flock"
	"go.yaml.in/yaml/v3"
)

const locatorSchemaVersion = 2

type locatorRecord struct {
	SchemaVersion int      `yaml:"schema_version"`
	Dir           string   `yaml:"dir"`
	Dirs          []string `yaml:"dirs,omitempty"`
}

type Locator struct {
	Path string
}

func (l Locator) WithLock(ctx context.Context, fn func() error) error {
	path := l.Path
	if strings.TrimSpace(path) == "" {
		var err error
		path, err = DefaultLocatorPath()
		if err != nil {
			return err
		}
	}
	parent := filepath.Dir(path)
	if info, err := os.Lstat(parent); err == nil && (info.Mode()&os.ModeSymlink != 0 || !info.IsDir()) {
		return errors.New("本机部署定位目录不可用")
	}
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return err
	}
	lock := flock.New(path + ".lock")
	lockCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	locked, err := lock.TryLockContext(lockCtx, 25*time.Millisecond)
	if err != nil || !locked {
		if err == nil {
			err = context.DeadlineExceeded
		}
		return fmt.Errorf("等待本机部署定位锁失败: %w", err)
	}
	defer func() { _ = lock.Unlock() }()
	return fn()
}

func DefaultLocatorPath() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil || strings.TrimSpace(base) == "" {
		return "", errors.New("无法确定用户配置目录")
	}
	return filepath.Join(base, "langbot", "deployment-location.yaml"), nil
}

func (l Locator) Resolve(override string) (string, error) {
	if strings.TrimSpace(override) != "" {
		return ResolveDir(override)
	}
	record, err := l.read()
	if err != nil {
		return "", err
	}
	return filepath.Clean(record.Dir), nil
}

// Directories includes the legacy/default directory without changing the registry.
func (l Locator) Directories() ([]string, error) {
	record, err := l.read()
	if err != nil {
		return nil, err
	}
	if len(record.Dirs) == 0 {
		return []string{record.Dir}, nil
	}
	return record.Dirs, nil
}

// RegisteredDirectories excludes the implicit default when no locator exists.
func (l Locator) RegisteredDirectories() ([]string, error) {
	record, err := l.read()
	return record.Dirs, err
}

func (l Locator) read() (locatorRecord, error) {
	path := l.Path
	if strings.TrimSpace(path) == "" {
		var err error
		path, err = DefaultLocatorPath()
		if err != nil {
			return locatorRecord{}, err
		}
	}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		dir, err := DefaultDir()
		return locatorRecord{SchemaVersion: locatorSchemaVersion, Dir: dir}, err
	}
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return locatorRecord{}, errors.New("本机部署定位记录不可用")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return locatorRecord{}, errors.New("无法读取本机部署定位记录")
	}
	var record locatorRecord
	if err := yaml.Unmarshal(data, &record); err != nil || (record.SchemaVersion != 1 && record.SchemaVersion != locatorSchemaVersion) || !filepath.IsAbs(record.Dir) {
		return locatorRecord{}, errors.New("本机部署定位记录损坏")
	}
	record.Dir = filepath.Clean(record.Dir)
	dirs := []string{record.Dir}
	for _, dir := range record.Dirs {
		if !filepath.IsAbs(dir) {
			return locatorRecord{}, errors.New("本机部署定位记录损坏")
		}
		dirs = appendUniqueDir(dirs, filepath.Clean(dir))
	}
	record.Dirs = dirs
	return record, nil
}

// Write registers a deployment, preserving the legacy fallback. The caller
// must hold WithLock when performing a read/modify/write operation.
func (l Locator) Write(dir string) error {
	resolved, err := ResolveDir(dir)
	if err != nil {
		return err
	}
	path := l.Path
	if strings.TrimSpace(path) == "" {
		path, err = DefaultLocatorPath()
		if err != nil {
			return err
		}
	}
	parent := filepath.Dir(path)
	if info, statErr := os.Lstat(parent); statErr == nil && (info.Mode()&os.ModeSymlink != 0 || !info.IsDir()) {
		return errors.New("本机部署定位目录不可用")
	}
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return err
	}
	if info, statErr := os.Lstat(path); statErr == nil && info.Mode()&os.ModeSymlink != 0 {
		return errors.New("本机部署定位记录不能是符号链接")
	}
	record, err := l.read()
	if err != nil {
		return err
	}
	dirs := record.Dirs
	if len(dirs) == 0 {
		if _, readErr := (Store{Dir: record.Dir}).Read(); readErr == nil {
			dirs = append(dirs, record.Dir)
		}
	}
	if len(dirs) == 0 {
		record.Dir = resolved
	}
	record.SchemaVersion = locatorSchemaVersion
	record.Dirs = appendUniqueDir(dirs, resolved)
	data, err := yaml.Marshal(record)
	if err != nil {
		return err
	}
	return writeAtomic(path, data, 0o600)
}

func appendUniqueDir(dirs []string, dir string) []string {
	for _, existing := range dirs {
		if existing == dir {
			return dirs
		}
	}
	return append(dirs, dir)
}

func writeAtomic(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".lbctl-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(mode); err != nil {
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
	if err := os.Rename(tmpName, path); err != nil {
		return err
	}
	if dirFile, err := os.Open(dir); err == nil {
		_ = dirFile.Sync()
		_ = dirFile.Close()
	}
	return nil
}
