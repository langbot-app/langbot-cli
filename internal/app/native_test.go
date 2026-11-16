package app

import (
	"context"
	"fmt"
	"github.com/langbot-app/langbot-cli/internal/deploy"
	"github.com/langbot-app/langbot-cli/internal/result"
	"github.com/langbot-app/langbot-cli/internal/telemetry"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestNativePackageFailureStepMatchesTelemetry(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture")
	}
	for _, enabled := range []bool{false, true} {
		t.Run(fmt.Sprint(enabled), func(t *testing.T) {
			root := t.TempDir()
			t.Setenv("LANGBOT_HOME", root)
			uvDir := filepath.Join(root, "tools", "uv", deploy.UVVersion)
			if err := os.MkdirAll(uvDir, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(uvDir, "uv"), []byte("#!/bin/sh\nif [ \"$1\" = pip ]; then echo package-failed >&2; exit 1; fi\nexit 0\n"), 0o700); err != nil {
				t.Fatal(err)
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) }))
			defer server.Close()
			client := telemetry.New(telemetry.Options{Home: t.TempDir(), LookupEnv: func(key string) (string, bool) {
				if key == "LANGBOT_TELEMETRY" && !enabled {
					return "0", true
				}
				if key == "LBCTL_TELEMETRY_ENDPOINT" {
					return server.URL, true
				}
				return "", false
			}})
			defer client.Close()
			ctx, operation := client.Begin(context.Background(), "install")
			service := New(Dependencies{LocalLocatorPath: filepath.Join(root, "locator.yaml")})
			value, err := service.installNative(ctx, InstallOptions{Dir: filepath.Join(root, "instance"), Version: "v4.11.7", Port: freeLifecyclePort(t), Region: "global"})
			if err == nil {
				t.Fatal("expected package failure")
			}
			data := value.Data.(map[string]any)
			if data["step"] != "install_package" || enabled && operation.Event.Step != data["step"] {
				t.Fatalf("inconsistent package failure: %+v, %+v", data, operation)
			}
		})
	}
}

func TestNativeMutationErrorClassification(t *testing.T) {
	for _, test := range []struct {
		err  error
		want string
	}{
		{fmt.Errorf("uv: %w", deploy.ErrNativeDependency), "dependency"},
		{fmt.Errorf("%w: %w", deploy.ErrNativeDependency, deploy.ErrNativeInterrupted), "interrupted"},
		{fmt.Errorf("uv: %w", context.Canceled), "interrupted"},
		{fmt.Errorf("uv: %w", context.DeadlineExceeded), "interrupted"},
	} {
		if got := result.AsError(nativeMutationError(test.err)).Type; got != test.want {
			t.Fatalf("%v: %s != %s", test.err, got, test.want)
		}
	}
}

func TestNativeInstallFailureDiagnostics(t *testing.T) {
	root := t.TempDir()
	t.Setenv("LANGBOT_HOME", root)
	if err := os.WriteFile(filepath.Join(root, "tools"), []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	service := New(Dependencies{LocalLocatorPath: filepath.Join(root, "locator.yaml")})
	dir := filepath.Join(root, "中文 instance")
	value, err := service.installNative(context.Background(), InstallOptions{
		Dir: dir, Version: "v4.11.7", Port: freeLifecyclePort(t), Region: "global",
	})
	if err == nil {
		t.Fatal("expected prepare failure")
	}
	data := value.Data.(map[string]any)
	if data["step"] != "download_uv" {
		t.Fatalf("%+v: %v", data, err)
	}
	log, readErr := os.ReadFile(data["log_path"].(string))
	if readErr != nil || !strings.Contains(string(log), "download_uv") {
		t.Fatalf("%s: %v", log, readErr)
	}
	if !strings.Contains(data["retry_command"].(string), nativeShellQuote(dir)) || !strings.Contains(data["rollback_command"].(string), "--dir") {
		t.Fatalf("invalid recovery commands: %+v", data)
	}
	if data["runtime_log_path"] != nil {
		t.Fatalf("reported nonexistent runtime log: %+v", data)
	}
	record, readErr := (deploy.Store{Dir: dir}).Read()
	if readErr != nil {
		t.Fatal(readErr)
	}
	value, err = service.installNative(context.Background(), InstallOptions{Dir: dir, Port: record.Port, Region: "global"})
	data = value.Data.(map[string]any)
	if err == nil || data["step"] != "download_uv" || data["version"] != record.CoreVersion {
		t.Fatalf("retry did not resume recorded version: %+v: %v", data, err)
	}
	saved, readErr := (deploy.Store{Dir: dir}).Read()
	if readErr != nil || saved.DeploymentID != record.DeploymentID || saved.DataDir != record.DataDir {
		t.Fatalf("retry replaced deployment: %+v: %v", saved, readErr)
	}
}

func TestInstallAutostartMetadata(t *testing.T) {
	for _, manager := range []string{"windows-run", "systemd-user", "launchd", "process"} {
		data := map[string]any{}
		record := deploy.Record{Driver: deploy.DriverNative, Name: "mybot", ServiceManager: manager}
		installAutostartData(data, record, t.TempDir())
		if data["autostart"] != (manager != "process") || !strings.Contains(data["autostart_disable_command"].(string), "--name 'mybot'") {
			t.Fatal(data)
		}
		if manager == "windows-run" && data["autostart_mechanism"] != "HKCU Run" {
			t.Fatal(data)
		}
	}
}
