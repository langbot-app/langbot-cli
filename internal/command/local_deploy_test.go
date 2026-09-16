package command

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/langbot-app/langbot-cli/internal/deploy"
)

type environmentTransport func(*http.Request) (*http.Response, error)

func (f environmentTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

type recordingRunner struct {
	calls    int
	blocking bool
	stopped  bool
}

func (r *recordingRunner) Docker(ctx context.Context, _ ...string) (deploy.CommandResult, error) {
	r.calls++
	if r.blocking {
		<-ctx.Done()
		return deploy.CommandResult{}, ctx.Err()
	}
	return deploy.CommandResult{Stdout: "27"}, nil
}

func (r *recordingRunner) Compose(context.Context, deploy.Record, ...string) (deploy.CommandResult, error) {
	r.calls++
	if r.stopped {
		return deploy.CommandResult{Stdout: `[{"Service":"langbot","State":"exited"},{"Service":"langbot_plugin_runtime","State":"exited"}]`}, nil
	}
	return deploy.CommandResult{Stdout: `[{"Service":"langbot","State":"running","Health":"healthy"},{"Service":"langbot_plugin_runtime","State":"running","Health":"healthy"}]`}, nil
}

func TestStatusIgnoresSelectedRemoteContext(t *testing.T) {
	configPath := writeEnvironmentConfig(t, "https://cloud.example", "workspace-cloud")
	localDir := writeDeploymentRecord(t, deploy.Record{
		DeploymentID: "local-langbot", Driver: deploy.DriverDockerCompose, CoreVersion: "v4.10.10", Profile: "basic",
		Endpoint: "http://127.0.0.1:5300", ComposeProject: "langbot",
	})
	runner := &recordingRunner{}
	remoteRequests := 0
	remote := environmentTransport(func(request *http.Request) (*http.Response, error) {
		remoteRequests++
		return nil, fmt.Errorf("unexpected remote request: %s", request.URL)
	})
	localHTTP := &http.Client{Transport: environmentTransport(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: http.NoBody}, nil
	})}
	var out bytes.Buffer
	code := Execute(context.Background(), []string{"--config", configPath, "--output", "json", "status"}, Dependencies{
		Out: &out, LookupEnv: environmentLookup, Transport: remote, LocalHTTP: localHTTP, LocalRunner: runner, LocalDir: localDir,
	})
	if code != 0 || remoteRequests != 0 || runner.calls == 0 {
		t.Fatalf("status used remote context: code=%d requests=%d calls=%d output=%s", code, remoteRequests, runner.calls, out.String())
	}
	data := commandData(t, out.String())
	if data["dir"] != localDir || data["status"] != "running" {
		t.Fatalf("local status target mismatch: %#v", data)
	}
	for _, expected := range []string{`"status": "running"`, `"deployment_id": "local-langbot"`} {
		if !strings.Contains(out.String(), expected) {
			t.Fatalf("local status omitted %s: %s", expected, out.String())
		}
	}
}

func TestLocalCommandsRejectConnectionOverrides(t *testing.T) {
	for _, args := range [][]string{
		{"--context", "production", "status"},
		{"--endpoint", "https://cloud.example", "doctor"},
		{"--api-key-stdin", "start"},
	} {
		var out bytes.Buffer
		runner := &recordingRunner{}
		code := Execute(context.Background(), append([]string{"--output", "json"}, args...), Dependencies{
			In: strings.NewReader("secret"), Out: &out, LocalRunner: runner, LocalDir: filepath.Join(t.TempDir(), "deployment"),
		})
		if code != 2 || runner.calls != 0 || !strings.Contains(out.String(), "本机部署命令不接受") {
			t.Fatalf("args=%v code=%d calls=%d output=%s", args, code, runner.calls, out.String())
		}
	}
}

func TestStatusUsesDirOverride(t *testing.T) {
	defaultDir := filepath.Join(t.TempDir(), "default")
	overrideDir := writeDeploymentRecord(t, deploy.Record{
		DeploymentID: "override", Driver: deploy.DriverDockerCompose, Endpoint: "http://127.0.0.1:5300", ComposeProject: "langbot",
	})
	runner := &recordingRunner{stopped: true}
	localHTTP := &http.Client{Transport: environmentTransport(func(*http.Request) (*http.Response, error) {
		return nil, fmt.Errorf("connection refused")
	})}
	var out bytes.Buffer
	code := Execute(context.Background(), []string{"--output", "json", "status", "--dir", overrideDir}, Dependencies{
		Out: &out, LocalRunner: runner, LocalHTTP: localHTTP, LocalDir: defaultDir,
	})
	data := commandData(t, out.String())
	if code != 0 || data["status"] != "stopped" || data["dir"] != overrideDir {
		t.Fatalf("status did not use --dir: code=%d output=%s", code, out.String())
	}
}

func TestStatusDefaultOutputSummarizesLocalDeployment(t *testing.T) {
	localDir := writeDeploymentRecord(t, deploy.Record{
		DeploymentID: "local-langbot", Driver: deploy.DriverDockerCompose, CoreVersion: "v4.10.10", Profile: "basic",
		Endpoint: "http://127.0.0.1:5300", ComposeProject: "langbot",
	})
	var out bytes.Buffer
	code := Execute(context.Background(), []string{"status"}, Dependencies{
		Out: &out, LocalRunner: &recordingRunner{}, LocalDir: localDir,
		LocalHTTP: &http.Client{Transport: environmentTransport(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: http.NoBody}, nil
		})},
	})
	if code != 0 {
		t.Fatalf("status failed: code=%d output=%s", code, out.String())
	}
	for _, expected := range []string{
		"Status: Running", "Directory: " + localDir, "Core Version: v4.10.10", "Profile: basic",
		"Endpoint: http://127.0.0.1:5300", "HTTP: Healthy", "Containers:",
	} {
		if !strings.Contains(out.String(), expected) {
			t.Fatalf("default status omitted %q: %s", expected, out.String())
		}
	}
	if strings.Contains(out.String(), "Active Environment") || strings.Contains(out.String(), "Workspace") {
		t.Fatalf("status still exposes remote environment fields: %s", out.String())
	}
}

func TestDoctorIgnoresSelectedRemoteContext(t *testing.T) {
	configPath := writeEnvironmentConfig(t, "https://cloud.example", "workspace-cloud")
	localDir := filepath.Join(t.TempDir(), "missing", "deployment")
	runner := &preflightRecordingRunner{}
	remoteRequests := 0
	var out bytes.Buffer
	code := Execute(context.Background(), []string{"--config", configPath, "--output", "json", "doctor"}, Dependencies{
		Out: &out, LookupEnv: environmentLookup, LocalDir: localDir, LocalRunner: runner,
		Transport: environmentTransport(func(*http.Request) (*http.Response, error) {
			remoteRequests++
			return nil, fmt.Errorf("unexpected remote request")
		}),
	})
	if code != 0 || remoteRequests != 0 || runner.calls == 0 {
		t.Fatalf("doctor used remote context: code=%d requests=%d calls=%d output=%s", code, remoteRequests, runner.calls, out.String())
	}
	if data := commandData(t, out.String()); data["dir"] != localDir {
		t.Fatalf("doctor target mismatch: %#v", data)
	}
	for _, expected := range []string{`"name": "docker_cli"`, `"name": "deployment_record"`} {
		if !strings.Contains(out.String(), expected) {
			t.Fatalf("doctor omitted %s: %s", expected, out.String())
		}
	}
}

type preflightRecordingRunner struct {
	calls int
}

func (r *preflightRecordingRunner) Docker(_ context.Context, args ...string) (deploy.CommandResult, error) {
	r.calls++
	switch strings.Join(args, " ") {
	case "--version":
		return deploy.CommandResult{Stdout: "Docker version 27"}, nil
	case "info --format {{.ServerVersion}}":
		return deploy.CommandResult{Stdout: "27"}, nil
	case "compose version --short":
		return deploy.CommandResult{Stdout: "v2.29.0"}, nil
	default:
		return deploy.CommandResult{}, fmt.Errorf("unexpected Docker arguments: %v", args)
	}
}

func (r *preflightRecordingRunner) Compose(context.Context, deploy.Record, ...string) (deploy.CommandResult, error) {
	return deploy.CommandResult{}, fmt.Errorf("unexpected Compose call")
}

func TestStatusTimeoutBoundsManagedDockerCall(t *testing.T) {
	localDir := writeDeploymentRecord(t, deploy.Record{
		DeploymentID: "local", Driver: deploy.DriverDockerCompose, Endpoint: "http://127.0.0.1:5300", ComposeProject: "langbot",
	})
	runner := &recordingRunner{blocking: true}
	var out bytes.Buffer
	started := time.Now()
	code := Execute(context.Background(), []string{"--timeout", "20ms", "status"}, Dependencies{
		Out: &out, LocalRunner: runner, LocalDir: localDir,
	})
	if code != 7 || time.Since(started) > time.Second || !strings.Contains(out.String(), "Docker daemon 不可达") {
		t.Fatalf("timeout did not bound Docker: code=%d elapsed=%s output=%q", code, time.Since(started), out.String())
	}
}

func writeEnvironmentConfig(t *testing.T, endpoint, workspace string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	content := fmt.Sprintf("version: 1\ncurrent_context: active\ncontexts:\n  active:\n    endpoint: %s\n    credential:\n      type: env\n      name: TEST_API_KEY\n", endpoint)
	if workspace != "" {
		content += "    expected_workspace_uuid: " + workspace + "\n"
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func writeDeploymentRecord(t *testing.T, record deploy.Record) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "deployment")
	if record.Driver == deploy.DriverDockerCompose {
		record.ComposeFile = filepath.Join(dir, "compose.yaml")
	}
	if err := (deploy.Store{Dir: dir}).Write(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	if record.Driver == deploy.DriverDockerCompose {
		if err := os.WriteFile(record.ComposeFile, []byte("services: {}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func commandData(t *testing.T, output string) map[string]any {
	t.Helper()
	var envelope struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal([]byte(output), &envelope); err != nil {
		t.Fatalf("invalid JSON output: %v; output=%s", err, output)
	}
	return envelope.Data
}

func environmentLookup(name string) (string, bool) {
	if name == "TEST_API_KEY" {
		return "test-api-key", true
	}
	return "", false
}

func TestUpgradeRequiresVersionAndExplicitConfirmation(t *testing.T) {
	runner := &recordingRunner{}
	for _, test := range []struct {
		args []string
		code int
		want string
	}{
		{[]string{"upgrade", "--yes"}, 2, "upgrade 需要 --version"},
		{[]string{"upgrade", "--version", "v4.10.10"}, 6, "--yes"},
		{[]string{"upgrade", "--version", "latest", "--yes"}, 2, "稳定 Release tag"},
	} {
		var out bytes.Buffer
		code := Execute(context.Background(), append([]string{"--output", "json"}, test.args...), Dependencies{
			Out: &out, LocalRunner: runner, LocalDir: filepath.Join(t.TempDir(), "deployment"),
		})
		if code != test.code || !strings.Contains(out.String(), test.want) || runner.calls != 0 {
			t.Fatalf("args=%v code=%d calls=%d output=%s", test.args, code, runner.calls, out.String())
		}
	}
}

func TestUninstallRequiresExplicitConfirmationBeforeDocker(t *testing.T) {
	runner := &recordingRunner{}
	var out bytes.Buffer
	code := Execute(context.Background(), []string{"--output", "json", "uninstall"}, Dependencies{
		Out: &out, LocalRunner: runner, LocalDir: filepath.Join(t.TempDir(), "deployment"),
	})
	if code != 6 || runner.calls != 0 || !strings.Contains(out.String(), "--yes") {
		t.Fatalf("uninstall confirmation mismatch: code=%d calls=%d output=%s", code, runner.calls, out.String())
	}
}
