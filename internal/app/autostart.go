package app

import (
	"context"
	"time"

	"github.com/langbot-app/langbot-cli/internal/deploy"
	"github.com/langbot-app/langbot-cli/internal/result"
)

func (s *Service) Autostart(ctx context.Context, action, name, dir, timeout string) (Result, error) {
	if action != "status" && action != "enable" && action != "disable" {
		return Result{}, result.New("input", "unknown autostart action")
	}
	if name != "" && dir != "" {
		return Result{}, result.New("input", "--name and --dir are mutually exclusive")
	}
	var err error
	if name != "" {
		dir, err = deploy.InstanceDir(name)
	} else {
		dir, err = s.resolveLocalDir(dir)
	}
	if err != nil {
		return Result{}, err
	}
	duration, err := operationTimeout(timeout, timeout != "", 30*time.Second)
	if err != nil {
		return Result{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, duration)
	defer cancel()
	store := deploy.Store{Dir: dir}
	var info deploy.AutostartInfo
	err = deploy.WithMutationLock(ctx, dir, func() error {
		record, err := store.Read()
		if err != nil {
			return err
		}
		if action != "status" {
			if err := deploy.ChangeAutostart(ctx, dir, record, action == "enable", s.localRunner()); err != nil {
				return err
			}
		}
		info, err = deploy.AutostartStatus(ctx, record)
		return err
	})
	if err != nil {
		return Result{}, nativeMutationError(err)
	}
	data := map[string]any{"action": "autostart", "autostart": info.Enabled, "supported": info.Supported, "mechanism": info.Mechanism, "location": info.Location, "instance_dir": dir}
	return Result{Data: data}, nil
}

func installAutostartData(data map[string]any, record deploy.Record, dir string) {
	enabled := record.ServiceManager != "process"
	if record.Autostart != nil {
		enabled = *record.Autostart
	}
	mechanism := record.ServiceManager
	if record.Driver == deploy.DriverDockerCompose {
		mechanism = "Docker restart: unless-stopped"
	}
	if mechanism == "windows-run" {
		mechanism = "HKCU Run"
	}
	command := "lbctl autostart disable"
	if record.Name != "" && record.Name != "default" {
		command += " --name " + nativeShellQuote(record.Name)
	} else if defaultDir, err := deploy.InstanceDir("default"); err != nil || defaultDir != dir {
		command += " --dir " + nativeShellQuote(dir)
	}
	data["autostart"], data["autostart_mechanism"], data["autostart_disable_command"] = enabled, mechanism, command
	if mechanism == "systemd-user" {
		data["autostart_note"] = "loginctl enable-linger"
	}
}
