package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/langbot-app/langbot-cli/internal/deploy"
	"github.com/langbot-app/langbot-cli/internal/result"
)

type upgradeRunner struct {
	running         bool
	startingPolls   int
	startingAfterUp int
	failUp          bool
	failPull        bool
	pulledVersion   string
	resolvedConfig  string
	calls           []string
}

func (r *upgradeRunner) Docker(_ context.Context, args ...string) (deploy.CommandResult, error) {
	call := strings.Join(args, " ")
	r.calls = append(r.calls, "docker "+call)
	switch {
	case len(args) > 0 && args[0] == "run":
		var dataDir, backupDir string
		for _, argument := range args {
			if strings.HasPrefix(argument, "type=bind,src=") {
				parts := strings.Split(strings.TrimPrefix(argument, "type=bind,src="), ",dst=")
				if strings.HasPrefix(parts[1], "/data") {
					dataDir = parts[0]
				} else {
					backupDir = parts[0]
				}
			}
		}
		if backupDir != "" {
			return deploy.CommandResult{}, os.CopyFS(filepath.Join(backupDir, "data"), os.DirFS(dataDir))
		}
		config, err := os.ReadFile(filepath.Join(dataDir, "config.yaml"))
		if err != nil {
			return deploy.CommandResult{}, err
		}
		encoded, _ := json.Marshal(map[string]any{"config": string(config), "size": 100, "free": 10 << 30})
		return deploy.CommandResult{Stdout: string(encoded)}, nil
	case strings.HasPrefix(call, "info "):
		return deploy.CommandResult{Stdout: "29"}, nil
	case strings.HasPrefix(call, "pull "):
		if r.failPull {
			return deploy.CommandResult{}, errors.New("registry unavailable")
		}
		r.pulledVersion = strings.TrimPrefix(args[len(args)-1], "rockchin/langbot:")
		return deploy.CommandResult{}, nil
	case strings.HasPrefix(call, "image inspect "):
		return deploy.CommandResult{Stdout: `["rockchin/langbot@sha256:` + strings.Repeat("a", 64) + `"]`}, nil
	default:
		return deploy.CommandResult{}, fmt.Errorf("unexpected Docker command: %s", call)
	}
}

func (r *upgradeRunner) Compose(_ context.Context, _ deploy.Record, args ...string) (deploy.CommandResult, error) {
	call := strings.Join(args, " ")
	r.calls = append(r.calls, "compose "+call)
	switch call {
	case "config --format json":
		return deploy.CommandResult{Stdout: r.resolvedConfig}, nil
	case "ps --format json":
		state := "exited"
		if r.running {
			state = "running"
		}
		health := ""
		if r.running && r.startingPolls > 0 {
			r.startingPolls--
			health = "starting"
		}
		return deploy.CommandResult{Stdout: fmt.Sprintf(`[{"Service":"langbot","State":%q},{"Service":"langbot_plugin_runtime","State":%q,"Health":%q}]`, state, state, health)}, nil
	case "stop":
		r.running = false
		return deploy.CommandResult{}, nil
	case "up -d --pull never":
		if r.failUp {
			return deploy.CommandResult{Stderr: "failed"}, errors.New("failed")
		}
		r.running = true
		r.startingPolls = r.startingAfterUp
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
	image, err := deploy.ComposeSourceImage(data)
	if err != nil {
		return nil, err
	}
	version := strings.TrimPrefix(image, "rockchin/langbot:")
	if strings.Contains(image, "@sha256:") {
		version = t.runner.pulledVersion
	}
	body := fmt.Sprintf(`{"code":0,"data":{"version":%q,"edition":"community","instance_uuid":"instance-upgrade","workspace_uuid":"workspace-upgrade","api_key_id":"global","permissions":[],"schema_version":1,"operations":{}},"msg":"ok"}`, version)
	return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
}

func adoptedUpgradeServiceFixture(t *testing.T) (*Service, *upgradeRunner, deploy.Record, string) {
	t.Helper()
	service, runner, record, _ := upgradeServiceFixture(t, false)
	recordDir := filepath.Join(t.TempDir(), "adopted-record")
	record.ComposeProject = "existing-project"
	record.Asset.Name = filepath.Base(record.ComposeFile)
	content, err := os.ReadFile(record.ComposeFile)
	if err != nil {
		t.Fatal(err)
	}
	record.Asset.Digest = composeDigest(content)
	record.EnvFile = ""
	if err := (deploy.Store{Dir: recordDir}).Write(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	service.deps.LocalDir = recordDir
	resolved, err := json.Marshal(map[string]any{"services": map[string]any{
		"langbot":                map[string]any{"volumes": []map[string]string{{"type": "bind", "source": record.DataDir, "target": "/app/data"}}},
		"langbot_plugin_runtime": map[string]any{"volumes": []map[string]string{{"type": "bind", "source": filepath.Join(record.DataDir, "plugins"), "target": "/app/data/plugins"}}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	runner.resolvedConfig = string(resolved)
	return service, runner, record, recordDir
}

func TestAdoptedUpgradeCompletesAndCanUpgradeAgain(t *testing.T) {
	service, runner, record, dir := adoptedUpgradeServiceFixture(t)
	for _, version := range []string{"v4.10.10", "v4.10.11"} {
		response, err := service.Upgrade(context.Background(), UpgradeOptions{CheckOptions: CheckOptions{Endpoint: record.Endpoint, EndpointSet: true}, Version: version, Yes: true})
		if err != nil || response.Data.(map[string]any)["completed"] != true {
			t.Fatalf("adopted upgrade %s: %+v, %v", version, response.Data, err)
		}
		updated, err := (deploy.Store{Dir: dir}).Read()
		content, readErr := os.ReadFile(record.ComposeFile)
		if err != nil || readErr != nil || updated.CoreVersion != version || updated.Asset.Name != filepath.Base(record.ComposeFile) || updated.Asset.Digest != composeDigest(content) {
			t.Fatalf("adopted record lost Compose identity: %+v, %v, %v", updated, err, readErr)
		}
		state, err := deploy.ReadUpgradeState(dir)
		if err != nil || state.Phase != "completed" {
			t.Fatalf("state: %+v, %v", state, err)
		}
		if err := deploy.VerifyUpgradeBackup(state.BackupDir); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(filepath.Join(state.BackupDir, "deployment.yaml")); err != nil {
			t.Fatal(err)
		}
	}
	if !runner.running {
		t.Fatal("adopted deployment did not restart")
	}
}

func TestAdoptedUpgradeRejectsMountMismatchBeforePullOrStop(t *testing.T) {
	service, runner, record, _ := adoptedUpgradeServiceFixture(t)
	runner.resolvedConfig = `{"services":{"langbot":{"volumes":[{"type":"volume","source":"unbacked","target":"/app/data"}]},"langbot_plugin_runtime":{}}}`
	_, err := service.Upgrade(context.Background(), UpgradeOptions{CheckOptions: CheckOptions{Endpoint: record.Endpoint, EndpointSet: true}, Version: "v4.10.10", Yes: true})
	if result.AsError(err).Kind != "precondition" {
		t.Fatalf("wrong rejection: %v", err)
	}
	for _, call := range runner.calls {
		if call == "compose stop" || strings.HasPrefix(call, "docker pull") {
			t.Fatalf("unsafe preflight mutation: %s", call)
		}
	}
}

func TestAdoptedUpgradeRegistryFailureLeavesDeploymentRunning(t *testing.T) {
	service, runner, record, dir := adoptedUpgradeServiceFixture(t)
	runner.failPull = true
	before, err := os.ReadFile(record.ComposeFile)
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.Upgrade(context.Background(), UpgradeOptions{CheckOptions: CheckOptions{Endpoint: record.Endpoint, EndpointSet: true}, Version: "v4.10.10", Yes: true})
	if err == nil || !runner.running {
		t.Fatalf("registry failure stopped deployment: %v", err)
	}
	after, err := os.ReadFile(record.ComposeFile)
	if err != nil || string(before) != string(after) {
		t.Fatal("registry failure changed Compose")
	}
	if _, err := os.Stat(filepath.Join(dir, "upgrade-state.yaml")); !os.IsNotExist(err) {
		t.Fatal("registry failure left recovery state")
	}
}

func upgradeServiceFixture(t *testing.T, failUp bool) (*Service, *upgradeRunner, deploy.Record, string) {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, "deployment")
	assets, err := deploy.NewInstallAssets(dir, "v4.10.9", "basic", 15300, deploy.InstallNetworkOptions{})
	if err != nil {
		t.Fatal(err)
	}
	record := assets.Record("local-upgrade-test", "sha256:old")
	record.ImageRepositoryExplicit = true
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

func TestUpgradeResolvesLatestStableWhenVersionIsOmitted(t *testing.T) {
	service, _, record, _ := upgradeServiceFixture(t, false)
	called := false
	service.deps.LatestRelease = func(context.Context) (string, error) { called = true; return "v4.10.10", nil }
	response, err := service.Upgrade(context.Background(), UpgradeOptions{CheckOptions: CheckOptions{Endpoint: record.Endpoint, EndpointSet: true}, DryRun: true})
	if err != nil || !called || response.Data.(map[string]any)["dry_run"] != true {
		t.Fatalf("latest upgrade: %+v %v", response, err)
	}
}

func TestUpgradeCompletesAfterBackupAndVersionReadback(t *testing.T) {
	service, runner, record, dir := upgradeServiceFixture(t, false)
	service.deps.Version = "0.4.2"
	if err := os.WriteFile(record.EnvFile, []byte("CUSTOM_VALUE=keep\nLANGBOT_DEPLOY_TOOL_VERSION=0.4.1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
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
	environment, err := os.ReadFile(record.EnvFile)
	if err != nil || string(environment) != "CUSTOM_VALUE=keep\nLANGBOT_DEPLOY_TOOL_VERSION=0.4.2\nLANGBOT_DEPLOY_SOURCE=lbctl\n" {
		t.Fatalf("upgrade environment = %s, %v", environment, err)
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

func TestUpgradeWaitsForServiceHealthchecksToSettle(t *testing.T) {
	previousInterval := upgradeServicePollInterval
	upgradeServicePollInterval = time.Millisecond
	t.Cleanup(func() { upgradeServicePollInterval = previousInterval })
	service, runner, record, dir := upgradeServiceFixture(t, false)
	runner.startingAfterUp = 3
	response, err := service.Upgrade(context.Background(), UpgradeOptions{
		CheckOptions: CheckOptions{Endpoint: record.Endpoint, EndpointSet: true}, Version: "v4.10.10", Yes: true,
	})
	if err != nil || response.Data.(map[string]any)["completed"] != true {
		t.Fatalf("Upgrade() = %+v, %v", response.Data, err)
	}
	if runner.startingPolls != 0 {
		t.Fatalf("starting polls left = %d", runner.startingPolls)
	}
	state, err := deploy.ReadUpgradeState(dir)
	if err != nil || state.Phase != "completed" {
		t.Fatalf("state = %+v, %v", state, err)
	}
}

func TestUpgradeServiceWaitGivesUpAfterTimeout(t *testing.T) {
	previousInterval, previousTimeout := upgradeServicePollInterval, upgradeServiceReadyTimeout
	upgradeServicePollInterval, upgradeServiceReadyTimeout = time.Millisecond, 20*time.Millisecond
	t.Cleanup(func() { upgradeServicePollInterval, upgradeServiceReadyTimeout = previousInterval, previousTimeout })
	polls := 0
	status, err := waitForUpgradedServices(context.Background(), func(context.Context) (deploy.StatusResult, error) {
		polls++
		return deploy.StatusResult{Status: "starting"}, nil
	})
	if err != nil || status.Status != "starting" || polls < 2 {
		t.Fatalf("status = %+v, polls = %d, err = %v", status, polls, err)
	}
}

func TestUpgradeWithoutVersionIsNoOpWhenAlreadyLatest(t *testing.T) {
	service, runner, record, _ := upgradeServiceFixture(t, false)
	service.deps.LatestRelease = func(context.Context) (string, error) { return record.CoreVersion, nil }
	response, err := service.Upgrade(context.Background(), UpgradeOptions{CheckOptions: CheckOptions{Endpoint: record.Endpoint, EndpointSet: true}, Yes: true})
	data, _ := response.Data.(map[string]any)
	if err != nil || data["up_to_date"] != true || data["changed"] != false {
		t.Fatalf("up-to-date upgrade = %+v, %v", response.Data, err)
	}
	for _, call := range runner.calls {
		if strings.HasPrefix(call, "docker pull") || call == "compose stop" {
			t.Fatalf("up-to-date upgrade must not pull or stop: %v", runner.calls)
		}
	}
	if _, err := service.Upgrade(context.Background(), UpgradeOptions{CheckOptions: CheckOptions{Endpoint: record.Endpoint, EndpointSet: true}, Version: record.CoreVersion, Yes: true}); err == nil {
		t.Fatal("an explicit same --version must still be rejected")
	}
}
