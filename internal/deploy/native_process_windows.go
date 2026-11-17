package deploy

import (
	"errors"
	"fmt"
	"os/exec"
	"syscall"
	"unsafe"

	"github.com/langbot-app/langbot-cli/internal/i18n"
	"golang.org/x/sys/windows"
)

func nativeProcessTree(pid int) ([]int, error) {
	if !nativePIDAlive(pid) {
		return nil, nil
	}
	handle, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil, err
	}
	defer windows.CloseHandle(handle)
	entry := windows.ProcessEntry32{Size: uint32(unsafe.Sizeof(windows.ProcessEntry32{}))}
	parents := map[int]int{}
	err = windows.Process32First(handle, &entry)
	for err == nil {
		parents[int(entry.ProcessID)] = int(entry.ParentProcessID)
		err = windows.Process32Next(handle, &entry)
	}
	if !errors.Is(err, windows.ERROR_NO_MORE_FILES) {
		return nil, err
	}
	processes := []int{pid}
	for index := 0; index < len(processes); index++ {
		for child, parent := range parents {
			if parent == processes[index] && child != pid {
				processes = append(processes, child)
				delete(parents, child)
			}
		}
	}
	return processes, nil
}

func retryNativeRemoval(err error) bool {
	return errors.Is(err, windows.ERROR_ACCESS_DENIED) || errors.Is(err, windows.ERROR_SHARING_VIOLATION)
}

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
