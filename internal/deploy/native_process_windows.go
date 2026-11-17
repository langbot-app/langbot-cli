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
	data, err := os.ReadFile(filepath.Join(dir, "native.pid"))
	if os.IsNotExist(err) {
		return func(context.Context) error { return nil }, func() {}, nil
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
	return nativeProcessWaiter(uint32(pid), children)
}

func nativeProcessWaiter(pid uint32, children map[uint32][]uint32) (func(context.Context) error, func(), error) {
	type processHandle struct {
		pid    uint32
		handle windows.Handle
	}
	var handles []processHandle
	closeHandles := func() {
		for _, process := range handles {
			windows.CloseHandle(process.handle)
		}
	}
	wait := func(ctx context.Context) error {
		var pendingPID uint32
		err := waitNativeExit(ctx, func() (bool, error) {
			for _, process := range handles {
				status, err := windows.WaitForSingleObject(process.handle, 0)
				if err != nil {
					return false, err
				}
				if status != windows.WAIT_OBJECT_0 {
					pendingPID = process.pid
					return false, nil
				}
			}
			return true, nil
		})
		if err != nil && pendingPID != 0 {
			return fmt.Errorf(i18n.T("LangBot process %d has not exited: %w"), pendingPID, err)
		}
		return err
	}
	type candidate struct {
		pid           uint32
		parentStarted int64
	}
	pending := []candidate{{pid: pid}}
	seen := make(map[uint32]bool)
	for len(pending) > 0 {
		current := pending[0]
		pending = pending[1:]
		if seen[current.pid] {
			continue
		}
		seen[current.pid] = true
		handle, err := windows.OpenProcess(windows.SYNCHRONIZE|windows.PROCESS_QUERY_LIMITED_INFORMATION, false, current.pid)
		if errors.Is(err, windows.ERROR_INVALID_PARAMETER) {
			continue
		}
		if err != nil {
			closeHandles()
			return nil, nil, err
		}
		var created, exited, kernel, user windows.Filetime
		if err := windows.GetProcessTimes(handle, &created, &exited, &kernel, &user); err != nil {
			windows.CloseHandle(handle)
			closeHandles()
			return nil, nil, err
		}
		if created.Nanoseconds() < current.parentStarted {
			windows.CloseHandle(handle)
			continue
		}
		handles = append(handles, processHandle{pid: current.pid, handle: handle})
		for _, child := range children[current.pid] {
			pending = append(pending, candidate{pid: child, parentStarted: created.Nanoseconds()})
		}
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
