package telemetry

import (
	"fmt"
	"golang.org/x/sys/windows"
	"os"
	"os/exec"
	"syscall"
	"unsafe"
)

func systemEnvironment(dir string) map[string]any {
	version := windows.RtlGetVersion()
	result := map[string]any{"os_version": fmt.Sprintf("%d.%d.%d", version.MajorVersion, version.MinorVersion, version.BuildNumber), "is_admin": windows.GetCurrentProcessToken().IsElevated(), "is_wsl": false, "is_container": false}
	result["is_container"] = os.Getenv("CONTAINER_SANDBOX_MOUNT_POINT") != ""
	var memory struct {
		Length, Load                                                                                                  uint32
		TotalPhysical, AvailablePhysical, TotalPage, AvailablePage, TotalVirtual, AvailableVirtual, AvailableExtended uint64
	}
	memory.Length = uint32(unsafe.Sizeof(memory))
	if ok, _, _ := windows.NewLazySystemDLL("kernel32.dll").NewProc("GlobalMemoryStatusEx").Call(uintptr(unsafe.Pointer(&memory))); ok != 0 {
		result["memory_bytes"] = memory.TotalPhysical
	}
	if path, err := windows.UTF16PtrFromString(dir); err == nil {
		var available, total, free uint64
		if windows.GetDiskFreeSpaceEx(path, &available, &total, &free) == nil {
			result["disk_free_bytes"] = available
		}
	}
	return result
}

func configureDiagnosticCommand(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: windows.CREATE_NO_WINDOW}
}
