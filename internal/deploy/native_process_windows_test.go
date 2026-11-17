package deploy

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

func TestNativeStopWaiterTracksChildAfterSupervisorExit(t *testing.T) {
	dir := t.TempDir()
	childPath := filepath.Join(dir, "child.pid")
	command := exec.Command(os.Args[0], "-test.run=^TestNativeWindowsCancellationTree$")
	command.Env = append(os.Environ(), "LBCTL_TREE_HELPER=parent", "LBCTL_TREE_PID="+childPath)
	hideNativeConsole(command)
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = command.Process.Kill()
		_ = command.Wait()
	}()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	var childPID int
	if err := waitNativeExit(ctx, func() (bool, error) {
		data, _ := os.ReadFile(childPath)
		childPID, _ = strconv.Atoi(strings.TrimSpace(string(data)))
		return childPID > 0, nil
	}); err != nil {
		t.Fatal(err)
	}
	child, err := os.FindProcess(childPID)
	if err != nil {
		t.Fatal(err)
	}
	defer child.Release()
	defer child.Kill()
	pidPath := filepath.Join(dir, "native.pid")
	if err := os.WriteFile(pidPath, []byte(strconv.Itoa(command.Process.Pid)), 0o600); err != nil {
		t.Fatal(err)
	}
	wait, cleanup, err := nativeStopWaiter(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	staleWait, staleCleanup, err := nativeProcessWaiter(uint32(command.Process.Pid), time.Now().UnixNano(), map[uint32][]uint32{
		uint32(command.Process.Pid): {uint32(os.Getpid())},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer staleCleanup()
	if err := command.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if err := staleWait(ctx); err != nil {
		t.Fatalf("waited for an older process with a reused parent PID: %v", err)
	}
	if err := os.Remove(pidPath); err != nil {
		t.Fatal(err)
	}
	short, stop := context.WithTimeout(ctx, 200*time.Millisecond)
	defer stop()
	if err := wait(short); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("wait returned while child was alive: %v", err)
	}
	if err := child.Kill(); err != nil {
		t.Fatal(err)
	}
	if err := wait(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestNativeStopIgnoresReusedRootPID(test *testing.T) {
	dir := test.TempDir()
	path := filepath.Join(dir, "native.pid")
	if err := os.WriteFile(path, []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
		test.Fatal(err)
	}
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(os.Getpid()))
	if err != nil {
		test.Fatal(err)
	}
	defer windows.CloseHandle(handle)
	var created, exited, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(handle, &created, &exited, &kernel, &user); err != nil {
		test.Fatal(err)
	}
	mtime := time.Unix(0, created.Nanoseconds()).Add(-time.Second)
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		test.Fatal(err)
	}
	wait, cleanup, err := nativeStopWaiter(dir)
	if err != nil {
		test.Fatal(err)
	}
	defer cleanup()
	ctx, cancel := context.WithTimeout(test.Context(), 200*time.Millisecond)
	defer cancel()
	if err := wait(ctx); err != nil {
		test.Fatalf("waited for an unrelated process: %v", err)
	}
	if err := NativeStop(ctx, dir, Record{ServiceManager: "process"}); err != nil {
		test.Fatalf("stale PID prevented stop: %v", err)
	}
}

func TestNativeRemovalRetryable(t *testing.T) {
	for _, code := range []error{windows.ERROR_ACCESS_DENIED, windows.ERROR_SHARING_VIOLATION, windows.ERROR_LOCK_VIOLATION} {
		if !nativeRemovalRetryable(&os.PathError{Op: "remove", Path: "parser.pyd", Err: code}) {
			t.Fatalf("not retryable: %v", code)
		}
	}
	if nativeRemovalRetryable(windows.ERROR_PATH_NOT_FOUND) {
		t.Fatal("unexpected retry")
	}
}

func TestRemoveNativeFilesRetriesSharingViolation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "parser.pyd")
	if err := os.WriteFile(path, []byte("loaded extension"), 0o600); err != nil {
		t.Fatal(err)
	}
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		t.Fatal(err)
	}
	handle, err := windows.CreateFile(name, windows.GENERIC_READ, windows.FILE_SHARE_READ, nil, windows.OPEN_EXISTING, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if handle != 0 {
			windows.CloseHandle(handle)
		}
	}()
	calls := 0
	err = removeNativeFiles(t.Context(), dir, func(path string) error {
		calls++
		err := os.RemoveAll(path)
		if calls == 1 {
			if !nativeRemovalRetryable(err) {
				t.Fatalf("expected locked file: %v", err)
			}
			windows.CloseHandle(handle)
			handle = 0
		}
		return err
	}, nativeRemovalRetryable, time.Second)
	if err != nil || calls != 2 {
		t.Fatalf("calls=%d err=%v", calls, err)
	}
}

func TestNativeWindowsRemovalWaitsForFileUnlock(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "_http_parser.pyd")
	if err := os.WriteFile(path, []byte("library"), 0o600); err != nil {
		t.Fatal(err)
	}
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		t.Fatal(err)
	}
	handle, err := windows.CreateFile(name, windows.GENERIC_READ, windows.FILE_SHARE_READ, nil, windows.OPEN_EXISTING, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	released := make(chan struct{})
	go func() { time.Sleep(450 * time.Millisecond); windows.CloseHandle(handle); close(released) }()
	defer func() { <-released }()
	if err := RemoveNativeFiles(t.Context(), dir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal(err)
	}
	for _, code := range []error{windows.ERROR_ACCESS_DENIED, windows.ERROR_SHARING_VIOLATION} {
		if !nativeRemovalRetryable(&os.PathError{Op: "remove", Path: path, Err: code}) {
			t.Fatal(code)
		}
	}
	if nativeRemovalRetryable(errors.New("unrelated")) {
		t.Fatal("retried unrelated error")
	}
}

func TestNativeWindowsConsole(t *testing.T) {
	if mode := os.Getenv("LBCTL_CONSOLE_HELPER"); mode != "" {
		console := windows.NewLazySystemDLL("kernel32.dll").NewProc("GetConsoleCP")
		before, _, _ := console.Call()
		if mode == "attached" && before == 0 {
			t.Fatal("helper did not receive a console")
		}
		if mode == "child" {
			window, _, _ := windows.NewLazySystemDLL("kernel32.dll").NewProc("GetConsoleWindow").Call()
			visible, _, _ := windows.NewLazySystemDLL("user32.dll").NewProc("IsWindowVisible").Call(window)
			if visible != 0 {
				t.Fatal("native child received a visible console window")
			}
		}
		if err := releaseNativeConsole(); err != nil {
			t.Fatal(err)
		}
		if after, _, _ := console.Call(); after != 0 {
			t.Fatal("supervisor retained a console")
		}
		return
	}
	for _, mode := range []string{"attached", "detached", "child"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestNativeWindowsConsole$")
			command.Env = append(os.Environ(), "LBCTL_CONSOLE_HELPER="+mode)
			switch mode {
			case "attached":
				command.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NEW_CONSOLE, HideWindow: true}
			case "detached":
				detachNative(command)
			case "child":
				configureNativeChild(command)
				if command.SysProcAttr.CreationFlags&windows.CREATE_NO_WINDOW == 0 {
					t.Fatal("native child is missing CREATE_NO_WINDOW")
				}
			}
			if output, err := command.CombinedOutput(); err != nil {
				t.Fatalf("%v: %s", err, output)
			}
		})
	}
}

func TestNativeWindowsCancellationTree(t *testing.T) {
	if mode := os.Getenv("LBCTL_TREE_HELPER"); mode != "" {
		if mode == "parent" {
			child := exec.Command(os.Args[0], "-test.run=^TestNativeWindowsCancellationTree$")
			child.Env = append(os.Environ(), "LBCTL_TREE_HELPER=child")
			if err := child.Start(); err != nil {
				os.Exit(2)
			}
			_ = child.Wait()
			return
		}
		if err := os.WriteFile(os.Getenv("LBCTL_TREE_PID"), []byte(fmt.Sprint(os.Getpid())), 0o600); err != nil {
			os.Exit(3)
		}
		time.Sleep(time.Minute)
		return
	}
	pidPath := filepath.Join(t.TempDir(), "child.pid")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestNativeWindowsCancellationTree$")
	command.Env = append(os.Environ(), "LBCTL_TREE_HELPER=parent", "LBCTL_TREE_PID="+pidPath)
	configureNativeChild(command)
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	waited := false
	defer func() {
		cancel()
		if !waited {
			_ = command.Wait()
		}
	}()
	deadline := time.Now().Add(10 * time.Second)
	var childPID int
	for time.Now().Before(deadline) {
		data, _ := os.ReadFile(pidPath)
		childPID, _ = strconv.Atoi(strings.TrimSpace(string(data)))
		if childPID > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if childPID == 0 {
		t.Fatal("child did not start")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "native.pid"), []byte(strconv.Itoa(command.Process.Pid)), 0o600); err != nil {
		t.Fatal(err)
	}
	wait, closeHandles, err := nativeStopWaiter(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer closeHandles()
	cancel()
	_ = command.Wait()
	waited = true
	waitCtx, waitCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer waitCancel()
	if err := wait(waitCtx); err != nil {
		t.Fatal(err)
	}
	if nativePIDAlive(childPID) {
		t.Fatal("Python-style subprocess tree survived cancellation")
	}
}

func TestMissingScheduledTaskRetryUsesWindowsRun(t *testing.T) {
	record := Record{ServiceManager: "schtasks", DeploymentID: "test-missing-" + fmt.Sprint(os.Getpid())}
	if manager := NativeInstallManager(record); manager != "windows-run" {
		t.Fatalf("failed install retained %s", manager)
	}
}
