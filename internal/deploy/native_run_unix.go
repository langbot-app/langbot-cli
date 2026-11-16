//go:build !windows

package deploy

import (
	"errors"
	"github.com/langbot-app/langbot-cli/internal/i18n"
)

func NativeInstallManager(record Record) string { return record.ServiceManager }

func nativeRunEnabled(record Record) (bool, error) {
	return false, errors.New(i18n.T("windows-run requires Windows"))
}

func installNativeRun(record Record, binary, dir string) error {
	return errors.New(i18n.T("windows-run requires Windows"))
}

func removeNativeRun(record Record) error {
	return errors.New(i18n.T("windows-run requires Windows"))
}
