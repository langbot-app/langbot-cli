package config

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/gofrs/flock"
	"github.com/langbot-app/langbot-cli/internal/endpoint"
	"go.yaml.in/yaml/v3"
)

var namePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
var envPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func DefaultPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		return "", inputError("无法确定配置目录")
	}
	return filepath.Join(home, ".config", "langbot", "config.yaml"), nil
}

func (s Store) path() (string, error) {
	if strings.TrimSpace(s.Path) != "" {
		return filepath.Clean(s.Path), nil
	}
	return DefaultPath()
}

func (s Store) Load() (File, error) {
	path, err := s.path()
	if err != nil {
		return File{}, err
	}
	if err := rejectSymlink(path); err != nil {
		return File{}, err
	}
	data, err := readData(path)
	if errors.Is(err, os.ErrNotExist) {
		return emptyFile(), nil
	}
	if err != nil {
		return File{}, inputError("读取配置文件失败")
	}
	return decode(data)
}

func (s Store) Update(ctx context.Context, mutate func(*File) error) error {
	if mutate == nil {
		return inputError("配置更新函数不能为空")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	path, err := s.path()
	if err != nil {
		return err
	}
	if err := rejectSymlink(path); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return inputError("创建配置目录失败")
	}

	lock := flock.New(path + ".lock")
	lockCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	locked, err := lock.TryLockContext(lockCtx, lockRetryPeriod)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return networkError("等待配置锁超时或已取消")
		}
		return inputError("获取配置锁失败")
	}
	if !locked {
		return networkError("等待配置锁超时")
	}
	defer func() { _ = lock.Unlock() }()
	if err := rejectSymlink(path); err != nil {
		return err
	}

	file, err := readUnlocked(path)
	if err != nil {
		return err
	}
	if err := mutate(&file); err != nil {
		return err
	}
	if err := validateFile(file); err != nil {
		return err
	}
	data, err := encode(file)
	if err != nil {
		return err
	}
	if err := atomicWrite(path, data); err != nil {
		return inputError("写入配置文件失败")
	}
	return nil
}

func readUnlocked(path string) (File, error) {
	data, err := readData(path)
	if errors.Is(err, os.ErrNotExist) {
		return emptyFile(), nil
	}
	if err != nil {
		return File{}, inputError("读取配置文件失败")
	}
	return decode(data)
}

func readData(path string) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxConfigBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxConfigBytes {
		return nil, inputError("配置文件过大")
	}
	return data, nil
}

func decode(data []byte) (File, error) {
	if len(data) > maxConfigBytes {
		return File{}, inputError("配置文件过大")
	}
	var file File
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(&file); err != nil {
		return File{}, inputError("配置文件格式无效")
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return File{}, inputError("配置文件只能包含一个 YAML 文档")
	}
	if err := validateFile(file); err != nil {
		return File{}, err
	}
	return file, nil
}

func encode(file File) ([]byte, error) {
	data, err := yaml.Marshal(file)
	if err != nil {
		return nil, inputError("序列化配置失败")
	}
	if len(data) > maxConfigBytes {
		return nil, inputError("配置文件过大")
	}
	return data, nil
}

func validateFile(file File) error {
	if file.Version != currentVersion {
		return inputError("不支持的配置版本")
	}
	if file.Contexts == nil {
		return inputError("contexts 必须是对象")
	}
	if file.CurrentContext != "" {
		if _, ok := file.Contexts[file.CurrentContext]; !ok {
			return inputError("current_context 不存在")
		}
	}
	for name, item := range file.Contexts {
		if !namePattern.MatchString(name) {
			return inputError("context 名称无效")
		}
		if strings.TrimSpace(item.Endpoint) == "" {
			return inputError("context endpoint 不能为空")
		}
		if _, err := endpoint.Normalize(item.Endpoint); err != nil {
			return inputError("context endpoint 无效")
		}
		if item.Credential != nil {
			if item.Credential.Type != "env" || !envPattern.MatchString(item.Credential.Name) {
				return inputError("context credential 必须是有效的 env 引用")
			}
		}
		if item.Timeout != "" {
			d, err := time.ParseDuration(item.Timeout)
			if err != nil || d <= 0 {
				return inputError("context timeout 无效")
			}
		}
		if strings.ContainsAny(item.ExpectedWorkspaceUUID, "\r\n") {
			return inputError("expected_workspace_uuid 无效")
		}
	}
	return nil
}

func atomicWrite(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".config-*")
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
	if err := os.Rename(tmpName, path); err != nil {
		return err
	}
	if dirFile, err := os.Open(dir); err == nil {
		_ = dirFile.Sync()
		_ = dirFile.Close()
	}
	return nil
}

func rejectSymlink(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return inputError("检查配置文件失败")
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return inputError("不支持符号链接配置文件")
	}
	if !info.Mode().IsRegular() {
		return inputError("配置路径不是普通文件")
	}
	return nil
}
