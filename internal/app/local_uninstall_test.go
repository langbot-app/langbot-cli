package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/langbot-app/langbot-cli/internal/deploy"
	"github.com/langbot-app/langbot-cli/internal/result"
)

type uninstallRunner struct {
	containers bool
	downCalls  int
}

func (r *uninstallRunner) Docker(context.Context, ...string) (deploy.CommandResult, error) {
	return deploy.CommandResult{Stdout: "29"}, nil
}

func (r *uninstallRunner) Compose(_ context.Context, _ deploy.Record, args ...string) (deploy.CommandResult, error) {
	call := strings.Join(args, " ")
	switch call {
	case "ps -a --format json":
		if !r.containers {
			return deploy.CommandResult{Stdout: "[]"}, nil
		}
		return deploy.CommandResult{Stdout: `[{"Service":"langbot","State":"exited"},{"Service":"langbot_plugin_runtime","State":"exited"}]`}, nil
	case "down --remove-orphans":
		r.downCalls++
		r.containers = false
		return deploy.CommandResult{}, nil
	default:
		return deploy.CommandResult{}, fmt.Errorf("unexpected Compose command: %s", call)
	}
}

type uninstallTransport struct{ version string }

func (t uninstallTransport) RoundTrip(*http.Request) (*http.Response, error) {
	body := fmt.Sprintf(`{"code":0,"data":{"version":%q,"edition":"community"},"msg":"ok"}`, t.version)
	return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
}

func uninstallFixture(t *testing.T, containers bool) (*Service, *uninstallRunner, deploy.Record, string) {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, "deployment")
	assets, err := deploy.NewInstallAssets(dir, "v4.10.10", "basic", 15300)
	if err != nil {
		t.Fatal(err)
	}
	record := assets.Record("local-uninstall", "sha256:test")
	if err := (deploy.Store{Dir: dir}).Write(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	if err := assets.Write(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(record.DataDir, "config.yaml"), []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	runner := &uninstallRunner{containers: containers}
	transport := uninstallTransport{version: record.CoreVersion}
	service := New(Dependencies{
		LocalDir: dir, LocalRunner: runner, Transport: transport,
		DefaultConfigPath: func() (string, error) { return filepath.Join(root, "cli.yaml"), nil },
	})
	return service, runner, record, dir
}

func TestUninstallPreservesDataAndRecord(t *testing.T) {
	service, runner, record, dir := uninstallFixture(t, true)
	value, err := service.Uninstall(context.Background(), UninstallOptions{
		CheckOptions: CheckOptions{Endpoint: record.Endpoint, EndpointSet: true}, Yes: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	data := value.Data.(map[string]any)
	if data["project_removed"] != true || data["record_preserved"] != true || runner.downCalls != 1 {
		t.Fatalf("result=%#v down=%d", data, runner.downCalls)
	}
	for _, path := range []string{record.DataDir, record.ComposeFile, record.EnvFile, filepath.Join(dir, "deployment.yaml")} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("默认卸载误删了 %s: %v", path, err)
		}
	}
}

func TestUninstallPurgeRemovesDataAfterProject(t *testing.T) {
	service, runner, record, dir := uninstallFixture(t, true)
	backup := filepath.Join(dir, "backups", "keep", "manifest.yaml")
	if err := os.MkdirAll(filepath.Dir(backup), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(backup, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	value, err := service.Uninstall(context.Background(), UninstallOptions{
		CheckOptions: CheckOptions{Endpoint: record.Endpoint, EndpointSet: true}, PurgeData: true, Yes: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	data := value.Data.(map[string]any)
	if data["data_purged"] != true || runner.downCalls != 1 {
		t.Fatalf("result=%#v down=%d", data, runner.downCalls)
	}
	if _, err := os.Stat(record.DataDir); !os.IsNotExist(err) {
		t.Fatalf("数据目录未删除: %v", err)
	}
	for _, path := range []string{backup, record.ComposeFile, filepath.Join(dir, "deployment.yaml")} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("清理误删了 %s: %v", path, err)
		}
	}
}

func TestUninstallRequiresConfirmationAndBlocksInterruptedUpgrade(t *testing.T) {
	service, runner, record, dir := uninstallFixture(t, true)
	options := UninstallOptions{CheckOptions: CheckOptions{Endpoint: record.Endpoint, EndpointSet: true}}
	if _, err := service.Uninstall(context.Background(), options); result.AsError(err).Kind != "precondition" || runner.downCalls != 0 {
		t.Fatalf("缺少确认仍执行卸载: %v, down=%d", err, runner.downCalls)
	}
	if err := deploy.WriteUpgradeState(dir, deploy.UpgradeState{
		DeploymentID: record.DeploymentID, SourceVersion: "v4.10.9", TargetVersion: "v4.10.10", Phase: "compose_switched",
	}); err != nil {
		t.Fatal(err)
	}
	options.Yes = true
	if _, err := service.Uninstall(context.Background(), options); result.AsError(err).Kind != "recovery_required" || runner.downCalls != 0 {
		t.Fatalf("升级现场未阻止卸载: %v, down=%d", err, runner.downCalls)
	}
}

func TestUninstallPurgeRejectsAdoptedComposeBeforeDown(t *testing.T) {
	service, runner, record, dir := uninstallFixture(t, true)
	custom := record
	custom.ComposeProject = "custom"
	custom.Asset = deploy.AssetInfo{Name: "compose.yaml", Digest: "sha256:test"}
	if err := (deploy.Store{Dir: dir}).Write(context.Background(), custom); err != nil {
		t.Fatal(err)
	}
	_, err := service.Uninstall(context.Background(), UninstallOptions{
		CheckOptions: CheckOptions{Endpoint: record.Endpoint, EndpointSet: true}, PurgeData: true, Yes: true,
	})
	if result.AsError(err).Kind != "precondition" || runner.downCalls != 0 {
		t.Fatalf("自定义 Compose 清理没有在 down 前拒绝: %v, down=%d", err, runner.downCalls)
	}
	if _, statErr := os.Stat(record.DataDir); statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("数据目录状态异常: %v", statErr)
	}
}
