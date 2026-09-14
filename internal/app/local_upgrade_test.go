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

type upgradeRunner struct {
	running bool
	failUp  bool
	calls   []string
}

func (r *upgradeRunner) Docker(_ context.Context, args ...string) (deploy.CommandResult, error) {
	call := strings.Join(args, " ")
	r.calls = append(r.calls, "docker "+call)
	switch {
	case strings.HasPrefix(call, "info "):
		return deploy.CommandResult{Stdout: "29"}, nil
	case strings.HasPrefix(call, "pull "):
		return deploy.CommandResult{}, nil
	case strings.HasPrefix(call, "image inspect "):
		return deploy.CommandResult{Stdout: "rockchin/langbot@sha256:" + strings.Repeat("a", 64)}, nil
	default:
		return deploy.CommandResult{}, fmt.Errorf("unexpected Docker command: %s", call)
	}
}

func (r *upgradeRunner) Compose(_ context.Context, _ deploy.Record, args ...string) (deploy.CommandResult, error) {
	call := strings.Join(args, " ")
	r.calls = append(r.calls, "compose "+call)
	switch call {
	case "ps --format json":
		state := "exited"
		if r.running {
			state = "running"
		}
		return deploy.CommandResult{Stdout: fmt.Sprintf(`[{"Service":"langbot","State":%q},{"Service":"langbot_plugin_runtime","State":%q}]`, state, state)}, nil
	case "stop":
		r.running = false
		return deploy.CommandResult{}, nil
	case "up -d --pull never":
		if r.failUp {
			return deploy.CommandResult{Stderr: "failed"}, errors.New("failed")
		}
		r.running = true
		return deploy.CommandResult{}, nil
	default:
		return deploy.CommandResult{}, fmt.Errorf("unexpected Compose command: %s", call)
	}
}

type upgradeTransport struct {
	runner      *upgradeRunner
	composeFile string
}

func (t upgradeTransport) RoundTrip(*http.Request) (*http.Response, error) {
	if !t.runner.running {
		return nil, errors.New("connection refused")
	}
	data, err := os.ReadFile(t.composeFile)
	if err != nil {
		return nil, err
	}
	version := "v4.10.9"
	if strings.Contains(string(data), "rockchin/langbot:v4.10.10") {
		version = "v4.10.10"
	}
	body := fmt.Sprintf(`{"code":0,"data":{"version":%q,"edition":"community"},"msg":"ok"}`, version)
	return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
}

func upgradeServiceFixture(t *testing.T, failUp bool) (*Service, *upgradeRunner, deploy.Record, string) {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, "deployment")
	assets, err := deploy.NewInstallAssets(dir, "v4.10.9", "basic", 15300)
	if err != nil {
		t.Fatal(err)
	}
	record := assets.Record("local-upgrade-test", "sha256:old")
	if err := (deploy.Store{Dir: dir}).Write(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	if err := assets.Write(); err != nil {
		t.Fatal(err)
	}
	config := "database:\n  use: sqlite\n  sqlite:\n    path: data/langbot.db\nvdb:\n  use: chroma\nstorage:\n  use: local\n"
	if err := os.WriteFile(filepath.Join(record.DataDir, "config.yaml"), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(record.DataDir, "langbot.db"), []byte("old database"), 0o600); err != nil {
		t.Fatal(err)
	}
	runner := &upgradeRunner{running: true, failUp: failUp}
	transport := upgradeTransport{runner: runner, composeFile: record.ComposeFile}
	service := New(Dependencies{
		LocalDir: dir, LocalRunner: runner, LocalHTTP: &http.Client{Transport: transport}, Transport: transport,
		DefaultConfigPath: func() (string, error) { return filepath.Join(root, "config.yaml"), nil },
	})
	return service, runner, record, dir
}

func TestUpgradeDryRunDoesNotStopOrPull(t *testing.T) {
	service, runner, record, dir := upgradeServiceFixture(t, false)
	response, err := service.Upgrade(context.Background(), UpgradeOptions{
		CheckOptions: CheckOptions{Endpoint: record.Endpoint, EndpointSet: true}, Version: "v4.10.10", DryRun: true,
	})
	if err != nil || response.Data.(map[string]any)["dry_run"] != true {
		t.Fatalf("Upgrade(dry-run) = %+v, %v", response.Data, err)
	}
	for _, call := range runner.calls {
		if strings.HasPrefix(call, "docker pull ") || call == "compose stop" || strings.HasPrefix(call, "compose up ") {
			t.Fatalf("dry-run 改动了部署: %s", call)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "upgrade-state.yaml")); !os.IsNotExist(err) {
		t.Fatalf("dry-run 写入升级状态: %v", err)
	}
}

func TestUpgradeCompletesAfterBackupAndVersionReadback(t *testing.T) {
	service, runner, record, dir := upgradeServiceFixture(t, false)
	response, err := service.Upgrade(context.Background(), UpgradeOptions{
		CheckOptions: CheckOptions{Endpoint: record.Endpoint, EndpointSet: true}, Version: "v4.10.10", Yes: true,
	})
	if err != nil || response.Data.(map[string]any)["completed"] != true {
		t.Fatalf("Upgrade() = %+v, %v", response.Data, err)
	}
	updated, err := (deploy.Store{Dir: dir}).Read()
	if err != nil || updated.CoreVersion != "v4.10.10" || updated.Asset.Digest != "sha256:"+strings.Repeat("a", 64) {
		t.Fatalf("record = %+v, %v", updated, err)
	}
	state, err := deploy.ReadUpgradeState(dir)
	if err != nil || state.Phase != "completed" || state.BackupDir == "" {
		t.Fatalf("state = %+v, %v", state, err)
	}
	if err := deploy.VerifyUpgradeBackup(state.BackupDir); err != nil {
		t.Fatal(err)
	}
	if !runner.running {
		t.Fatal("升级后服务未运行")
	}
}

func TestUpgradeFailurePreservesBackupAndBlocksRestart(t *testing.T) {
	service, runner, record, dir := upgradeServiceFixture(t, true)
	_, err := service.Upgrade(context.Background(), UpgradeOptions{
		CheckOptions: CheckOptions{Endpoint: record.Endpoint, EndpointSet: true}, Version: "v4.10.10", Yes: true,
	})
	if result.AsError(err).Kind != "recovery_required" {
		t.Fatalf("failure error = %v", err)
	}
	state, err := deploy.ReadUpgradeState(dir)
	if err != nil || state.Phase != "recovery_required" || state.FailureStep != "compose_up" || state.BackupDir == "" {
		t.Fatalf("failure state = %+v, %v", state, err)
	}
	if err := deploy.VerifyUpgradeBackup(state.BackupDir); err != nil {
		t.Fatal(err)
	}
	current, err := (deploy.Store{Dir: dir}).Read()
	if err != nil || current.CoreVersion != "v4.10.9" {
		t.Fatalf("失败后不得宣布升级成功: %+v, %v", current, err)
	}
	if _, err := service.Start(context.Background(), LifecycleOptions{CheckOptions: CheckOptions{Endpoint: record.Endpoint, EndpointSet: true}}); result.AsError(err).Kind != "recovery_required" {
		t.Fatalf("未恢复现场仍允许 start: %v", err)
	}
	if runner.running {
		t.Fatal("失败后不得自动启动旧版")
	}
}
