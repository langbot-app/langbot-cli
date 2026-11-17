package deploy

import (
	"context"
	"encoding/binary"
	"encoding/xml"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
	"unicode/utf16"

	"github.com/langbot-app/langbot-cli/internal/i18n"
	"go.yaml.in/yaml/v3"
)

type AutostartInfo struct {
	Enabled   bool   `json:"enabled"`
	Supported bool   `json:"supported"`
	Mechanism string `json:"mechanism"`
	Location  string `json:"location"`
}

func AutostartStatus(ctx context.Context, record Record) (AutostartInfo, error) {
	info := AutostartInfo{Supported: true, Mechanism: record.ServiceManager}
	if record.Driver == DriverDockerCompose {
		info.Mechanism, info.Location = "Docker restart policy", record.ComposeFile
		content, err := os.ReadFile(record.ComposeFile)
		if err != nil {
			return info, err
		}
		var config struct {
			Services map[string]struct {
				Restart string `yaml:"restart"`
			} `yaml:"services"`
		}
		if err := yaml.Unmarshal(content, &config); err != nil {
			return info, err
		}
		info.Enabled = len(config.Services) > 0
		for _, service := range config.Services {
			if service.Restart != "unless-stopped" && service.Restart != "always" {
				info.Enabled = false
			}
		}
		return info, nil
	}
	path, err := nativeServicePath(record)
	if err != nil {
		return info, err
	}
	info.Location = path
	switch record.ServiceManager {
	case "windows-run":
		info.Mechanism = "HKCU Run"
		info.Location = `HKCU\Software\Microsoft\Windows\CurrentVersion\Run\` + nativeServiceName(record)
		info.Enabled, err = nativeRunEnabled(record)
	case "systemd", "systemd-user":
		args := []string{"is-enabled", nativeServiceName(record)}
		if record.ServiceManager == "systemd-user" {
			args = append([]string{"--user"}, args...)
		}
		var output string
		output, err = runNativeCommand(ctx, exec.CommandContext(ctx, "systemctl", args...), nil)
		switch strings.TrimSpace(output) {
		case "enabled", "enabled-runtime":
			info.Enabled, err = true, nil
		case "disabled", "not-found", "masked", "static":
			err = nil
		}
	case "launchd":
		_, err = os.Stat(path)
		info.Enabled = err == nil
		if os.IsNotExist(err) {
			err = nil
		}
	case "schtasks":
		info.Location = `Task Scheduler\` + nativeServiceName(record)
		command := exec.CommandContext(ctx, "schtasks.exe", "/Query", "/TN", nativeServiceName(record), "/XML")
		var output strings.Builder
		_, err = runNativeCommand(ctx, command, &output)
		if err == nil {
			var task struct {
				Enabled bool `xml:"Settings>Enabled"`
			}
			err = xml.Unmarshal(scheduledTaskXML([]byte(output.String())), &task)
			info.Enabled = task.Enabled
		}
	case "process":
		info.Supported = false
	default:
		return info, fmt.Errorf(i18n.T("unknown native service manager %q"), record.ServiceManager)
	}
	return info, err
}

func scheduledTaskXML(data []byte) []byte {
	if len(data) >= 2 && (data[1] == 0 || (data[0] == 0xff && data[1] == 0xfe)) {
		if data[0] == 0xff {
			data = data[2:]
		}
		units := make([]uint16, len(data)/2)
		for index := range units {
			units[index] = binary.LittleEndian.Uint16(data[index*2:])
		}
		data = []byte(string(utf16.Decode(units)))
	}
	return []byte(strings.Replace(string(data), `encoding="UTF-16"`, `encoding="UTF-8"`, 1))
}

func nativeAutostartToggle(ctx context.Context, record Record, enabled bool) *exec.Cmd {
	if record.ServiceManager == "schtasks" {
		action := "/Disable"
		if enabled {
			action = "/Enable"
		}
		return exec.CommandContext(ctx, "schtasks.exe", "/Change", "/TN", nativeServiceName(record), action)
	}
	action := "disable"
	if enabled {
		action = "enable"
	}
	args := []string{action, nativeServiceName(record)}
	if record.ServiceManager == "systemd-user" {
		args = append([]string{"--user"}, args...)
	}
	return exec.CommandContext(ctx, "systemctl", args...)
}

func setNativeAutostart(ctx context.Context, dir string, record Record, enabled bool) error {
	record.Autostart = &enabled
	switch record.ServiceManager {
	case "process":
		if enabled {
			return errors.New(i18n.T("process manager does not support autostart; configure a platform service manager"))
		}
		return nil
	case "schtasks":
		_, err := runNativeCommand(ctx, nativeAutostartToggle(ctx, record, enabled), nil)
		return err
	default:
		return InstallNativeService(ctx, dir, record)
	}
}

func composeAutostart(content []byte, enabled *bool) []byte {
	if enabled == nil {
		return content
	}
	policy := "unless-stopped"
	if !*enabled {
		policy = `"no"`
	}
	text := string(content)
	for _, previous := range []string{"on-failure", "unless-stopped", `"no"`} {
		text = strings.ReplaceAll(text, "    restart: "+previous+"\n", "    restart: "+policy+"\n")
	}
	return []byte(text)
}

func ChangeAutostart(ctx context.Context, dir string, record Record, enabled bool, runner Runner) error {
	previous, err := AutostartStatus(ctx, record)
	if err != nil {
		return err
	}
	next := record
	next.Autostart = &enabled
	if record.Driver != DriverDockerCompose {
		if err := setNativeAutostart(ctx, dir, record, enabled); err != nil {
			return err
		}
		if err := (Store{Dir: dir}).Write(ctx, next); err != nil {
			recoveryCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
			defer cancel()
			rollbackErr := setNativeAutostart(recoveryCtx, dir, record, previous.Enabled)
			if rollbackErr != nil {
				return fmt.Errorf(i18n.T("save autostart: %w; rollback: %v"), err, rollbackErr)
			}
			return fmt.Errorf(i18n.T("save autostart: %w"), err)
		}
		return nil
	}
	if err := ValidateOwnedLayout(record, dir); err != nil {
		return fmt.Errorf(i18n.T("autostart only modifies lbctl-generated Compose files: %w"), err)
	}
	content, err := os.ReadFile(record.ComposeFile)
	if err != nil {
		return err
	}
	containers, err := runner.Compose(ctx, record, "ps", "--all", "--quiet")
	if err != nil {
		return dockerProblem(containers, err)
	}
	update := func(ctx context.Context, policy string) error {
		ids := strings.Fields(containers.Stdout)
		if len(ids) == 0 {
			return nil
		}
		command, err := runner.Docker(ctx, append([]string{"update", "--restart=" + policy}, ids...)...)
		if err != nil {
			return dockerProblem(command, err)
		}
		return nil
	}
	updated := composeAutostart(content, &enabled)
	if err := writeAtomic(record.ComposeFile, updated, 0o600); err != nil {
		return err
	}
	policy := "no"
	if enabled {
		policy = "unless-stopped"
	}
	err = update(ctx, policy)
	if err == nil {
		err = (Store{Dir: dir}).Write(ctx, next)
	}
	if err != nil {
		restoreErr := writeAtomic(record.ComposeFile, content, 0o600)
		oldPolicy := "on-failure"
		if record.Autostart != nil {
			oldPolicy = "no"
			if *record.Autostart {
				oldPolicy = "unless-stopped"
			}
		}
		recoveryCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		rollbackErr := update(recoveryCtx, oldPolicy)
		if restoreErr != nil {
			err = fmt.Errorf(i18n.T("%w; restore Compose: %v"), err, restoreErr)
		}
		if rollbackErr != nil {
			err = fmt.Errorf(i18n.T("%w; restore policy: %v"), err, rollbackErr)
		}
		return fmt.Errorf(i18n.T("autostart: %w"), err)
	}
	return nil
}
