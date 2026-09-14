package command

import (
	"bytes"
	"context"
	"fmt"
	"io"
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

func TestTemporaryEndpointDiscoveryDoesNotReuseCurrentContextKey(t *testing.T) {
	configPath := writeEnvironmentConfig(t, "https://cloud.example", "workspace-local")
	localDir := writeDeploymentRecord(t, deploy.Record{Driver: deploy.DriverDockerCompose, Endpoint: "http://127.0.0.1:5300", ComposeProject: "langbot"})
	runner := &recordingRunner{}
	requests := 0
	transport := environmentTransport(func(request *http.Request) (*http.Response, error) {
		requests++
		if request.URL.Host != "127.0.0.1:5300" || request.Header.Get("X-API-Key") != "" {
			t.Fatalf("temporary discovery used the wrong target or credential: %s", request.URL)
		}
		return response(http.StatusOK, `{"code":0,"data":{"version":"v4.10.10","edition":"community"}}`), nil
	})
	localHTTP := &http.Client{Transport: environmentTransport(func(*http.Request) (*http.Response, error) {
		return response(http.StatusOK, `{}`), nil
	})}
	deps := Dependencies{LookupEnv: environmentLookup, Transport: transport, LocalRunner: runner, LocalHTTP: localHTTP, LocalDir: localDir}
	var out bytes.Buffer
	deps.Out = &out
	code := Execute(context.Background(), []string{"--config", configPath, "--endpoint", "http://127.0.0.1:5300", "--output", "json", "status"}, deps)
	if code != 0 || requests != 1 || runner.calls == 0 || !strings.Contains(out.String(), `"management": "local_managed"`) || !strings.Contains(out.String(), `"name": "temporary"`) {
		t.Fatalf("temporary local status failed: code=%d requests=%d output=%s", code, requests, out.String())
	}
	out.Reset()
	code = Execute(context.Background(), []string{"--config", configPath, "--endpoint", "http://127.0.0.1:5300", "--output", "json", "whoami"}, deps)
	if code != 3 || requests != 1 || !strings.Contains(out.String(), `"type": "auth"`) {
		t.Fatalf("protected command accepted an anonymous endpoint override: code=%d requests=%d output=%s", code, requests, out.String())
	}
}

func TestStatusReportsStoppedManagedDeploymentWithoutHTTP(t *testing.T) {
	configPath := writeEnvironmentConfig(t, "https://cloud.example", "workspace-local")
	localDir := writeDeploymentRecord(t, deploy.Record{Driver: deploy.DriverDockerCompose, Endpoint: "http://127.0.0.1:5300", ComposeProject: "langbot"})
	runner := &recordingRunner{stopped: true}
	offline := environmentTransport(func(*http.Request) (*http.Response, error) {
		return nil, fmt.Errorf("connection refused")
	})
	var out bytes.Buffer
	code := Execute(context.Background(), []string{"--config", configPath, "--endpoint", "http://127.0.0.1:5300", "--output", "json", "status"}, Dependencies{
		Out: &out, LookupEnv: environmentLookup, Transport: offline, LocalHTTP: &http.Client{Transport: offline}, LocalRunner: runner, LocalDir: localDir,
	})
	if code != 0 || runner.calls == 0 || !strings.Contains(out.String(), `"status": "stopped"`) || !strings.Contains(out.String(), `"discovery": "partial"`) {
		t.Fatalf("offline local status failed: code=%d calls=%d output=%s", code, runner.calls, out.String())
	}
}

func TestStatusDiscoversCloudWithoutDocker(t *testing.T) {
	configPath := writeEnvironmentConfig(t, "https://cloud.example", "workspace-local")
	runner := &recordingRunner{}
	var out bytes.Buffer
	code := Execute(context.Background(), []string{"--config", configPath, "--output", "json", "status"}, Dependencies{
		Out: &out, LookupEnv: environmentLookup, Transport: discoveryTransport("cloud", false), LocalRunner: runner, LocalDir: filepath.Join(t.TempDir(), "deployment"),
	})
	if code != 0 || runner.calls != 0 {
		t.Fatalf("cloud status failed or called Docker: code=%d calls=%d output=%s", code, runner.calls, out.String())
	}
	for _, expected := range []string{`"type": "cloud"`, `"authentication": "verified"`, `"management": "cloud_managed"`} {
		if !strings.Contains(out.String(), expected) {
			t.Fatalf("cloud discovery omitted %s: %s", expected, out.String())
		}
	}
}

func TestStatusDefaultOutputSummarizesActiveEnvironment(t *testing.T) {
	configPath := writeEnvironmentConfig(t, "https://cloud.example", "workspace-local")
	var out bytes.Buffer
	code := Execute(context.Background(), []string{"--config", configPath, "status"}, Dependencies{
		Out: &out, LookupEnv: environmentLookup, Transport: discoveryTransport("cloud", false), LocalDir: filepath.Join(t.TempDir(), "deployment"),
	})
	if code != 0 {
		t.Fatalf("default status failed: code=%d output=%s", code, out.String())
	}
	for _, expected := range []string{
		"Active Environment: active", "Type: LangBot Cloud", "Connection: Connected", "Authentication: Verified",
		"Workspace: workspace-local", "Lifecycle: Managed by LangBot Cloud",
	} {
		if !strings.Contains(out.String(), expected) {
			t.Fatalf("default status omitted %q: %s", expected, out.String())
		}
	}
	if strings.HasPrefix(strings.TrimSpace(out.String()), "{") || strings.Contains(out.String(), "schema_version:") {
		t.Fatalf("default status exposed machine output: %s", out.String())
	}
}

func TestStatusKeepsLegacyDiscoveryPartial(t *testing.T) {
	configPath := writeEnvironmentConfig(t, "https://legacy.example", "")
	runner := &recordingRunner{}
	var out bytes.Buffer
	code := Execute(context.Background(), []string{"--config", configPath, "--output", "json", "status"}, Dependencies{
		Out: &out, LookupEnv: environmentLookup, Transport: discoveryTransport("cloud", true), LocalRunner: runner, LocalDir: filepath.Join(t.TempDir(), "deployment"),
	})
	if code != 0 || runner.calls != 0 {
		t.Fatalf("legacy status failed or called Docker: code=%d calls=%d output=%s", code, runner.calls, out.String())
	}
	for _, expected := range []string{`"discovery": "partial"`, `"status": "unknown"`, `"version": "v4.10.9"`} {
		if !strings.Contains(out.String(), expected) {
			t.Fatalf("legacy discovery omitted %s: %s", expected, out.String())
		}
	}
}

func TestStatusMatchesManagedSelfHostedDeployment(t *testing.T) {
	configPath := writeEnvironmentConfig(t, "http://127.0.0.1:5300", "workspace-local")
	localDir := filepath.Join(t.TempDir(), "deployment")
	composeFile := filepath.Join(localDir, "compose.yaml")
	if err := (deploy.Store{Dir: localDir}).Write(context.Background(), deploy.Record{
		InstanceUUID: "instance-test", Driver: deploy.DriverDockerCompose, DeploymentID: "local", Endpoint: "http://127.0.0.1:5300",
		ComposeProject: "langbot", ComposeFile: composeFile,
	}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(composeFile, []byte("services: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runner := &recordingRunner{}
	localHTTP := &http.Client{Transport: environmentTransport(func(*http.Request) (*http.Response, error) {
		return response(http.StatusOK, `{}`), nil
	})}
	var out bytes.Buffer
	code := Execute(context.Background(), []string{"--config", configPath, "--output", "json", "status"}, Dependencies{
		Out: &out, LookupEnv: environmentLookup, Transport: discoveryTransport("community", false), LocalRunner: runner, LocalHTTP: localHTTP, LocalDir: localDir,
	})
	if code != 0 || runner.calls == 0 {
		t.Fatalf("managed local status failed: code=%d calls=%d output=%s", code, runner.calls, out.String())
	}
	for _, expected := range []string{`"type": "self_hosted"`, `"driver": "docker_compose"`, `"management": "local_managed"`, `"status": "running"`} {
		if !strings.Contains(out.String(), expected) {
			t.Fatalf("managed discovery omitted %s: %s", expected, out.String())
		}
	}
}

func TestDoctorRoutesCloudWithoutDocker(t *testing.T) {
	configPath := writeEnvironmentConfig(t, "https://cloud.example", "workspace-local")
	runner := &recordingRunner{}
	var out bytes.Buffer
	code := Execute(context.Background(), []string{"--config", configPath, "--output", "json", "doctor"}, Dependencies{
		Out: &out, LookupEnv: environmentLookup, Transport: discoveryTransport("cloud", false), LocalRunner: runner, LocalDir: filepath.Join(t.TempDir(), "deployment"),
	})
	if code != 0 || runner.calls != 0 || !strings.Contains(out.String(), `"runtime_binding"`) {
		t.Fatalf("cloud doctor routing mismatch: code=%d calls=%d output=%s", code, runner.calls, out.String())
	}
}

func TestStatusUsesVerifiedBindingWhenEditionIsUnknown(t *testing.T) {
	configPath := writeEnvironmentConfig(t, "http://127.0.0.1:5300", "workspace-local")
	localDir := writeDeploymentRecord(t, deploy.Record{
		InstanceUUID: "instance-test", Driver: deploy.DriverDockerCompose, Endpoint: "http://127.0.0.1:5300", ComposeProject: "langbot",
	})
	runner := &recordingRunner{}
	localHTTP := &http.Client{Transport: environmentTransport(func(*http.Request) (*http.Response, error) {
		return response(http.StatusOK, `{}`), nil
	})}
	var out bytes.Buffer
	code := Execute(context.Background(), []string{"--config", configPath, "--output", "json", "status"}, Dependencies{
		Out: &out, LookupEnv: environmentLookup, Transport: discoveryTransport("custom", false), LocalRunner: runner, LocalHTTP: localHTTP, LocalDir: localDir,
	})
	if code != 0 || runner.calls == 0 {
		t.Fatalf("verified local binding was rejected by an unknown edition: code=%d calls=%d output=%s", code, runner.calls, out.String())
	}
	for _, expected := range []string{`"type": "unknown"`, `"binding": "verified"`, `"management": "local_managed"`} {
		if !strings.Contains(out.String(), expected) {
			t.Fatalf("verified local binding omitted %s: %s", expected, out.String())
		}
	}
}

func TestStatusDoesNotUseEndpointFallbackAfterInstanceMismatch(t *testing.T) {
	configPath := writeEnvironmentConfig(t, "http://127.0.0.1:5300", "workspace-local")
	localDir := writeDeploymentRecord(t, deploy.Record{
		InstanceUUID: "different-instance", Driver: deploy.DriverDockerCompose, Endpoint: "http://127.0.0.1:5300", ComposeProject: "langbot",
	})
	runner := &recordingRunner{}
	var out bytes.Buffer
	code := Execute(context.Background(), []string{"--config", configPath, "--output", "json", "status"}, Dependencies{
		Out: &out, LookupEnv: environmentLookup, Transport: discoveryTransport("community", false), LocalRunner: runner, LocalDir: localDir,
	})
	if code != 0 || runner.calls != 0 {
		t.Fatalf("instance mismatch called Docker: code=%d calls=%d output=%s", code, runner.calls, out.String())
	}
	if !strings.Contains(out.String(), `"binding": "mismatch"`) || !strings.Contains(out.String(), `"management": "unbound"`) {
		t.Fatalf("instance mismatch was not kept unbound: %s", out.String())
	}
}

func TestStatusDoesNotClaimNativeLifecycleSupport(t *testing.T) {
	configPath := writeEnvironmentConfig(t, "http://127.0.0.1:5300", "workspace-local")
	localDir := writeDeploymentRecord(t, deploy.Record{
		InstanceUUID: "instance-test", Driver: deploy.DriverNativeProcess, Endpoint: "http://127.0.0.1:5300",
	})
	runner := &recordingRunner{}
	var out bytes.Buffer
	code := Execute(context.Background(), []string{"--config", configPath, "--output", "json", "status"}, Dependencies{
		Out: &out, LookupEnv: environmentLookup, Transport: discoveryTransport("community", false), LocalRunner: runner, LocalDir: localDir,
	})
	if code != 0 || runner.calls != 0 {
		t.Fatalf("native binding called Docker: code=%d calls=%d output=%s", code, runner.calls, out.String())
	}
	for _, expected := range []string{`"driver": "native_process"`, `"binding": "verified"`, `"management": "unavailable"`} {
		if !strings.Contains(out.String(), expected) {
			t.Fatalf("native lifecycle contract omitted %s: %s", expected, out.String())
		}
	}
}

func TestStatusSharesOneLocalRuntimeAcrossWorkspaceContexts(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	configData := "version: 1\ncontexts:\n" +
		"  workspace-a:\n    endpoint: http://127.0.0.1:5300\n    credential:\n      type: env\n      name: KEY_A\n    expected_workspace_uuid: workspace-a\n" +
		"  workspace-b:\n    endpoint: http://127.0.0.1:5300\n    credential:\n      type: env\n      name: KEY_B\n    expected_workspace_uuid: workspace-b\n"
	if err := os.WriteFile(configPath, []byte(configData), 0o600); err != nil {
		t.Fatal(err)
	}
	localDir := writeDeploymentRecord(t, deploy.Record{
		InstanceUUID: "shared-instance", Driver: deploy.DriverDockerCompose, Endpoint: "http://127.0.0.1:5300", ComposeProject: "langbot",
	})
	runner := &recordingRunner{}
	localHTTP := &http.Client{Transport: environmentTransport(func(*http.Request) (*http.Response, error) {
		return response(http.StatusOK, `{}`), nil
	})}
	transport := environmentTransport(func(request *http.Request) (*http.Response, error) {
		switch request.URL.Path {
		case "/api/v1/system/info":
			return response(http.StatusOK, `{"code":0,"data":{"version":"v4.10.10","edition":"community"}}`), nil
		case "/api/v1/system/context":
			workspace := strings.ToLower(strings.TrimPrefix(request.Header.Get("X-API-Key"), "key-"))
			return response(http.StatusOK, fmt.Sprintf(`{"code":0,"data":{"instance_uuid":"shared-instance","workspace_uuid":%q,"api_key_id":"key-id","permissions":[]}}`, workspace)), nil
		case "/api/v1/system/capabilities":
			return response(http.StatusOK, `{"code":0,"data":{"schema_version":1,"operations":{}}}`), nil
		default:
			return response(http.StatusNotFound, `{"code":404,"msg":"not found"}`), nil
		}
	})
	lookup := func(name string) (string, bool) {
		values := map[string]string{"KEY_A": "key-workspace-a", "KEY_B": "key-workspace-b"}
		value, ok := values[name]
		return value, ok
	}
	for _, contextName := range []string{"workspace-a", "workspace-b"} {
		var out bytes.Buffer
		code := Execute(context.Background(), []string{"--config", configPath, "--context", contextName, "--output", "json", "status"}, Dependencies{
			Out: &out, LookupEnv: lookup, Transport: transport, LocalRunner: runner, LocalHTTP: localHTTP, LocalDir: localDir,
		})
		if code != 0 || !strings.Contains(out.String(), `"workspace_uuid": "`+contextName+`"`) || !strings.Contains(out.String(), `"management": "local_managed"`) {
			t.Fatalf("context %s did not reuse the local runtime: code=%d output=%s", contextName, code, out.String())
		}
	}
	if runner.calls == 0 {
		t.Fatal("matching Workspace contexts did not inspect the shared runtime")
	}
}

func TestStatusUsesManagedLocalRecordWhenNoContextIsSelected(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	localDir := writeDeploymentRecord(t, deploy.Record{
		DeploymentID: "local-langbot", InstanceUUID: "instance-test", Driver: deploy.DriverDockerCompose,
		Endpoint: "http://127.0.0.1:5300", ComposeProject: "langbot",
	})
	runner := &recordingRunner{}
	localHTTP := &http.Client{Transport: environmentTransport(func(*http.Request) (*http.Response, error) {
		return response(http.StatusOK, `{}`), nil
	})}
	var out bytes.Buffer
	code := Execute(context.Background(), []string{"--config", configPath, "--output", "json", "status"}, Dependencies{
		Out: &out, LookupEnv: func(string) (string, bool) { return "", false }, Transport: discoveryTransport("community", false),
		LocalRunner: runner, LocalHTTP: localHTTP, LocalDir: localDir,
	})
	if code != 0 || runner.calls == 0 {
		t.Fatalf("managed local fallback failed: code=%d calls=%d output=%s", code, runner.calls, out.String())
	}
	for _, expected := range []string{`"name": "local-langbot"`, `"temporary": true`, `"management": "local_managed"`} {
		if !strings.Contains(out.String(), expected) {
			t.Fatalf("managed local fallback omitted %s: %s", expected, out.String())
		}
	}
}

func TestDoctorRunsLocalPreflightWithoutAConfiguredTarget(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	runnerTransport := environmentTransport(func(request *http.Request) (*http.Response, error) {
		return nil, fmt.Errorf("unexpected HTTP request: %s", request.URL)
	})
	localDir := filepath.Join(t.TempDir(), "missing", "deployment")
	preflightRunner := &preflightRecordingRunner{}
	var out bytes.Buffer
	code := Execute(context.Background(), []string{"--config", configPath, "--output", "json", "doctor"}, Dependencies{
		Out: &out, LookupEnv: func(string) (string, bool) { return "", false }, Transport: runnerTransport,
		LocalRunner: preflightRunner, LocalDir: localDir,
	})
	if code != 0 || preflightRunner.calls == 0 {
		t.Fatalf("local preflight failed: code=%d calls=%d output=%s", code, preflightRunner.calls, out.String())
	}
	for _, expected := range []string{`"discovery": "unconfigured"`, `"name": "docker_cli"`, `"name": "deployment_record"`} {
		if !strings.Contains(out.String(), expected) {
			t.Fatalf("local preflight omitted %s: %s", expected, out.String())
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
	configPath := writeEnvironmentConfig(t, "http://127.0.0.1:5300", "workspace-local")
	localDir := filepath.Join(t.TempDir(), "deployment")
	composeFile := filepath.Join(localDir, "compose.yaml")
	if err := (deploy.Store{Dir: localDir}).Write(context.Background(), deploy.Record{
		InstanceUUID: "instance-test", Driver: deploy.DriverDockerCompose, Endpoint: "http://127.0.0.1:5300", ComposeProject: "langbot", ComposeFile: composeFile,
	}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(composeFile, []byte("services: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runner := &recordingRunner{blocking: true}
	var out bytes.Buffer
	started := time.Now()
	code := Execute(context.Background(), []string{"--config", configPath, "--timeout", "20ms", "status"}, Dependencies{
		Out: &out, LookupEnv: environmentLookup, Transport: discoveryTransport("community", false), LocalRunner: runner, LocalDir: localDir,
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

func environmentLookup(name string) (string, bool) {
	if name == "TEST_API_KEY" {
		return "test-api-key", true
	}
	return "", false
}

func discoveryTransport(edition string, legacy bool) http.RoundTripper {
	return environmentTransport(func(request *http.Request) (*http.Response, error) {
		switch request.URL.Path {
		case "/api/v1/system/info":
			version := "v4.10.10"
			if legacy {
				version = "v4.10.9"
			}
			return response(http.StatusOK, fmt.Sprintf(`{"code":0,"data":{"version":%q,"edition":%q}}`, version, edition)), nil
		case "/api/v1/system/context":
			if legacy {
				return response(http.StatusNotFound, `{"code":404,"msg":"not found"}`), nil
			}
			return response(http.StatusOK, `{"code":0,"data":{"instance_uuid":"instance-test","workspace_uuid":"workspace-local","api_key_id":"key-test","permissions":["resource.view"]}}`), nil
		case "/api/v1/system/capabilities":
			if legacy {
				return response(http.StatusNotFound, `{"code":404,"msg":"not found"}`), nil
			}
			return response(http.StatusOK, `{"code":0,"data":{"schema_version":1,"operations":{"bot.list":{"supported":true}}}}`), nil
		default:
			return response(http.StatusNotFound, `{"code":404,"msg":"not found"}`), nil
		}
	})
}

func response(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
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
