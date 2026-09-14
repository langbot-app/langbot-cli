package deploy

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func upgradeFixture(t *testing.T) (Record, string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "deployment")
	assets, err := NewInstallAssets(dir, "v4.10.9", "basic", 15300)
	if err != nil {
		t.Fatal(err)
	}
	record := assets.Record("local-test", "sha256:source")
	if err := (Store{Dir: dir}).Write(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	if err := assets.Write(); err != nil {
		t.Fatal(err)
	}
	config := "database:\n  use: sqlite\n  sqlite:\n    path: data/langbot.db\nvdb:\n  use: chroma\nstorage:\n  use: local\n"
	for name, value := range map[string]string{"config.yaml": config, "langbot.db": "original database", "plugins/example/file.txt": "plugin state"} {
		path := filepath.Join(record.DataDir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(value), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return record, dir
}

func TestUpgradeOnlyAcceptsNewerStableVersionAndOwnedAssets(t *testing.T) {
	for _, test := range []struct {
		current, target string
		want            int
	}{
		{"v4.10.9", "v4.10.10", -1}, {"v4.10.10", "v4.10.10", 0},
		{"v5.0.0", "v4.99.99", 1},
	} {
		got, err := CompareReleaseVersions(test.current, test.target)
		if err != nil || got != test.want {
			t.Fatalf("compare %s %s = %d, %v", test.current, test.target, got, err)
		}
	}
	if _, err := CompareReleaseVersions("latest", "v4.10.10"); err == nil {
		t.Fatal("latest 必须拒绝")
	}
	record, dir := upgradeFixture(t)
	updated, digest, err := ComposeForUpgrade(record, dir, "v4.10.10")
	if err != nil || !strings.Contains(string(updated), "rockchin/langbot:v4.10.10") || !strings.HasPrefix(digest, "sha256:") {
		t.Fatalf("ComposeForUpgrade() = %s, %s, %v", updated, digest, err)
	}
	if err := os.WriteFile(record.ComposeFile, []byte("services: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ComposeForUpgrade(record, dir, "v4.10.10"); err == nil {
		t.Fatal("手改 Compose 后不得自动升级")
	}
}

func TestUpgradeBackupVerifiesDataAndPreservesEnv(t *testing.T) {
	record, dir := upgradeFixture(t)
	bytes, free, err := InspectUpgradeData(record)
	if err != nil || bytes == 0 || free == 0 {
		t.Fatalf("InspectUpgradeData() = %d, %d, %v", bytes, free, err)
	}
	state := UpgradeState{DeploymentID: record.DeploymentID, SourceVersion: record.CoreVersion, TargetVersion: "v4.10.10", SourceCompose: fileDigestPath(record.ComposeFile), Phase: "stopped"}
	backup, err := CreateUpgradeBackup(context.Background(), record, state)
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyUpgradeBackup(backup); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"data/config.yaml", "data/langbot.db", "data/plugins/example/file.txt", "compose.yaml", ".env", "manifest.yaml"} {
		if !regularFile(filepath.Join(backup, name)) {
			t.Fatalf("备份缺少 %s", name)
		}
	}
	if err := os.WriteFile(filepath.Join(backup, "data", "langbot.db"), []byte("tampered"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := VerifyUpgradeBackup(backup); err == nil {
		t.Fatal("备份内容修改后必须检测到摘要不一致")
	}
	if _, err := os.Stat(filepath.Join(dir, "upgrade-state.yaml")); !os.IsNotExist(err) {
		t.Fatal("备份不应自行写入升级状态")
	}
}

func TestUpgradeRejectsExternalPersistenceAndInterruptedState(t *testing.T) {
	record, dir := upgradeFixture(t)
	configPath := filepath.Join(record.DataDir, "config.yaml")
	if err := os.WriteFile(configPath, []byte("database:\n  use: postgresql\nvdb:\n  use: chroma\nstorage:\n  use: local\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := InspectUpgradeData(record); err == nil {
		t.Fatal("外部 PostgreSQL 不得自动升级")
	}
	state := UpgradeState{DeploymentID: record.DeploymentID, SourceVersion: "v4.10.9", TargetVersion: "v4.10.10", Phase: "compose_switched"}
	if err := WriteUpgradeState(dir, state); err != nil {
		t.Fatal(err)
	}
	if err := CheckUpgradeInterrupted(dir); err == nil {
		t.Fatal("未完成升级不得再次执行生命周期修改")
	}
	status, err := (Diagnostics{Dir: dir, Store: Store{Dir: dir}, Runner: &fakeRunner{docker: map[string]CommandResult{"info --format {{.ServerVersion}}": {Stdout: "29"}}}}).Status(context.Background())
	if status.Upgrade == nil || status.Upgrade.Phase != "compose_switched" {
		t.Fatalf("status 未恢复升级阶段: %+v, %v", status.Upgrade, err)
	}
	state.Phase = "completed"
	if err := WriteUpgradeState(dir, state); err != nil {
		t.Fatal(err)
	}
	if err := CheckUpgradeInterrupted(dir); err != nil {
		t.Fatal(err)
	}
}

func TestUpgradeBackupStopsOnCanceledContext(t *testing.T) {
	record, _ := upgradeFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := CreateUpgradeBackup(ctx, record, UpgradeState{SourceVersion: "v4.10.9", SourceCompose: fileDigestPath(record.ComposeFile)})
	if err == nil {
		t.Fatal("取消后不得继续复制数据备份")
	}
}
