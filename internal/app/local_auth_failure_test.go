package app

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/gofrs/flock"
	"github.com/langbot-app/langbot-cli/internal/config"
	"github.com/langbot-app/langbot-cli/internal/deploy"
	"github.com/langbot-app/langbot-cli/internal/i18n"
	"github.com/langbot-app/langbot-cli/internal/output"
	"github.com/langbot-app/langbot-cli/internal/progress"
)

var invalidLocalAuthConfigs = map[string]string{
	"key_type": "api:\n  global_api_key: 12345\n",
	"yaml":     "api: [\n",
	"timeout":  "api: {}\n",
}

func assertLocalAuthWarning(t *testing.T, response Result, err error, action string) {
	t.Helper()
	if err != nil {
		t.Fatalf("%s failed: %v", action, err)
	}
	data := response.Data.(map[string]any)
	warning, _ := data["local_auth_error"].(string)
	if !strings.Contains(warning, "lbctl restart --dir") || data["local_auth"] != false {
		t.Fatalf("missing warning: %+v", data)
	}
	for _, format := range []string{output.FormatDefault, output.FormatJSON, output.FormatYAML} {
		var rendered bytes.Buffer
		if err := output.RenderCommand(&rendered, map[string]any{"ok": true, "data": data}, format, action); err != nil {
			t.Fatal(err)
		}
		if strings.Count(rendered.String(), "CLI") != 1 || !strings.Contains(rendered.String(), "lbctl restart --dir") {
			t.Fatalf("warning not rendered exactly once in %s: %s", format, rendered.String())
		}
	}
}

func localAuthTimeoutTransport(base http.RoundTripper) http.RoundTripper {
	return taskRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.Header.Get("X-API-Key") != "" {
			return nil, context.DeadlineExceeded
		}
		return base.RoundTrip(request)
	})
}

func TestDockerLocalAuthFailuresDoNotFailInstallOrLifecycle(t *testing.T) {
	defer i18n.Use("en")()
	for failure, content := range invalidLocalAuthConfigs {
		t.Run(failure, func(t *testing.T) {
			root := t.TempDir()
			dir := filepath.Join(root, "deployment")
			runner := &lifecycleTestRunner{state: "stopped", port: freeLifecyclePort(t), authConfig: content}
			transport := http.RoundTripper(lifecycleTransport{runner: runner})
			if failure == "timeout" {
				transport = localAuthTimeoutTransport(transport)
			}
			service := New(Dependencies{LocalDir: dir, LocalLocatorPath: filepath.Join(root, "locator.yaml"), LocalRunner: runner,
				Transport: transport, LocalHTTP: &http.Client{Transport: lifecycleTransport{runner: runner}},
				DefaultConfigPath: func() (string, error) { return filepath.Join(root, "cli.yaml"), nil },
			})
			options := InstallOptions{Version: "v4.11.7", ImageRepository: deploy.DefaultImageRepository, Profile: "basic", Port: runner.port, PluginDebugPort: new(int), AdapterPorts: []string{"none"}}
			response, err := service.Install(context.Background(), options)
			assertLocalAuthWarning(t, response, err, "install")
			if !runner.isRunning() {
				t.Fatal("installation did not start deployment")
			}
			if failure != "timeout" {
				if err := os.WriteFile(filepath.Join(dir, "data", "config.yaml"), []byte(content), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			for _, action := range []string{"restart", "start", "repair"} {
				if action == "repair" {
					response, err = service.Install(context.Background(), options)
				} else {
					response, err = service.runLifecycle(context.Background(), action, LifecycleOptions{CheckOptions: CheckOptions{LocalDir: dir}})
				}
				assertLocalAuthWarning(t, response, err, action)
				if !runner.isRunning() {
					t.Fatalf("%s stopped deployment", action)
				}
			}
		})
	}
}

type authFailureUpgradeRunner struct {
	*upgradeRunner
	content string
}

func (runner authFailureUpgradeRunner) Docker(ctx context.Context, args ...string) (deploy.CommandResult, error) {
	response, err := runner.upgradeRunner.Docker(ctx, args...)
	if err == nil && strings.Contains(strings.Join(args, " "), ",dst=/backup") {
		for _, argument := range args {
			if strings.HasPrefix(argument, "type=bind,src=") && strings.Contains(argument, ",dst=/data") {
				dir := strings.Split(strings.TrimPrefix(argument, "type=bind,src="), ",dst=")[0]
				err = os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(runner.content), 0o600)
			}
		}
	}
	return response, err
}

func TestDockerLocalAuthFailuresDoNotRequireUpgradeRecovery(t *testing.T) {
	for failure, content := range invalidLocalAuthConfigs {
		t.Run(failure, func(t *testing.T) {
			service, runner, record, dir := upgradeServiceFixture(t, false)
			service.deps.LocalRunner = authFailureUpgradeRunner{upgradeRunner: runner, content: content}
			if failure == "timeout" {
				service.deps.Transport = localAuthTimeoutTransport(service.deps.Transport)
			}
			response, err := service.Upgrade(context.Background(), UpgradeOptions{CheckOptions: CheckOptions{LocalDir: dir}, Version: "v4.10.10", Yes: true})
			assertLocalAuthWarning(t, response, err, "upgrade")
			state, err := deploy.ReadUpgradeState(dir)
			updated, readErr := (deploy.Store{Dir: dir}).Read()
			if err != nil || state.Phase != "completed" || readErr != nil || updated.CoreVersion != "v4.10.10" || !runner.running || response.Data.(map[string]any)["completed"] != true {
				t.Fatalf("upgrade did not complete: %+v, %+v, %v, %v", state, updated, err, readErr)
			}
			if updated.DeploymentID != record.DeploymentID {
				t.Fatal("deployment identity changed")
			}
		})
	}
}

func nativeAuthFailureFixture(t *testing.T, content string) (*Service, deploy.Record, string) {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, "instance")
	t.Setenv("HOME", root)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("LANGBOT_HOME", root)
	record := deploy.Record{Driver: deploy.DriverNative, DeploymentID: "local-auth-failure", Name: "default", CoreVersion: "v4.11.7", Port: freeLifecyclePort(t),
		DataDir: filepath.Join(dir, "data"), ToolsDir: filepath.Join(root, "tools"), ServiceManager: "systemd-user", Region: "global"}
	record.Endpoint = fmt.Sprintf("http://127.0.0.1:%d", record.Port)
	deploy.ApplyRegion(&record)
	if err := (deploy.Store{Dir: dir}).Write(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(root, "bin")
	uv := filepath.Join(record.ToolsDir, "uv", deploy.UVVersion)
	for _, path := range []string{bin, uv, record.DataDir} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for path, text := range map[string]string{
		filepath.Join(bin, "systemctl"):              "#!/bin/sh\nfor argument in \"$@\"; do\ncase \"$argument\" in\nstart) printf '%s' \"$TEST_PID\" > \"$TEST_INSTANCE/native.pid\";;\nstop) rm -f \"$TEST_INSTANCE/native.pid\";;\nesac\ndone\n",
		filepath.Join(uv, "uv"):                      "#!/bin/sh\nexit 0\n",
		filepath.Join(record.DataDir, "config.yaml"): content,
	} {
		if err := os.WriteFile(path, []byte(text), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("TEST_PID", fmt.Sprint(os.Getpid()))
	t.Setenv("TEST_INSTANCE", dir)
	lock := flock.New(filepath.Join(dir, "native.run.lock"))
	if err := lock.Lock(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lock.Unlock() })
	transport := taskRoundTripFunc(func(*http.Request) (*http.Response, error) {
		return taskResponse(`{"code":0,"data":{"version":"v4.11.7","edition":"community","instance_uuid":"instance-local","workspace_uuid":"workspace-local","api_key_id":"global","permissions":[],"schema_version":1,"operations":{}}}`), nil
	})
	service := New(Dependencies{LocalLocatorPath: filepath.Join(root, "locator.yaml"), Transport: transport, LocalHTTP: &http.Client{Transport: transport},
		DefaultConfigPath: func() (string, error) { return filepath.Join(root, "cli.yaml"), nil },
	})
	return service, record, dir
}

func TestNativeLocalAuthFailuresDoNotStopLifecycle(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX service fixture")
	}
	for failure, content := range invalidLocalAuthConfigs {
		for _, action := range []string{"install", "repair", "start", "restart", "upgrade"} {
			t.Run(failure+"/"+action, func(t *testing.T) {
				service, record, dir := nativeAuthFailureFixture(t, content)
				if failure == "timeout" {
					service.deps.Transport = localAuthTimeoutTransport(service.deps.Transport)
				}
				if action != "install" && action != "start" {
					if err := os.WriteFile(filepath.Join(dir, "native.pid"), []byte(fmt.Sprint(os.Getpid())), 0o600); err != nil {
						t.Fatal(err)
					}
				}
				var response Result
				var err error
				switch action {
				case "install", "repair":
					response, err = service.installNative(context.Background(), InstallOptions{Dir: dir, Version: record.CoreVersion, Port: record.Port, Region: "global"})
				case "upgrade":
					response, err = service.Upgrade(context.Background(), UpgradeOptions{CheckOptions: CheckOptions{LocalDir: dir}, Version: "v4.11.8", Yes: true})
				default:
					response, err = service.runLifecycle(context.Background(), action, LifecycleOptions{CheckOptions: CheckOptions{LocalDir: dir}})
				}
				assertLocalAuthWarning(t, response, err, action)
				if !deploy.NativeRunning(dir) {
					t.Fatal("authentication failure left native deployment stopped")
				}
				if action == "upgrade" {
					updated, err := (deploy.Store{Dir: dir}).Read()
					if err != nil || updated.CoreVersion != "v4.11.8" || response.Data.(map[string]any)["completed"] != true {
						t.Fatalf("upgrade rolled back: %+v, %v", updated, err)
					}
				}
			})
		}
	}
}

func TestLocalAuthBindingMismatchIsOnlyAWarning(t *testing.T) {
	service, _, record, dir := upgradeServiceFixture(t, false)
	auth := localAuthAttempt{service: service, dir: dir}
	auth.configure(context.Background(), record)
	if err := service.update(func(file *config.File) error {
		selected := file.Contexts[record.DeploymentID]
		selected.ExpectedWorkspaceUUID = "another-workspace"
		file.Contexts[record.DeploymentID] = selected
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	auth.bind(context.Background(), record)
	assertLocalAuthWarning(t, auth.result(Result{Data: map[string]any{"action": "restart"}}), nil, "restart")
}

func TestLocalAuthBindingTimeoutLeavesOperationContextUsable(t *testing.T) {
	service, _, record, dir := upgradeServiceFixture(t, false)
	service.deps.Transport = taskRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		<-request.Context().Done()
		return nil, request.Context().Err()
	})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	auth := localAuthAttempt{service: service, dir: dir}
	auth.configure(ctx, record)
	auth.bind(ctx, record)
	if ctx.Err() != nil {
		t.Fatal("identity timeout cancelled the lifecycle context")
	}
	assertLocalAuthWarning(t, auth.result(Result{Data: map[string]any{"action": "install"}}), nil, "install")
}

func TestUpgradeDryRunDoesNotBindLocalAuth(t *testing.T) {
	service, _, record, dir := upgradeServiceFixture(t, false)
	if _, err := service.configureLocalAuth(context.Background(), dir, record); err != nil {
		t.Fatal(err)
	}
	base := service.deps.Transport
	service.deps.Transport = taskRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.Header.Get("X-API-Key") != "" {
			t.Fatal("upgrade dry-run attempted credential binding")
		}
		return base.RoundTrip(request)
	})
	if _, err := service.Upgrade(context.Background(), UpgradeOptions{CheckOptions: CheckOptions{LocalDir: dir}, Version: "v4.10.10", DryRun: true}); err != nil {
		t.Fatal(err)
	}
}

func TestAdoptLocalAuthWarningAndRestartHintAppearOnce(t *testing.T) {
	for _, language := range []string{i18n.English, i18n.Chinese} {
		t.Run(language, func(t *testing.T) {
			defer i18n.Use(language)()
			for _, content := range []string{"api: {}\n", invalidLocalAuthConfigs["key_type"], invalidLocalAuthConfigs["yaml"]} {
				root := t.TempDir()
				composeFile := filepath.Join(root, "compose.yaml")
				if err := os.WriteFile(composeFile, []byte("services:\n  langbot:\n    image: rockchin/langbot:v4.10.10\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.MkdirAll(filepath.Join(root, "data"), 0o700); err != nil {
					t.Fatal(err)
				}
				runner := &lifecycleTestRunner{state: "running", port: freeLifecyclePort(t), authConfig: content}
				service := New(Dependencies{LocalDir: filepath.Join(root, "record"), LocalLocatorPath: filepath.Join(root, "locator.yaml"), LocalRunner: runner,
					Transport: lifecycleTransport{runner: runner}, DefaultConfigPath: func() (string, error) { return filepath.Join(root, "cli.yaml"), nil },
				})
				var rendered bytes.Buffer
				display := progress.New(&rendered, func(string) (string, bool) { return "", false }, false, false)
				response, err := service.Adopt(progress.WithContext(context.Background(), display), AdoptOptions{ComposeFile: composeFile, ComposeProject: "langbot", Endpoint: fmt.Sprintf("http://127.0.0.1:%d", runner.port)})
				display.Finish(err == nil)
				if err != nil {
					t.Fatal(err)
				}
				if err := output.RenderCommand(&rendered, map[string]any{"ok": true, "data": response.Data}, output.FormatDefault, "adopt"); err != nil {
					t.Fatal(err)
				}
				if strings.Count(rendered.String(), "lbctl restart --dir") != 1 {
					t.Fatalf("activation or warning printed more than once: %s", rendered.String())
				}
				if content != "api: {}\n" {
					assertLocalAuthWarning(t, response, err, "adopt")
					prefix := "Warning:"
					if language == i18n.Chinese {
						prefix = "警告："
					}
					if !strings.Contains(rendered.String(), prefix) {
						t.Fatalf("warning not translated: %s", rendered.String())
					}
				}
				for _, call := range runner.calls {
					if call == "compose restart" || call == "compose up -d" {
						t.Fatal("adopt restarted deployment")
					}
				}
			}
		})
	}
}
