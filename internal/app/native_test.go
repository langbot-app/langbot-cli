package app

import (
	"context"
	"fmt"
	"github.com/langbot-app/langbot-cli/internal/deploy"
	"github.com/langbot-app/langbot-cli/internal/result"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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
	if data["step"] != "prepare_runtime" {
		t.Fatalf("%+v: %v", data, err)
	}
	log, readErr := os.ReadFile(data["log_path"].(string))
	if readErr != nil || !strings.Contains(string(log), "prepare_runtime") {
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
	if err == nil || data["step"] != "prepare_runtime" || data["version"] != record.CoreVersion {
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
