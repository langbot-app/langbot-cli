package deploy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/gofrs/flock"
	"github.com/langbot-app/langbot-cli/internal/telemetry"
)

type fakeNativeRunKey struct {
	name, value string
	err         error
}

func (key *fakeNativeRunKey) SetStringValue(name, value string) error {
	key.name, key.value = name, value
	return key.err
}

func (key *fakeNativeRunKey) DeleteValue(name string) error {
	key.name = name
	return key.err
}

func TestNativeRunRegistration(t *testing.T) {
	key := &fakeNativeRunKey{}
	record := Record{DeploymentID: "test-id"}
	err := writeNativeRun(key, record, `C:\Users\中文 用户\lbctl.exe`, `C:\Users\中文 用户\LangBot\`)
	if err != nil || key.name != "langbot-test-id" || key.value != `"C:\Users\中文 用户\lbctl.exe" _native-run "C:\Users\中文 用户\LangBot\\"` {
		t.Fatalf("%+v: %v", key, err)
	}
	key.err = errors.New("access denied")
	if err := writeNativeRun(key, record, "lbctl.exe", "instance"); !errors.Is(err, key.err) {
		t.Fatal(err)
	}
	if err := writeNativeRun(key, record, "lbctl.exe", strings.Repeat("long", 100)); err == nil || !strings.Contains(err.Error(), "260") {
		t.Fatal(err)
	}
}

func TestNativeRunRemoval(t *testing.T) {
	for _, failure := range []error{nil, os.ErrNotExist, os.ErrPermission} {
		key := &fakeNativeRunKey{err: failure}
		err := deleteNativeRun(key, Record{DeploymentID: "test-id"})
		if key.name != "langbot-test-id" || (err != nil) != errors.Is(failure, os.ErrPermission) {
			t.Fatalf("%+v: %v", key, err)
		}
	}
}

func TestSelectNativeManagerWindows(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows manager selection")
	}
	t.Setenv("PATH", t.TempDir())
	if got := SelectNativeManager(context.Background()); got != "windows-run" {
		t.Fatal(got)
	}
}

func TestNativeCommandOutput(t *testing.T) {
	if os.Getenv("LBCTL_COMMAND_HELPER") == "1" {
		fmt.Fprint(os.Stdout, "stdout preserved\n")
		fmt.Fprint(os.Stderr, strings.Repeat("x", 4096)+"Access is denied.")
		os.Exit(1)
	}
	command := exec.CommandContext(context.Background(), os.Args[0], "-test.run=^TestNativeCommandOutput$")
	command.Env = append(os.Environ(), "LBCTL_COMMAND_HELPER=1")
	var output bytes.Buffer
	tail, err := runNativeCommand(context.Background(), command, &output)
	if err == nil || !strings.Contains(err.Error(), filepath.Base(os.Args[0])) || !strings.HasSuffix(err.Error(), "Access is denied.") || len(tail) != 2048 {
		t.Fatalf("tail=%d err=%v", len(tail), err)
	}
	if !strings.Contains(output.String(), "stdout preserved") || len(output.String()) < 4096 {
		t.Fatal("stream was truncated")
	}
}

func TestNativeCommandSignalIsInterrupted(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix signal termination")
	}
	command := exec.CommandContext(t.Context(), "sh", "-c", "kill -TERM $$")
	_, err := runNativeCommand(context.Background(), command, nil)
	if !errors.Is(err, ErrNativeInterrupted) {
		t.Fatalf("signal termination not classified: %v", err)
	}
}

func TestNativeCommandDiagnostics(t *testing.T) {
	events := make(chan telemetry.Event, 2)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var event telemetry.Event
		if err := json.NewDecoder(request.Body).Decode(&event); err != nil {
			t.Error(err)
		}
		events <- event
	}))
	defer server.Close()
	client := telemetry.New(telemetry.Options{Home: t.TempDir(), LookupEnv: func(key string) (string, bool) {
		if key == "LBCTL_TELEMETRY_ENDPOINT" {
			return server.URL, true
		}
		return "", false
	}})
	ctx, operation := client.Begin(t.Context(), "install")
	telemetry.SetStep(ctx, "install_package")
	command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestNativeCommandOutput$")
	command.Env = append(os.Environ(), "LBCTL_COMMAND_HELPER=1")
	_, err := runNativeCommand(ctx, command, nil)
	operation.Finish(err, "dependency")
	client.Close()
	close(events)
	found := false
	for event := range events {
		if event.Outcome != "failed" {
			continue
		}
		found = true
		process, ok := event.Details["process"].(map[string]any)
		if !ok || process["exit_code"] != float64(1) || process["stdout"] != "stdout preserved\n" || !strings.HasSuffix(process["stderr"].(string), "Access is denied.") {
			t.Fatalf("subprocess details: %+v", event.Details)
		}
	}
	if !found {
		t.Fatal("missing failure")
	}
}

func TestNativePIDAndLock(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "native.pid"), []byte(fmt.Sprint(os.Getpid())), 0o600); err != nil {
		t.Fatal(err)
	}
	if NativeRunning(dir) {
		t.Fatal("stale PID without lock considered running")
	}
	lock := flock.New(filepath.Join(dir, "native.run.lock"))
	if err := lock.Lock(); err != nil {
		t.Fatal(err)
	}
	defer lock.Unlock()
	if !NativeRunning(dir) {
		t.Fatal("live supervisor not detected")
	}
}

func TestNativePythonAndUTF8(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "中文 用户")
	want := filepath.Join(dir, "venvs", "v4.11.7", "bin", "python")
	if runtime.GOOS == "windows" {
		want = filepath.Join(dir, "venvs", "v4.11.7", "Scripts", "python.exe")
	}
	if got := NativePython(dir, "v4.11.7"); got != want {
		t.Fatal(got)
	}
	env := strings.Join(NativeEnvironment(Record{DataDir: filepath.Join(dir, "data")}), "\n")
	for _, value := range []string{"PYTHONUTF8=1", "PYTHONIOENCODING=utf-8"} {
		if !strings.Contains(env, value) {
			t.Fatal(env)
		}
	}
}

func TestNativeSystemdWorkingDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "中文 user %")
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	unit := nativeSystemdUnit(binary, dir, Record{ServiceManager: "systemd-user"})
	if !strings.Contains(unit, "WorkingDirectory="+strings.ReplaceAll(dir, "%", "%%")+"\n") || strings.Contains(unit, `WorkingDirectory="`) {
		t.Fatal(unit)
	}
	analyzer, err := exec.LookPath("systemd-analyze")
	if err != nil {
		return
	}
	path := filepath.Join(t.TempDir(), "langbot-verify.service")
	if err := os.WriteFile(path, []byte(unit), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if output, err := exec.CommandContext(ctx, analyzer, "verify", path).CombinedOutput(); err != nil {
		t.Fatalf("%s: %v", output, err)
	}
}
