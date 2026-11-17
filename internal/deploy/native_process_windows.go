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
	"unsafe"

	"github.com/langbot-app/langbot-cli/internal/i18n"
	"golang.org/x/sys/windows"
)

func nativeRemovalRetryable(err error) bool {
	return errors.Is(err, windows.ERROR_ACCESS_DENIED) || errors.Is(err, windows.ERROR_SHARING_VIOLATION) || errors.Is(err, windows.ERROR_LOCK_VIOLATION)
}

func nativeStopWaiter(dir string) (func(context.Context) error, func(), error) {
	var handles []windows.Handle
	closeHandles := func() {
		for _, handle := range handles {
			windows.CloseHandle(handle)
		}
	}
	wait := func(ctx context.Context) error {
		return waitNativeExit(ctx, func() (bool, error) {
			for _, handle := range handles {
				status, err := windows.WaitForSingleObject(handle, 0)
				if err != nil {
					return false, err
				}
				if status != windows.WAIT_OBJECT_0 {
					return false, nil
				}
			}
			return true, nil
		})
	}
	data, err := os.ReadFile(filepath.Join(dir, "native.pid"))
	if os.IsNotExist(err) {
		return wait, closeHandles, nil
	}
	if err != nil {
		return nil, nil, err
	}
	pid, err := strconv.ParseUint(strings.TrimSpace(string(data)), 10, 32)
	if err != nil || pid == 0 {
		return nil, nil, fmt.Errorf(i18n.T("invalid native supervisor PID: %s"), strings.TrimSpace(string(data)))
	}
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil, nil, err
	}
	defer windows.CloseHandle(snapshot)
	children := make(map[uint32][]uint32)
	entry := windows.ProcessEntry32{Size: uint32(unsafe.Sizeof(windows.ProcessEntry32{}))}
	for err = windows.Process32First(snapshot, &entry); err == nil; err = windows.Process32Next(snapshot, &entry) {
		children[entry.ParentProcessID] = append(children[entry.ParentProcessID], entry.ProcessID)
	}
	if !errors.Is(err, windows.ERROR_NO_MORE_FILES) {
		return nil, nil, err
	}
	pending := []uint32{uint32(pid)}
	seen := make(map[uint32]bool)
	for len(pending) > 0 {
		current := pending[0]
		pending = pending[1:]
		if seen[current] {
			continue
		}
		seen[current] = true
		pending = append(pending, children[current]...)
		handle, err := windows.OpenProcess(windows.SYNCHRONIZE, false, current)
		if errors.Is(err, windows.ERROR_INVALID_PARAMETER) {
			continue
		}
		if err != nil {
			closeHandles()
			return nil, nil, err
		}
		handles = append(handles, handle)
	}
	return wait, closeHandles, nil
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
