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
	"github.com/langbot-app/langbot-cli/internal/i18n"
	"go.yaml.in/yaml/v3"
)

var namePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
var envPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func DefaultPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		return "", inputError(i18n.T("Unable to determine configuration directory"))
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
		return File{}, inputError(i18n.T("Failed to read configuration file"))
	}
	return decode(data)
}

func (s Store) Update(ctx context.Context, mutate func(*File) error) error {
	if mutate == nil {
		return inputError(i18n.T("Configuration update function must not be nil"))
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
		return inputError(i18n.T("Failed to create configuration directory"))
	}

	lock := flock.New(path + ".lock")
	lockCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	locked, err := lock.TryLockContext(lockCtx, lockRetryPeriod)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return networkError(i18n.T("Configuration lock wait timed out or was cancelled"))
		}
		return inputError(i18n.T("Failed to acquire configuration lock"))
	}
	if !locked {
		return networkError(i18n.T("Timed out waiting for configuration lock"))
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
		return inputError(i18n.T("Failed to write configuration file"))
	}
	return nil
}

func readUnlocked(path string) (File, error) {
	data, err := readData(path)
	if errors.Is(err, os.ErrNotExist) {
		return emptyFile(), nil
	}
	if err != nil {
		return File{}, inputError(i18n.T("Failed to read configuration file"))
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
		return nil, inputError(i18n.T("Configuration file is too large"))
	}
	return data, nil
}

func decode(data []byte) (File, error) {
	if len(data) > maxConfigBytes {
		return File{}, inputError(i18n.T("Configuration file is too large"))
	}
	var file File
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(&file); err != nil {
		return File{}, inputError(i18n.T("Invalid configuration file format"))
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return File{}, inputError(i18n.T("Configuration file must contain only one YAML document"))
	}
	if err := validateFile(file); err != nil {
		return File{}, err
	}
	return file, nil
}

func encode(file File) ([]byte, error) {
	data, err := yaml.Marshal(file)
	if err != nil {
		return nil, inputError(i18n.T("Failed to serialize configuration"))
	}
	if len(data) > maxConfigBytes {
		return nil, inputError(i18n.T("Configuration file is too large"))
	}
	return data, nil
}

func validateFile(file File) error {
	if file.Version != currentVersion {
		return inputError(i18n.T("Unsupported configuration version"))
	}
	if file.Contexts == nil {
		return inputError(i18n.T("contexts must be an object"))
	}
	if file.CurrentContext != "" {
		if _, ok := file.Contexts[file.CurrentContext]; !ok {
			return inputError(i18n.T("current_context does not exist"))
		}
	}
	for name, item := range file.Contexts {
		if item.UserToken != "" && (len(item.UserToken) > maxSecretBytes || validateSecret(item.UserToken) != nil) {
			return inputError(i18n.T("Invalid context user_token"))
		}
		if !namePattern.MatchString(name) {
			return inputError(i18n.T("Invalid context name"))
		}
		if strings.TrimSpace(item.Endpoint) == "" {
			return inputError(i18n.T("context endpoint must not be empty"))
		}
		if _, err := endpoint.Normalize(item.Endpoint); err != nil {
			return inputError(i18n.T("Invalid context endpoint"))
		}
		if item.Credential != nil {
			if !(item.Credential.Type == "env" && envPattern.MatchString(item.Credential.Name)) &&
				!(item.Credential.Type == "local_deployment" && filepath.IsAbs(item.Credential.Name)) {
				return inputError(i18n.T("context credential must be a valid env reference"))
			}
		}
		if item.Timeout != "" {
			d, err := time.ParseDuration(item.Timeout)
			if err != nil || d <= 0 {
				return inputError(i18n.T("Invalid context timeout"))
			}
		}
		if strings.ContainsAny(item.ExpectedWorkspaceUUID, "\r\n") {
			return inputError(i18n.T("Invalid expected_workspace_uuid"))
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
		return inputError(i18n.T("Failed to inspect configuration file"))
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return inputError(i18n.T("Symbolic-link configuration files are not supported"))
	}
	if !info.Mode().IsRegular() {
		return inputError(i18n.T("Configuration path is not a regular file"))
	}
	return nil
}
