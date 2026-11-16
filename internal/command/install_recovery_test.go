package command

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/langbot-app/langbot-cli/internal/app"
	"github.com/langbot-app/langbot-cli/internal/deploy"
	"github.com/langbot-app/langbot-cli/internal/result"
	"github.com/langbot-app/langbot-cli/internal/telemetry"
)

func TestNativePortConflictRecovery(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	root := t.TempDir()
	t.Setenv("LANGBOT_HOME", root)
	t.Setenv("LBCTL_NO_UPDATE_CHECK", "1")
	port := strconv.Itoa(listener.Addr().(*net.TCPAddr).Port)
	args := []string{"install", "--driver", "native", "--yes", "--region", "global", "--version", "v4.11.7", "--port", port}
	for _, format := range []string{"", "json"} {
		var output bytes.Buffer
		commandArgs := append([]string{}, args...)
		if format != "" {
			commandArgs = append(commandArgs, "--output", format)
		}
		code := executeChinese(context.Background(), commandArgs, Dependencies{Out: &output, Err: io.Discard, LocalDir: filepath.Join(root, "instance")})
		if code != 6 || strings.Contains(output.String(), "context show") || !strings.Contains(output.String(), "释放冲突端口") {
			t.Fatalf("%d: %s", code, output.String())
		}
		if strings.Contains(output.String(), "<nil>") || strings.Contains(output.String(), "install.log") || strings.Contains(output.String(), "langbot.log") {
			t.Fatal(output.String())
		}
		if strings.Contains(output.String(), "使用 Docker") || strings.Contains(output.String(), "--driver docker") {
			t.Fatalf("HTTP conflict suggested Docker: %s", output.String())
		}
		if format == "" && (!strings.Contains(output.String(), "1 项失败（端口）") || !strings.Contains(output.String(), "预检阶段没有改动任何文件")) {
			t.Fatal(output.String())
		}
		if format == "json" {
			var envelope struct {
				Data map[string]any `json:"data"`
				Meta map[string]any `json:"meta"`
			}
			if err := json.Unmarshal(output.Bytes(), &envelope); err != nil {
				t.Fatal(err)
			}
			if envelope.Data["step"] != "check_ports" || envelope.Meta["next_steps"] == nil || envelope.Data["rollback_argv"] != nil {
				t.Fatal(output.String())
			}
			for _, step := range envelope.Meta["next_steps"].([]any) {
				if strings.Contains(step.(map[string]any)["description"].(string), "Workspace") {
					t.Fatal(output.String())
				}
			}
		}
	}
}

func TestRecoveryArgvParsesAndUninstallReleasesEndpoint(t *testing.T) {
	root := t.TempDir()
	t.Setenv("LANGBOT_HOME", root)
	t.Setenv("LBCTL_NO_UPDATE_CHECK", "1")
	if err := os.WriteFile(filepath.Join(root, "tools"), []byte("blocked"), 0o600); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	listener.Close()
	locator := filepath.Join(root, "locator.yaml")
	dir := filepath.Join(root, "custom instance")
	service := app.New(app.Dependencies{LocalLocatorPath: locator})
	value, err := service.Install(t.Context(), app.InstallOptions{Driver: "native", Name: "original", Dir: dir, Version: "v4.11.7", Port: port, Region: "global"})
	if err == nil {
		t.Fatal("expected runtime preparation failure")
	}
	data := value.Data.(map[string]any)
	for _, key := range []string{"retry_argv", "rollback_argv"} {
		args := data[key].([]string)
		rootCommand := newRoot(normalizeDependencies(Dependencies{}), &globalFlags{}, service)
		command, rest, err := rootCommand.Find(args)
		if err != nil {
			t.Fatal(err)
		}
		if err := command.ParseFlags(rest); err != nil {
			t.Fatalf("suggested command %v: %v", args, err)
		}
		if err := command.ValidateArgs(command.Flags().Args()); err != nil {
			t.Fatal(err)
		}
	}
	store := deploy.Store{Dir: dir}
	record, err := store.Read()
	if err != nil {
		t.Fatal(err)
	}
	record.ServiceManager = "process"
	if err := store.Write(t.Context(), record); err != nil {
		t.Fatal(err)
	}
	deps := Dependencies{Out: io.Discard, Err: io.Discard, LocalLocatorPath: locator, LookupEnv: func(string) (string, bool) { return "", false }, DefaultConfigPath: func() (string, error) { return filepath.Join(root, "config.yaml"), nil }}
	for _, args := range [][]string{data["rollback_argv"].([]string), {"uninstall", "--endpoint", record.Endpoint, "--yes"}, {"uninstall", "--name", "original", "--yes"}} {
		record.Uninstalled = false
		if err := store.Write(t.Context(), record); err != nil {
			t.Fatal(err)
		}
		if code := Execute(t.Context(), args, deps); code != 0 {
			t.Fatalf("recovery command %v exited %d", args, code)
		}
		saved, err := store.Read()
		if err != nil || !saved.Uninstalled || saved.DeploymentID != record.DeploymentID {
			t.Fatalf("uninstall did not preserve and release record: %+v: %v", saved, err)
		}
	}
	_, err = service.Install(t.Context(), app.InstallOptions{Driver: "native", Name: "replacement", Dir: filepath.Join(root, "replacement"), Version: record.CoreVersion, Port: port, Region: "global", DryRun: true})
	if err != nil {
		t.Fatalf("uninstalled endpoint still reserved: %v", err)
	}
	for _, action := range []string{"start", "stop", "restart", "uninstall"} {
		args := []string{action, "--name", "original", "--endpoint", record.Endpoint, "--yes"}
		if action != "uninstall" {
			args = args[:len(args)-1]
		}
		if code := Execute(t.Context(), args, deps); code != 2 {
			t.Fatalf("conflicting target selectors %v exited %d", args, code)
		}
	}
}

func TestNativeRegistrationConflictRecovery(t *testing.T) {
	root := t.TempDir()
	t.Setenv("LANGBOT_HOME", root)
	t.Setenv("LBCTL_NO_UPDATE_CHECK", "1")
	dir := filepath.Join(root, "instances", "default")
	record := deploy.Record{Driver: deploy.DriverNative, DeploymentID: "existing-native", Name: "default", CoreVersion: "v4.11.7", Port: 5300, Endpoint: "http://127.0.0.1:5300", DataDir: filepath.Join(dir, "data"), ServiceManager: "process"}
	if err := (deploy.Store{Dir: dir}).Write(t.Context(), record); err != nil {
		t.Fatal(err)
	}
	locator := deploy.Locator{Path: filepath.Join(root, "locator.yaml")}
	if err := locator.Write(dir); err != nil {
		t.Fatal(err)
	}
	var mutex sync.Mutex
	var events []telemetry.Event
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var event telemetry.Event
		if err := json.NewDecoder(request.Body).Decode(&event); err != nil {
			t.Error(err)
		}
		mutex.Lock()
		events = append(events, event)
		mutex.Unlock()
	}))
	defer server.Close()
	for _, dryRun := range []bool{false, true} {
		var output bytes.Buffer
		reporter := telemetry.New(telemetry.Options{Home: root, Version: "test", LookupEnv: func(key string) (string, bool) {
			return server.URL, key == "LBCTL_TELEMETRY_ENDPOINT"
		}})
		args := []string{"install", "--driver", "native", "--name", "replacement", "--yes", "--region", "global", "--version", record.CoreVersion}
		if dryRun {
			args = append(args, "--dry-run")
		}
		code := executeChinese(t.Context(), args, Dependencies{Telemetry: reporter, Out: &output, Err: io.Discard, LocalLocatorPath: locator.Path})
		reporter.Close()
		if code != 6 {
			t.Fatalf("%d: %s", code, output.String())
		}
		for _, want := range []string{"(register)", "default", record.Endpoint, "lbctl install --driver native --dir", "--name 'default'", "lbctl uninstall --dir '" + dir + "' --yes"} {
			if !strings.Contains(output.String(), want) {
				t.Fatalf("missing %s: %s", want, output.String())
			}
		}
		for _, absent := range []string{"<nil>", "install.log", "langbot.log", "预检：3 项通过"} {
			if strings.Contains(output.String(), absent) {
				t.Fatal(output.String())
			}
		}
	}
	if _, err := os.Stat(filepath.Join(root, "instances", "replacement")); !os.IsNotExist(err) {
		t.Fatalf("registration conflict created target: %v", err)
	}
	mutex.Lock()
	defer mutex.Unlock()
	failures := 0
	for _, event := range events {
		if event.Outcome == "failed" {
			failures++
			if event.Step != "register" {
				t.Fatalf("incorrect failure step: %+v", event)
			}
		}
	}
	if failures != 2 {
		t.Fatalf("missing failure events: %+v", events)
	}
}

func TestInstallServiceFailureRecoveryCommands(t *testing.T) {
	deps := normalizeDependencies(Dependencies{LookupEnv: func(string) (string, bool) { return "", false }, DefaultConfigPath: func() (string, error) { return filepath.Join(t.TempDir(), "config.yaml"), nil }})
	value := enrichResult(deps, &globalFlags{commandPath: "install"}, app.Result{Data: map[string]any{
		"step": "install_service", "retry_argv": []string{"install", "--dir", "中文 dir"}, "rollback_argv": []string{"uninstall", "--endpoint", "http://127.0.0.1:5300", "--yes"},
	}}, result.New("server", "service registration failed"))
	steps := value.Meta["next_steps"].([]map[string]any)
	if len(steps) != 2 || steps[0]["argv"].([]string)[1] != "install" || steps[1]["argv"].([]string)[1] != "uninstall" {
		t.Fatal(steps)
	}
}
