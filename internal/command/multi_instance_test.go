package command

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/langbot-app/langbot-cli/internal/deploy"
)

func TestContextsSelectCloudAndMultipleLocalInstances(t *testing.T) {
	root := t.TempDir()
	locator := deploy.Locator{Path: filepath.Join(root, "locator.yaml")}
	for _, item := range []struct{ id, endpoint, project string }{
		{"local-first", "http://127.0.0.1:15301", "first"},
		{"local-second", "http://127.0.0.1:15302", "second"},
	} {
		dir := writeDeploymentRecord(t, deploy.Record{DeploymentID: item.id, Driver: deploy.DriverDockerCompose, Endpoint: item.endpoint, ComposeProject: item.project})
		if err := locator.Write(dir); err != nil {
			t.Fatal(err)
		}
	}
	var out bytes.Buffer
	runner := &recordingRunner{}
	offlineSecond := false
	deps := Dependencies{
		Out: &out, LookupEnv: environmentLookup, LocalLocatorPath: locator.Path, LocalRunner: runner,
		DefaultConfigPath: func() (string, error) { return filepath.Join(root, "config.yaml"), nil },
		Transport: environmentTransport(func(request *http.Request) (*http.Response, error) {
			if offlineSecond && request.URL.Host == "127.0.0.1:15302" {
				return nil, errors.New("connection refused")
			}
			if request.URL.Host == "cloud.example" {
				return response(200, `{"code":0,"data":{"version":"v4.10.10","edition":"cloud"}}`), nil
			}
			return discoveryTransport("community", false).RoundTrip(request)
		}),
		LocalHTTP: &http.Client{Transport: environmentTransport(func(*http.Request) (*http.Response, error) { return response(200, `{}`), nil })},
	}
	run := func(args ...string) (int, string) {
		t.Helper()
		out.Reset()
		code := Execute(context.Background(), args, deps)
		return code, out.String()
	}
	for _, item := range []struct{ name, endpoint string }{
		{"first", "http://127.0.0.1:15301"}, {"second", "http://127.0.0.1:15302"}, {"cloud", "https://cloud.example"},
	} {
		if code, output := run("context", "add", item.name, "--endpoint", item.endpoint); code != 0 {
			t.Fatalf("add: %d %s", code, output)
		}
	}
	for _, format := range []string{"json", "yaml", ""} {
		args := []string{"context", "list"}
		if format != "" {
			args = append(args, "-o", format)
		}
		code, output := run(args...)
		if code != 0 || !strings.Contains(output, "127.0.0.1:15301") || !strings.Contains(output, "127.0.0.1:15302") || !strings.Contains(output, "cloud") || runner.calls != 0 {
			t.Fatalf("offline list failed: %d %s Docker calls=%d", code, output, runner.calls)
		}
		if format != "" && (!strings.Contains(output, "local-first") || !strings.Contains(output, "local-second")) {
			t.Fatalf("machine output lost deployment identity: %s", output)
		}
		if format == "" && strings.Contains(output, "尚未关联 context") {
			t.Fatalf("duplicated bound deployment rows: %s", output)
		}
	}
	// Switching is offline and uses the same command for all targets.
	for _, name := range []string{"second", "first", "cloud"} {
		if code, output := run("context", "use", name); code != 0 || runner.calls != 0 {
			t.Fatalf("offline use: %d %s", code, output)
		}
	}
	for _, name := range []string{"first", "second", "cloud"} {
		if code, output := run("context", "use", name); code != 0 {
			t.Fatalf("use: %d %s", code, output)
		}
		before := runner.calls
		code, output := run("status", "-o", "json")
		if code != 0 {
			t.Fatalf("status: %d %s", code, output)
		}
		if name == "cloud" {
			if !strings.Contains(output, `"management": "cloud_managed"`) || runner.calls != before {
				t.Fatalf("cloud inspected local Docker: %s", output)
			}
		} else if !strings.Contains(output, `"deployment_id": "local-`+name+`"`) {
			t.Fatalf("wrong local instance: %s", output)
		}
	}
	// A single-call override selects a local instance without changing cloud as current.
	if code, output := run("--context", "second", "status", "-o", "json"); code != 0 || !strings.Contains(output, `"deployment_id": "local-second"`) {
		t.Fatalf("context override: %d %s", code, output)
	}
	if code, output := run("context", "list", "-o", "json"); code != 0 || !strings.Contains(output, `"current_context": "cloud"`) {
		t.Fatalf("override changed saved selection: %d %s", code, output)
	}
	offlineSecond, runner.stopped = true, true
	if code, output := run("context", "use", "second"); code != 0 {
		t.Fatalf("offline switch failed: %d %s", code, output)
	}
	if code, output := run("status", "-o", "json"); code != 0 || !strings.Contains(output, `"status": "stopped"`) || !strings.Contains(output, `"deployment_id": "local-second"`) {
		t.Fatalf("stopped instance was not resolved: %d %s", code, output)
	}
}

func TestContextListReturnsSavedContextsWhenLocalRegistryIsUnavailable(t *testing.T) {
	root := t.TempDir()
	var out bytes.Buffer
	deps := Dependencies{Out: &out, LookupEnv: environmentLookup, LocalLocatorPath: root,
		DefaultConfigPath: func() (string, error) { return filepath.Join(root, "config.yaml"), nil },
	}
	if code := Execute(context.Background(), []string{"context", "add", "cloud", "--endpoint", "https://cloud.example"}, deps); code != 0 {
		t.Fatal(out.String())
	}
	out.Reset()
	if code := Execute(context.Background(), []string{"context", "list", "-o", "json"}, deps); code != 0 || !strings.Contains(out.String(), `"local_error"`) || !strings.Contains(out.String(), "https://cloud.example") {
		t.Fatalf("local error hid saved contexts: %d %s", code, out.String())
	}
}
