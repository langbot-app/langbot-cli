package deploy

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"github.com/langbot-app/langbot-cli/internal/i18n"
)

func PurgeData(dir string, record Record) (bool, string, error) {
	current, err := validatedPurgeRecord(dir, record)
	if err != nil {
		return false, "", err
	}
	if err := ValidatePurgeData(dir, current); err != nil {
		return false, current.DataDir, err
	}
	dataDir := filepath.Clean(current.DataDir)
	info, err := os.Lstat(dataDir)
	if errors.Is(err, os.ErrNotExist) {
		return false, "", nil
	}
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return false, "", errors.New(i18n.T("Data directory changed before deletion"))
	}
	quarantine := filepath.Join(dir, ".purge-data-"+current.DeploymentID)
	if _, err := os.Lstat(quarantine); !errors.Is(err, os.ErrNotExist) {
		return false, quarantine, errors.New(i18n.T("Found unfinished data cleanup state"))
	}
	if err := os.Rename(dataDir, quarantine); err != nil {
		return false, "", err
	}
	if err := os.RemoveAll(quarantine); err != nil {
		return false, quarantine, err
	}
	return true, "", nil
}

func ValidatePurgeData(dir string, record Record) error {
	current, err := validatedPurgeRecord(dir, record)
	if err != nil {
		return err
	}
	return filepath.WalkDir(current.DataDir, func(path string, entry fs.DirEntry, walkErr error) error {
		if errors.Is(walkErr, os.ErrNotExist) && path == current.DataDir {
			return nil
		}
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return errors.New(i18n.T("Data directory contains symbolic links; refusing automatic cleanup"))
		}
		return nil
	})
}

func ValidatePurgeTarget(dir string, record Record) error {
	_, err := validatedPurgeRecord(dir, record)
	return err
}

func validatedPurgeRecord(dir string, record Record) (Record, error) {
	current, err := (Store{Dir: dir}).Read()
	if err != nil || !samePurgeTarget(current, record) {
		return Record{}, errors.New(i18n.T("Deployment record changed or managed marker is invalid"))
	}
	if err := ValidateOwnedLayout(current, dir); err != nil {
		return Record{}, errors.New(i18n.T("Only default deployments installed by lbctl support automatic data cleanup"))
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
		return Record{}, errors.New(i18n.T("Data directory is missing, not a directory or a symbolic link"))
	}
	quarantine := filepath.Join(dir, ".purge-data-"+current.DeploymentID)
	if _, err := os.Lstat(quarantine); !errors.Is(err, os.ErrNotExist) {
		return Record{}, errors.New(i18n.T("Found unfinished data cleanup state"))
	}
	return current, nil
}

func samePurgeTarget(current, requested Record) bool {
	return current.DeploymentID == requested.DeploymentID && current.InstanceUUID == requested.InstanceUUID &&
		current.Driver == requested.Driver && current.CoreVersion == requested.CoreVersion && current.Profile == requested.Profile &&
		current.ComposeProject == requested.ComposeProject && samePath(current.ComposeFile, requested.ComposeFile) &&
		samePath(current.EnvFile, requested.EnvFile) && samePath(current.DataDir, requested.DataDir) &&
		current.Endpoint == requested.Endpoint && current.Port == requested.Port && current.Asset == requested.Asset &&
		reflect.DeepEqual(current.InstallPorts, requested.InstallPorts)
}

func validateRemovalBoundary(dir, target string) error {
	dir, err := filepath.Abs(filepath.Clean(dir))
	if err != nil {
		return errors.New(i18n.T("Invalid deployment directory"))
	}
	target, err = filepath.Abs(filepath.Clean(target))
	if err != nil {
		return errors.New(i18n.T("Invalid data directory"))
	}
	home, _ := os.UserHomeDir()
	if filepath.Dir(target) == target || (home != "" && samePath(target, home)) {
		return errors.New(i18n.T("Refusing to delete filesystem root or user home directory"))
	}
	if samePath(target, dir) || !samePath(target, filepath.Join(dir, "data")) {
		return errors.New(i18n.T("Data directory is outside the managed deployment boundary"))
	}
	rel, err := filepath.Rel(dir, target)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return errors.New(i18n.T("Data directory escapes the managed deployment boundary"))
	}
	return nil
}

func samePath(left, right string) bool {
	return filepath.Clean(left) == filepath.Clean(right)
}
