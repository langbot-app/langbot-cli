//go:build !windows

package deploy

import (
	"context"
	"os"
	"os/exec"
	"syscall"
)

func nativeStopWaiter(string) (func(context.Context) error, func(), error) {
	return func(context.Context) error { return nil }, func() {}, nil
}

func nativeRemovalRetryable(error) bool { return false }

func detachNative(command *exec.Cmd) { command.SysProcAttr = &syscall.SysProcAttr{Setsid: true} }

func releaseNativeConsole() error { return nil }

func hideNativeConsole(command *exec.Cmd) {}

func nativePIDAlive(pid int) bool { return pid > 0 && syscall.Kill(pid, 0) == nil }

func configureNativeChild(command *exec.Cmd) {
	configureCommandCancellation(command)
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	command.Cancel = func() error {
		if command.Process == nil {
			return os.ErrProcessDone
		}
		return syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
	}
}
