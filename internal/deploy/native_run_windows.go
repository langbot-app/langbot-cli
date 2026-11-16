package deploy

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

const nativeRunPath = `Software\Microsoft\Windows\CurrentVersion\Run`

func NativeInstallManager(record Record) string {
	if record.ServiceManager == "schtasks" {
		if directory, err := windows.GetSystemDirectory(); err == nil {
			if _, err := os.Stat(filepath.Join(directory, "Tasks", nativeServiceName(record))); os.IsNotExist(err) {
				return "windows-run"
			}
		}
	}
	return record.ServiceManager
}

func nativeRunEnabled(record Record) (bool, error) {
	key, err := registry.OpenKey(registry.CURRENT_USER, nativeRunPath, registry.QUERY_VALUE)
	if errors.Is(err, registry.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer key.Close()
	value, _, err := key.GetStringValue(nativeServiceName(record))
	if errors.Is(err, registry.ErrNotExist) {
		return false, nil
	}
	return value != "", err
}

func installNativeRun(record Record, binary, dir string) error {
	key, _, err := registry.CreateKey(registry.CURRENT_USER, nativeRunPath, registry.SET_VALUE)
	if err != nil {
		return fmt.Errorf("HKCU Run: %w", err)
	}
	defer key.Close()
	if err := writeNativeRun(key, record, binary, dir); err != nil {
		return fmt.Errorf("HKCU Run: %w", err)
	}
	return nil
}

func removeNativeRun(record Record) error {
	key, err := registry.OpenKey(registry.CURRENT_USER, nativeRunPath, registry.SET_VALUE)
	if errors.Is(err, registry.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("HKCU Run: %w", err)
	}
	defer key.Close()
	if err := deleteNativeRun(key, record); err != nil {
		return fmt.Errorf("HKCU Run: %w", err)
	}
	return nil
}
