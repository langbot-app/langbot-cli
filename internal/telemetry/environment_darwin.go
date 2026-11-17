package telemetry

import (
	"golang.org/x/sys/unix"
	"os"
)

func systemEnvironment(dir string) map[string]any {
	result := map[string]any{"is_admin": os.Geteuid() == 0, "is_wsl": false, "is_container": false}
	if memory, err := unix.SysctlUint64("hw.memsize"); err == nil {
		result["memory_bytes"] = memory
	}
	var disk unix.Statfs_t
	if unix.Statfs(dir, &disk) == nil {
		result["disk_free_bytes"] = disk.Bavail * uint64(disk.Bsize)
	}
	return result
}
