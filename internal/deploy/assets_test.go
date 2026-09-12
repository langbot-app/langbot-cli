package deploy

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInstallAssetsUsePinnedImageAndRestrictedFiles(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "deployment")
	assets, err := NewInstallAssets(dir, "v4.10.10", "basic", 15300)
	if err != nil {
		t.Fatal(err)
	}
	if assets.Image != "rockchin/langbot:v4.10.10" || strings.Contains(string(assets.Compose), ":latest") {
		t.Fatalf("unexpected image: %s\n%s", assets.Image, assets.Compose)
	}
	if strings.Contains(string(assets.Compose), "CONTROL_TOKEN=") || !strings.Contains(string(assets.Environment), "LANGBOT_BOX_CONTROL_TOKEN=") {
		t.Fatal("控制 token 应只写入环境文件")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := assets.Write(); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{assets.ComposeFile, assets.EnvFile} {
		info, statErr := os.Stat(path)
		if statErr != nil {
			t.Fatalf("stat %s: %v", path, statErr)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("%s mode = %v", path, info.Mode().Perm())
		}
	}
}

func TestInstallAssetsRejectUnpinnedVersionAndUnsafeTarget(t *testing.T) {
	if _, err := NewInstallAssets(t.TempDir(), "latest", "basic", 5300); err == nil {
		t.Fatal("应该拒绝 latest")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "user.txt"), []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ValidateInstallTarget(dir, true); err == nil {
		t.Fatal("应该拒绝非空目录")
	}
}

func TestLocatorResolvesCustomDeployment(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "config", "deployment-location.yaml")
	dir := filepath.Join(root, "custom")
	locator := Locator{Path: path}
	if err := locator.Write(dir); err != nil {
		t.Fatal(err)
	}
	got, err := locator.Resolve("")
	if err != nil || got != dir {
		t.Fatalf("Resolve() = %q, %v", got, err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("locator mode = %v", info.Mode().Perm())
	}
}

func TestPreflightInstallDoesNotCreateTarget(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "new")
	runner := &fakeRunner{docker: map[string]CommandResult{
		"--version":                        {Stdout: "Docker version 29"},
		"info --format {{.ServerVersion}}": {Stdout: "29"},
		"compose version --short":          {Stdout: "5.5.1"},
	}}
	checks, err := PreflightInstall(context.Background(), dir, 15431, runner)
	if err != nil || len(checks) != 14 {
		t.Fatalf("checks = %+v, err = %v", checks, err)
	}
	if _, statErr := os.Stat(dir); !os.IsNotExist(statErr) {
		t.Fatalf("前置检查不应创建目录: %v", statErr)
	}
}

func TestPreflightRejectsExistingManagedProject(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "new")
	runner := &fakeRunner{docker: map[string]CommandResult{
		"--version":                        {Stdout: "Docker version 29"},
		"info --format {{.ServerVersion}}": {Stdout: "29"},
		"compose version --short":          {Stdout: "5.5.1"},
		"ps -a --filter label=com.docker.compose.project=langbot-lbctl --format {{.ID}}": {Stdout: "existing-container\n"},
	}}
	checks, err := PreflightInstall(context.Background(), dir, 15431, runner)
	if err == nil {
		t.Fatal("已有相同 Compose project 时应拒绝安装")
	}
	found := false
	for _, check := range checks {
		if check.Name == "compose_project" && check.Status == "error" {
			found = true
		}
	}
	if !found {
		t.Fatalf("未报告 Compose project 冲突: %+v", checks)
	}
}
