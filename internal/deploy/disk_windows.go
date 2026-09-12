//go:build windows

package deploy

import (
	"path/filepath"

	"golang.org/x/sys/windows"
)

func availableBytes(path string) (uint64, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return 0, err
	}
	value, err := windows.UTF16PtrFromString(absolute)
	if err != nil {
		return 0, err
	}
	var available uint64
	if err := windows.GetDiskFreeSpaceEx(value, &available, nil, nil); err != nil {
		return 0, err
	}
	return available, nil
}
