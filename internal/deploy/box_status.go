package deploy

import (
	"bytes"
	"context"
	"github.com/langbot-app/langbot-cli/internal/i18n"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
)

type BoxSettings struct {
	Backend     string `json:"backend"`
	Enabled     bool   `json:"enabled"`
	Unsafe      bool   `json:"unsafe"`
	WarningCode string `json:"warning_code,omitempty"`
}

func NativeBoxSettings(record Record) (BoxSettings, bool) {
	if record.Driver != DriverNative {
		return BoxSettings{}, false
	}
	return InstalledBoxSettings(record)
}

func InstalledBoxSettings(record Record) (BoxSettings, bool) {
	if record.DataDir == "" {
		return BoxSettings{}, false
	}
	raw, err := os.ReadFile(filepath.Join(record.DataDir, "config.yaml"))
	if err != nil {
		return BoxSettings{}, false
	}
	config, err := ParseSystemConfig(raw)
	if err != nil {
		return BoxSettings{}, false
	}
	box := BoxSettings{Backend: "local", Enabled: true}
	if node := config.Node("box.backend"); node != nil {
		box.Backend = node.Value
	}
	if node := config.Node("box.enabled"); node != nil && node.Decode(&box.Enabled) != nil {
		return BoxSettings{}, false
	}
	if record.Driver == DriverDockerCompose {
		box.Enabled = record.Profile == "all" || record.Profile == "box"
	} else {
		if backend, ok := os.LookupEnv("BOX__BACKEND"); ok {
			box.Backend = backend
		}
		if value, ok := os.LookupEnv("BOX__ENABLED"); ok {
			box.Enabled, err = strconv.ParseBool(value)
			if err != nil {
				return BoxSettings{}, false
			}
		}
	}
	box.Unsafe = box.Enabled && box.Backend == "host"
	if box.Unsafe {
		box.WarningCode = "box_host_unsafe"
	}
	return box, true
}

func SelectInstallBox(ctx context.Context, dir string, record Record, runner Runner, backend string) (bool, error) {
	if backend == "" {
		return false, nil
	}
	raw, missing, err := ReadSystemConfig(ctx, dir, record, runner)
	if err != nil {
		return false, err
	}
	config, err := ParseSystemConfig(raw)
	if err != nil {
		return false, err
	}
	before, err := config.Bytes()
	if err != nil {
		return false, err
	}
	if err = config.Set("box.enabled", strconv.FormatBool(backend != "off")); err != nil {
		return false, err
	}
	if backend != "off" {
		if err = config.Set("box.backend", backend); err != nil {
			return false, err
		}
	}
	if record.Driver == DriverNative && runtime.GOOS == "windows" && backend == "host" {
		if err := config.Set("box.file_transfer_mode", "legacy"); err != nil {
			return false, err
		}
	}
	updated, err := config.Bytes()
	if err != nil {
		return false, err
	}
	if !missing && bytes.Equal(before, updated) {
		return false, nil
	}
	return true, WriteSystemConfig(ctx, dir, record, runner, updated)
}

func (box BoxSettings) Warning(version string) string {
	if !box.Unsafe {
		return ""
	}
	if !WindowsHostBoxSupported(runtime.GOOS, version) {
		return i18n.T("This Windows Core version does not support host Box. Box is unavailable until Core is upgraded.")
	}
	return i18n.T("WARNING: Host Box has no sandbox. Agent commands, skills and stdio MCP run as your system user, with access to your files and network. Untrusted users in a group chat can use prompt injection to trigger dangerous commands.")
}
