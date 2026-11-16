package deploy

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/gofrs/flock"
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
	tail, err := runNativeCommand(command, &output)
	if err == nil || !strings.Contains(err.Error(), filepath.Base(os.Args[0])) || !strings.HasSuffix(err.Error(), "Access is denied.") || len(tail) != 2048 {
		t.Fatalf("tail=%d err=%v", len(tail), err)
	}
	if !strings.Contains(output.String(), "stdout preserved") || len(output.String()) < 4096 {
		t.Fatal("stream was truncated")
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
