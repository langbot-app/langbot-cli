package deploy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/langbot-app/langbot-cli/internal/i18n"
	"go.yaml.in/yaml/v3"
)

const inspectDockerData = `import json, os, stat, shutil
def fail(error):
    raise error
total = 0
for root, directories, files in os.walk('/data', onerror=fail):
    for name in directories + files:
        path = os.path.join(root, name)
        info = os.lstat(path)
        if not (stat.S_ISDIR(info.st_mode) or stat.S_ISREG(info.st_mode)):
            raise ValueError('Unsupported backup path: ' + path)
        if stat.S_ISREG(info.st_mode):
            total += info.st_size
with open('/data/config.yaml') as config:
    print(json.dumps(dict(config=config.read(), size=total, free=shutil.disk_usage('/data').free)))
`

func InspectDockerUpgradeData(ctx context.Context, runner Runner, record Record) (int64, uint64, error) {
	image, err := dockerBackupImage(record)
	if err != nil {
		return 0, 0, err
	}
	output, err := runner.Docker(ctx, "run", "--rm", "--network", "none", "--user", "0:0", "--mount", "type=bind,src="+record.DataDir+",dst=/data,readonly", "--entrypoint", "python", image, "-c", inspectDockerData)
	if err != nil {
		return 0, 0, fmt.Errorf(i18n.T("Inspect Docker backup data: %w"), err)
	}
	var inspection struct {
		Config string `json:"config"`
		Size   int64  `json:"size"`
		Free   uint64 `json:"free"`
	}
	if err := json.Unmarshal([]byte(output.Stdout), &inspection); err != nil {
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
	if err := yaml.Unmarshal([]byte(inspection.Config), &config); err != nil {
		return 0, 0, errors.New(i18n.T("Unable to parse local configuration"))
	}
	if config.Database.Use != "sqlite" || config.VDB.Use != "chroma" || config.Storage.Use != "local" ||
		(config.Database.SQLite.Path != "data/langbot.db" && config.Database.SQLite.Path != "./data/langbot.db") {
		return 0, 0, errors.New(i18n.T("Current persistence configuration is outside the fully backed-up default local mode"))
	}
	if inspection.Size < 0 || inspection.Free < uint64(inspection.Size)+1<<30 {
		return inspection.Size, inspection.Free, errors.New(i18n.T("Insufficient backup space; retain at least 1 GiB extra"))
	}
	return inspection.Size, inspection.Free, nil
}

const copyDockerData = `import os, shutil, stat, sys
def fail(error):
    raise error
restore = sys.argv[1] == 'restore'
source, target = ('/backup/data', '/data') if restore else ('/data', '/backup/data')
owner = os.stat('/backup')
if restore:
    for name in os.listdir(target):
        path = os.path.join(target, name)
        if os.path.isdir(path) and not os.path.islink(path):
            shutil.rmtree(path)
        else:
            os.unlink(path)
def copy_file(source, target):
    if not stat.S_ISREG(os.lstat(source).st_mode):
        raise ValueError('Unsupported backup file: ' + source)
    shutil.copyfile(source, target)
    os.chmod(target, 0o600)
    if not restore:
        os.chown(target, owner.st_uid, owner.st_gid)
for root, directories, files in os.walk(source, onerror=fail):
    destination = os.path.join(target, os.path.relpath(root, source))
    os.makedirs(destination, exist_ok=True)
    os.chmod(destination, 0o700)
    if not restore:
        os.chown(destination, owner.st_uid, owner.st_gid)
    for name in directories:
        if not stat.S_ISDIR(os.lstat(os.path.join(root, name)).st_mode):
            raise ValueError('Unsupported backup directory: ' + name)
    for name in files:
        copy_file(os.path.join(root, name), os.path.join(destination, name))
`

func copyDockerUpgradeData(ctx context.Context, runner Runner, record Record, backup string, restore bool) error {
	image, err := dockerBackupImage(record)
	if err != nil {
		return err
	}
	mode, dataMode, backupMode := "backup", ",readonly", ""
	if restore {
		mode, dataMode, backupMode = "restore", "", ",readonly"
	}
	_, err = runner.Docker(ctx, "run", "--rm", "--network", "none", "--user", "0:0", "--mount", "type=bind,src="+record.DataDir+",dst=/data"+dataMode,
		"--mount", "type=bind,src="+backup+",dst=/backup"+backupMode, "--entrypoint", "python", image, "-c", copyDockerData, mode)
	return err
}

func dockerBackupImage(record Record) (string, error) {
	compose, err := os.ReadFile(record.ComposeFile)
	if err != nil {
		return "", err
	}
	return ComposeSourceImage(compose)
}

func RestoreDockerUpgradeBackup(ctx context.Context, runner Runner, record Record, backup string) error {
	if err := verifyUpgradeBackup(ctx, backup); err != nil {
		return err
	}
	backup, err := filepath.Abs(backup)
	if err != nil {
		return err
	}
	record.ComposeFile = filepath.Join(backup, "compose.yaml")
	return copyDockerUpgradeData(ctx, runner, record, backup, true)
}
