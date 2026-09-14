package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/langbot-app/langbot-cli/internal/deploy"
	"github.com/langbot-app/langbot-cli/internal/result"
)

type lifecycleTestRunner struct {
	mu    sync.Mutex
	state string
	port  int
	calls []string
}

func (r *lifecycleTestRunner) Docker(_ context.Context, args ...string) (deploy.CommandResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	joined := strings.Join(args, " ")
	r.calls = append(r.calls, "docker "+joined)
	switch {
	case joined == "--version":
		return deploy.CommandResult{Stdout: "Docker version 29"}, nil
	case strings.HasPrefix(joined, "info "):
		return deploy.CommandResult{Stdout: "29.7.2"}, nil
	case joined == "compose version --short":
		return deploy.CommandResult{Stdout: "5.5.1"}, nil
	case strings.HasPrefix(joined, "pull "):
		return deploy.CommandResult{}, nil
	case strings.HasPrefix(joined, "ps -a --filter "), strings.HasPrefix(joined, "network ls --filter "):
		return deploy.CommandResult{}, nil
	case strings.HasPrefix(joined, "image inspect "):
		return deploy.CommandResult{Stdout: "rockchin/langbot@sha256:abc\n"}, nil
	default:
		return deploy.CommandResult{}, fmt.Errorf("unexpected docker call: %s", joined)
	}
}

func (r *lifecycleTestRunner) Compose(_ context.Context, _ deploy.Record, args ...string) (deploy.CommandResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	joined := strings.Join(args, " ")
	r.calls = append(r.calls, "compose "+joined)
	switch {
	case joined == "config --services":
		return deploy.CommandResult{Stdout: "langbot\nlangbot_plugin_runtime\nlangbot_box\n"}, nil
	case joined == "port langbot 5300":
		return deploy.CommandResult{Stdout: fmt.Sprintf("0.0.0.0:%d\n", r.port)}, nil
	case joined == "up -d":
		r.state = "running"
		return deploy.CommandResult{}, nil
	case joined == "stop":
		r.state = "stopped"
		return deploy.CommandResult{}, nil
	case joined == "restart":
		r.state = "running"
		return deploy.CommandResult{}, nil
	case joined == "ps --format json":
		state := "exited"
		if r.state == "running" {
			state = "running"
		}
		return deploy.CommandResult{Stdout: fmt.Sprintf(`[{"Name":"langbot","Service":"langbot","State":%q},{"Name":"runtime","Service":"langbot_plugin_runtime","State":%q}]`, state, state)}, nil
	case strings.HasPrefix(joined, "logs "):
		return deploy.CommandResult{Stdout: "first\nsecond\n"}, nil
	default:
		return deploy.CommandResult{}, fmt.Errorf("unexpected compose call: %s", joined)
	}
}

func (r *lifecycleTestRunner) ComposeStream(_ context.Context, _ deploy.Record, stdout, _ io.Writer, _ ...string) error {
	_, err := io.WriteString(stdout, "first\nsecond\n")
	return err
}

func (r *lifecycleTestRunner) isRunning() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.state == "running"
}

type lifecycleTransport struct {
	runner  *lifecycleTestRunner
	edition string
}

func (t lifecycleTransport) RoundTrip(*http.Request) (*http.Response, error) {
	if !t.runner.isRunning() {
		return nil, errors.New("connection refused")
	}
	edition := t.edition
	if edition == "" {
		edition = "community"
	}
	body := fmt.Sprintf(`{"code":0,"data":{"version":"v4.10.10","edition":%q,"instance_uuid":"instance-local","workspace_uuid":"workspace-local","api_key_id":"key-local","permissions":[]},"msg":"ok"}`, edition)
	return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
}

func TestInstallAndLifecycleCommands(t *testing.T) {
	port := freeLifecyclePort(t)
	root := t.TempDir()
	dir := filepath.Join(root, "deployment")
	runner := &lifecycleTestRunner{state: "stopped", port: port}
	service := New(Dependencies{
		LocalDir: dir, LocalLocatorPath: filepath.Join(root, "locator.yaml"), LocalRunner: runner,
		Transport: lifecycleTransport{runner: runner}, LocalHTTP: &http.Client{Transport: lifecycleTransport{runner: runner}},
		LatestRelease:     func(context.Context) (string, error) { return "v4.10.10", nil },
		DefaultConfigPath: func() (string, error) { return filepath.Join(root, "config.yaml"), nil },
	})
	installed, err := service.Install(context.Background(), InstallOptions{Profile: "basic", Port: port})
	if err != nil {
		t.Fatalf("Install() error = %v, data = %#v", err, installed.Data)
	}
	record, err := (deploy.Store{Dir: dir}).Read()
	if err != nil || record.CoreVersion != "v4.10.10" || record.Asset.Digest != "sha256:abc" {
		t.Fatalf("record = %+v, err = %v", record, err)
	}
	if data, _ := os.ReadFile(record.ComposeFile); strings.Contains(string(data), ":latest") {
		t.Fatal("compose 不应该使用 latest")
	}
	options := LifecycleOptions{CheckOptions: CheckOptions{Endpoint: record.Endpoint, EndpointSet: true}}
	stopped, err := service.Stop(context.Background(), options)
	if err != nil || actionStatus(stopped) != "stopped" {
		t.Fatalf("Stop() = %#v, %v", stopped.Data, err)
	}
	started, err := service.Start(context.Background(), options)
	if err != nil || actionStatus(started) != "running" {
		t.Fatalf("Start() = %#v, %v", started.Data, err)
	}
	restarted, err := service.Restart(context.Background(), options)
	if err != nil || actionStatus(restarted) != "running" {
		t.Fatalf("Restart() = %#v, %v", restarted.Data, err)
	}
	logs, err := service.LocalLogs(context.Background(), LocalLogsOptions{CheckOptions: options.CheckOptions, Tail: 20})
	if err != nil || logs.Data.(map[string]any)["count"] != 2 {
		t.Fatalf("LocalLogs() = %#v, %v", logs.Data, err)
	}
}

func TestInstallDryRunDoesNotWriteOrPull(t *testing.T) {
	port := freeLifecyclePort(t)
	root := t.TempDir()
	dir := filepath.Join(root, "deployment")
	runner := &lifecycleTestRunner{port: port}
	service := New(Dependencies{LocalDir: dir, LocalRunner: runner, LatestRelease: func(context.Context) (string, error) { return "v4.10.10", nil }})
	resultValue, err := service.Install(context.Background(), InstallOptions{DryRun: true, Port: port})
	if err != nil || resultValue.Data.(map[string]any)["dry_run"] != true {
		t.Fatalf("Install(dry-run) = %#v, %v", resultValue.Data, err)
	}
	if _, statErr := os.Stat(dir); !os.IsNotExist(statErr) {
		t.Fatalf("dry-run 不应该创建目录: %v", statErr)
	}
	for _, call := range runner.calls {
		if strings.HasPrefix(call, "docker pull ") {
			t.Fatal("dry-run 不应该拉取镜像")
		}
	}
}

func TestLifecycleRejectsCloudWithoutDockerMutation(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "deployment")
	runner := &lifecycleTestRunner{state: "running"}
	record := deploy.Record{DeploymentID: "local-test", Driver: deploy.DriverDockerCompose, Profile: "basic", ComposeProject: "langbot", ComposeFile: filepath.Join(root, "compose.yaml"), Endpoint: "http://127.0.0.1:15300", Port: 15300}
	if err := os.WriteFile(record.ComposeFile, []byte("services: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := (deploy.Store{Dir: dir}).Write(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	service := New(Dependencies{
		LocalDir: dir, LocalRunner: runner, Transport: lifecycleTransport{runner: runner, edition: "cloud"},
		DefaultConfigPath: func() (string, error) { return filepath.Join(root, "config.yaml"), nil },
	})
	_, err := service.Stop(context.Background(), LifecycleOptions{CheckOptions: CheckOptions{Endpoint: record.Endpoint, EndpointSet: true}})
	if result.AsError(err).Kind != "precondition" {
		t.Fatalf("Stop(cloud) error = %v", err)
	}
	if len(runner.calls) != 0 {
		t.Fatalf("cloud context 不应调用 Docker: %v", runner.calls)
	}
}

func TestLifecycleRejectsFailedServiceDiscoveryBeforeDockerMutation(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "deployment")
	runner := &lifecycleTestRunner{state: "running"}
	record := deploy.Record{DeploymentID: "local-test", Driver: deploy.DriverDockerCompose, Profile: "basic", ComposeProject: "langbot", ComposeFile: filepath.Join(root, "compose.yaml"), Endpoint: "http://127.0.0.1:15300", Port: 15300}
	if err := os.WriteFile(record.ComposeFile, []byte("services: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := (deploy.Store{Dir: dir}).Write(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	transport := taskRoundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusInternalServerError, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"code":500,"msg":"failed"}`))}, nil
	})
	service := New(Dependencies{LocalDir: dir, LocalRunner: runner, Transport: transport, DefaultConfigPath: func() (string, error) { return filepath.Join(root, "config.yaml"), nil }})
	_, err := service.Stop(context.Background(), LifecycleOptions{CheckOptions: CheckOptions{Endpoint: record.Endpoint, EndpointSet: true}})
	if result.AsError(err).Kind != "server" || len(runner.calls) != 0 {
		t.Fatalf("Stop() should reject failed discovery before Docker: error=%v calls=%v", err, runner.calls)
	}
}

func TestRestartStoppedDeploymentDoesNotStart(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "deployment")
	runner := &lifecycleTestRunner{state: "stopped"}
	record := deploy.Record{DeploymentID: "local-test", Driver: deploy.DriverDockerCompose, Profile: "basic", ComposeProject: "langbot", ComposeFile: filepath.Join(root, "compose.yaml"), Endpoint: "http://127.0.0.1:15300", Port: 15300}
	if err := os.WriteFile(record.ComposeFile, []byte("services: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := (deploy.Store{Dir: dir}).Write(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	service := New(Dependencies{
		LocalDir: dir, LocalRunner: runner, Transport: lifecycleTransport{runner: runner},
		LocalHTTP:         &http.Client{Transport: lifecycleTransport{runner: runner}},
		DefaultConfigPath: func() (string, error) { return filepath.Join(root, "config.yaml"), nil },
	})
	_, err := service.Restart(context.Background(), LifecycleOptions{CheckOptions: CheckOptions{Endpoint: record.Endpoint, EndpointSet: true}})
	if result.AsError(err).Kind != "precondition" || runner.isRunning() {
		t.Fatalf("Restart(stopped) error = %v, running = %v", err, runner.isRunning())
	}
}

func TestAdoptRequiresLocalEndpointAndMatchingPublishedPort(t *testing.T) {
	port := freeLifecyclePort(t)
	root := t.TempDir()
	composeFile := filepath.Join(root, "compose.yaml")
	if err := os.WriteFile(composeFile, []byte("services: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runner := &lifecycleTestRunner{state: "running", port: port}
	service := New(Dependencies{
		LocalDir: filepath.Join(root, "record"), LocalLocatorPath: filepath.Join(root, "locator.yaml"), LocalRunner: runner,
		Transport: lifecycleTransport{runner: runner}, DefaultConfigPath: func() (string, error) { return filepath.Join(root, "config.yaml"), nil },
	})
	if _, err := service.Adopt(context.Background(), AdoptOptions{ComposeFile: composeFile, ComposeProject: "langbot", Endpoint: "https://remote.example"}); result.AsError(err).Kind != "precondition" {
		t.Fatalf("remote adopt error = %v", err)
	}
	endpoint := fmt.Sprintf("http://127.0.0.1:%d", port)
	adopted, err := service.Adopt(context.Background(), AdoptOptions{ComposeFile: composeFile, ComposeProject: "langbot", Endpoint: endpoint})
	if err != nil || adopted.Data.(map[string]any)["changed"] != true {
		t.Fatalf("Adopt() = %#v, %v", adopted.Data, err)
	}
	record, err := (deploy.Store{Dir: filepath.Join(root, "record")}).Read()
	if err != nil || record.Endpoint != endpoint {
		t.Fatalf("record = %+v, err = %v", record, err)
	}
}

func TestFollowLogsUsesJSONLines(t *testing.T) {
	port := freeLifecyclePort(t)
	root := t.TempDir()
	dir := filepath.Join(root, "deployment")
	runner := &lifecycleTestRunner{state: "running", port: port}
	record := deploy.Record{DeploymentID: "local-test", Driver: deploy.DriverDockerCompose, Profile: "basic", ComposeProject: "langbot", ComposeFile: filepath.Join(root, "compose.yaml"), Endpoint: fmt.Sprintf("http://127.0.0.1:%d", port), Port: port}
	if err := os.WriteFile(record.ComposeFile, []byte("services: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := (deploy.Store{Dir: dir}).Write(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	service := New(Dependencies{LocalDir: dir, LocalRunner: runner, Transport: lifecycleTransport{runner: runner}, LocalLogOut: &out, DefaultConfigPath: func() (string, error) { return filepath.Join(root, "config.yaml"), nil }})
	err := service.FollowLocalLogs(context.Background(), LocalLogsOptions{CheckOptions: CheckOptions{Endpoint: record.Endpoint, EndpointSet: true}, Tail: 20, Follow: true, Format: "json"})
	if err != nil || strings.Count(strings.TrimSpace(out.String()), "\n") != 1 || !strings.Contains(out.String(), `"line":"first"`) {
		t.Fatalf("FollowLocalLogs() output = %q, err = %v", out.String(), err)
	}
}

func TestLogSanitizationRemovesANSIAndKnownTokens(t *testing.T) {
	line := sanitizeLogLine(`\u001b[31mfailed secret-token\u001b[0m`, []string{"secret-token"})
	if line != "failed [REDACTED]" {
		t.Fatalf("sanitized line = %q", line)
	}
}

func freeLifecyclePort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	return port
}

func actionStatus(value Result) string {
	data, _ := value.Data.(map[string]any)
	status, _ := data["status"].(deploy.StatusResult)
	return status.Status
}
