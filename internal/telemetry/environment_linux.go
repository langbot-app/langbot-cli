package telemetry

import (
	"os"
	"strings"

	"golang.org/x/sys/unix"
)

func systemEnvironment(dir string) map[string]any {
	result := map[string]any{"is_admin": os.Geteuid() == 0, "is_wsl": false, "is_container": false}
	if data, err := os.ReadFile("/etc/os-release"); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			key, value, _ := strings.Cut(line, "=")
			if key == "ID" || key == "VERSION_ID" {
				result[strings.ToLower(key)] = strings.Trim(value, `"'`)
			}
		}
	}
	if data, err := os.ReadFile("/proc/sys/kernel/osrelease"); err == nil {
		result["is_wsl"] = strings.Contains(strings.ToLower(string(data)), "microsoft")
	}
	_, docker := os.Stat("/.dockerenv")
	_, podman := os.Stat("/run/.containerenv")
	cgroup, _ := os.ReadFile("/proc/1/cgroup")
	result["is_container"] = docker == nil || podman == nil || strings.Contains(string(cgroup), "docker") || strings.Contains(string(cgroup), "kubepods") || os.Getenv("container") != ""
	var memory unix.Sysinfo_t
	if unix.Sysinfo(&memory) == nil {
		result["memory_bytes"] = uint64(memory.Totalram) * uint64(memory.Unit)
	}
	var disk unix.Statfs_t
	if unix.Statfs(dir, &disk) == nil {
		result["disk_free_bytes"] = disk.Bavail * uint64(disk.Bsize)
	}
	return result
}
