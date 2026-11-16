package deploy

import (
	"fmt"
	"os/exec"
	"syscall"

	"github.com/langbot-app/langbot-cli/internal/i18n"
	"golang.org/x/sys/windows"
)

func releaseNativeConsole() error {
	result, _, err := windows.NewLazySystemDLL("kernel32.dll").NewProc("FreeConsole").Call()
	if result == 0 {
		return fmt.Errorf(i18n.T("FreeConsole: %w"), err)
	}
	return nil
}

func hideNativeConsole(command *exec.Cmd) {
	if command.SysProcAttr == nil {
		command.SysProcAttr = &syscall.SysProcAttr{}
	}
	command.SysProcAttr.CreationFlags |= windows.CREATE_NO_WINDOW
}

func detachNative(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NEW_PROCESS_GROUP | windows.DETACHED_PROCESS}
}

func nativePIDAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return false
	}
	defer windows.CloseHandle(handle)
	var code uint32
	return windows.GetExitCodeProcess(handle, &code) == nil && code == 259
}

func configureNativeChild(command *exec.Cmd) {
	configureCommandCancellation(command)
	hideNativeConsole(command)
}
