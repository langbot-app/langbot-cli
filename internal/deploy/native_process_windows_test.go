package deploy

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

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
	cancel()
	_ = command.Wait()
	waited = true
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
