package command

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/langbot-app/langbot-cli/internal/deploy"
)

type installPortRunner struct{ calls []string }

func (r *installPortRunner) Docker(_ context.Context, args ...string) (deploy.CommandResult, error) {
	joined := strings.Join(args, " ")
	r.calls = append(r.calls, joined)
	switch joined {
	case "--version":
		return deploy.CommandResult{Stdout: "Docker version 29"}, nil
	case "info --format {{.ServerVersion}}":
		return deploy.CommandResult{Stdout: "29"}, nil
	case "compose version --short":
		return deploy.CommandResult{Stdout: "5.5.1"}, nil
	}
	return deploy.CommandResult{}, nil
}

func (*installPortRunner) Compose(context.Context, deploy.Record, ...string) (deploy.CommandResult, error) {
	panic("dry-run must not run Compose")
}

func TestInstallDryRunExposesSelectedPortsAndProject(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	listener.Close()
	root := t.TempDir()
	dir := filepath.Join(root, "deployment")
	runner := &installPortRunner{}
	var out bytes.Buffer
	code := Execute(context.Background(), []string{
		"install", "--version", "v4.10.10", "--dir", dir, "--project", "regression",
		"--port", "invalid-port",
	}, Dependencies{Out: &out, LocalRunner: runner, LookupEnv: environmentLookup})
	if code != 2 || len(runner.calls) != 0 {
		t.Fatalf("invalid flag reached Docker: code=%d calls=%v output=%s", code, runner.calls, out.String())
	}
	out.Reset()
	code = Execute(context.Background(), []string{
		"install", "--version", "v4.10.10", "--dir", dir, "--project", "regression",
		"--port", strconv.Itoa(port),
		"--plugin-debug-port", "0", "--adapter-ports", "none", "--profile", "all", "--dry-run", "-o", "json",
	}, Dependencies{Out: &out, LocalRunner: runner, LocalLocatorPath: filepath.Join(root, "locator.yaml"), LookupEnv: environmentLookup})
	var envelope struct {
		OK   bool `json:"ok"`
		Data struct {
			Project string   `json:"compose_project"`
			Debug   int      `json:"plugin_debug_port"`
			Adapter []string `json:"adapter_ports"`
			Checks  []struct {
				Name   string `json:"name"`
				Detail any    `json:"detail"`
			} `json:"checks"`
		} `json:"data"`
	}
	if err := json.Unmarshal(out.Bytes(), &envelope); err != nil || code != 0 || !envelope.OK {
		t.Fatalf("dry-run failed: code=%d error=%v output=%s", code, err, out.String())
	}
	if envelope.Data.Project != "regression" || envelope.Data.Debug != 0 || envelope.Data.Adapter == nil || len(envelope.Data.Adapter) != 0 {
		t.Fatalf("plan lost selected publications: %s", out.String())
	}
	for _, check := range envelope.Data.Checks {
		if check.Name == "port" && check.Detail != float64(port) {
			t.Fatalf("preflight checked an unpublished port: %+v", check)
		}
	}
	for _, call := range runner.calls {
		if strings.HasPrefix(call, "pull ") || strings.Contains(call, "compose.project=langbot-lbctl") {
			t.Fatalf("dry-run affected images or selected the default project: %s", call)
		}
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("dry-run wrote deployment files: %v", err)
	}
}
