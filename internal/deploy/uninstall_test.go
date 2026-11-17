package deploy

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPurgeDataRemovesOnlyManagedData(t *testing.T) {
	record, dir := upgradeFixture(t)
	backup := filepath.Join(dir, "backups", "keep", "manifest.yaml")
	if err := os.MkdirAll(filepath.Dir(backup), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(backup, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	purged, recoveryPath, err := PurgeData(dir, record)
	if err != nil || !purged || recoveryPath != "" {
		t.Fatalf("PurgeData() = %v, %q, %v", purged, recoveryPath, err)
	}
	if _, err := os.Stat(record.DataDir); !os.IsNotExist(err) {
		t.Fatalf("数据目录仍然存在: %v", err)
	}
	for _, path := range []string{record.ComposeFile, record.EnvFile, backup, filepath.Join(dir, recordFileName)} {
		if !regularFile(path) {
			t.Fatalf("清理误删了 %s", path)
		}
	}
	if current, err := (Store{Dir: dir}).Read(); err != nil || current.DeploymentID != record.DeploymentID {
		t.Fatalf("部署记录未保留: %+v, %v", current, err)
	}
	purged, _, err = PurgeData(dir, record)
	if err != nil || purged {
		t.Fatalf("重复清理应保持幂等: %v, %v", purged, err)
	}
}

func TestPurgeDockerRootOwnedData(t *testing.T) {
	image := os.Getenv("LBCTL_DOCKER_TEST_IMAGE")
	if image == "" {
		t.Skip("set LBCTL_DOCKER_TEST_IMAGE for Docker integration")
	}
	if os.Geteuid() == 0 {
		t.Fatal("Docker cleanup test must run as non-root")
	}
	for _, symlink := range []bool{false, true} {
		t.Run(fmt.Sprintf("symlink=%v", symlink), func(t *testing.T) {
			record, dir := upgradeFixture(t)
			record.Asset.Name = image
			record.CoreVersion = strings.TrimPrefix(image, ImageRepository(record)+":")
			compose := composeAutostart([]byte(renderCompose(image, record.Port, filepath.Join(record.DataDir, "box"), defaultInstallPorts())), record.Autostart)
			if err := os.WriteFile(record.ComposeFile, compose, 0o600); err != nil {
				t.Fatal(err)
			}
			if err := (Store{Dir: dir}).Write(t.Context(), record); err != nil {
				t.Fatal(err)
			}
			runner := DockerRunner{}
			script := "import os; os.mkdir('/data/migration-backups', 0o700); open('/data/migration-backups/backup.db', 'w').write('backup')"
			if symlink {
				script += "; os.symlink('/etc', '/data/migration-backups/escape')"
			}
			output, err := runner.Docker(t.Context(), "run", "--rm", "--network", "none", "--user", "0:0", "--mount", "type=bind,src="+record.DataDir+",dst=/data", "--entrypoint", "python", image, "-c", script)
			if err != nil {
				t.Fatalf("create data: %v %s", err, output.Stderr)
			}
			t.Cleanup(func() {
				_, err := runner.Docker(context.Background(), "run", "--rm", "--network", "none", "--user", "0:0", "--mount", "type=bind,src="+dir+",dst=/fixture", "--entrypoint", "python", image, "-c", "import shutil; shutil.rmtree('/fixture/data', ignore_errors=True)")
				if err != nil {
					t.Error(err)
				}
			})
			if _, err := os.ReadDir(filepath.Join(record.DataDir, "migration-backups")); !os.IsPermission(err) {
				t.Fatalf("expected unreadable root-owned data: %v", err)
			}
			if err := ValidatePurgeTarget(dir, record); err != nil {
				t.Fatal(err)
			}
			purged, recovery, err := PurgeDockerData(t.Context(), runner, dir, record)
			if symlink {
				if err == nil || purged || recovery != record.DataDir || !strings.Contains(err.Error(), "symbolic links") {
					t.Fatalf("purged=%v recovery=%s err=%v", purged, recovery, err)
				}
			} else if err != nil || !purged || recovery != "" {
				t.Fatalf("purged=%v recovery=%s err=%v", purged, recovery, err)
			}
		})
	}
}

func TestPurgeDataRejectsUnmanagedPathAndSymlink(t *testing.T) {
	record, dir := upgradeFixture(t)
	outside := t.TempDir()
	tampered := record
	tampered.DataDir = outside
	if _, _, err := PurgeData(dir, tampered); err == nil {
		t.Fatal("不得接受与磁盘记录不一致的数据目录")
	}
	if _, err := os.Stat(outside); err != nil {
		t.Fatalf("边界外目录受到影响: %v", err)
	}
	target := filepath.Join(t.TempDir(), "target")
	if err := os.MkdirAll(target, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(record.DataDir, "escape")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("当前平台无法创建符号链接: %v", err)
	}
	if err := ValidatePurgeData(dir, record); err == nil {
		t.Fatal("数据目录包含符号链接时必须拒绝清理")
	}
	if _, err := os.Stat(target); err != nil {
		t.Fatalf("边界外目录受到影响: %v", err)
	}
}

func TestValidatePurgeDataRejectsAdoptedDeployment(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "record")
	compose := filepath.Join(root, "compose.yaml")
	if err := os.WriteFile(compose, []byte("services: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	record := Record{
		DeploymentID: "adopted", Driver: DriverDockerCompose, CoreVersion: "v4.10.10", Profile: "basic",
		ComposeProject: "custom", ComposeFile: compose, DataDir: filepath.Join(root, "data"), Endpoint: "http://127.0.0.1:5300", Port: 5300,
	}
	if err := (Store{Dir: dir}).Write(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	if err := ValidatePurgeData(dir, record); err == nil {
		t.Fatal("接管的自定义 Compose 不得自动清理数据")
	}
}
