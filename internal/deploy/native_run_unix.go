//go:build !windows

package deploy

import "fmt"

func NativeInstallManager(record Record) string { return record.ServiceManager }

func nativeRunEnabled(record Record) (bool, error) {
	return false, fmt.Errorf("windows-run requires Windows")
}

func installNativeRun(record Record, binary, dir string) error {
	return fmt.Errorf("windows-run requires Windows")
}

func removeNativeRun(record Record) error {
	return fmt.Errorf("windows-run requires Windows")
}
