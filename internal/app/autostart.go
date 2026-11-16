package app

import (
	"context"
	"errors"
	"time"

	"github.com/langbot-app/langbot-cli/internal/deploy"
	"github.com/langbot-app/langbot-cli/internal/i18n"
	"github.com/langbot-app/langbot-cli/internal/result"
	"github.com/langbot-app/langbot-cli/internal/telemetry"
)

func (s *Service) namedLocalDir(name, dir string) (string, error) {
	if name != "" && dir != "" {
		return "", result.New("input", "--name and --dir are mutually exclusive")
	}
	if name == "" {
		return s.resolveLocalDir(dir)
	}
	fallback, err := deploy.InstanceDir(name)
	if err != nil {
		return "", result.New("input", err.Error())
	}
	dirs, err := (deploy.Locator{Path: s.deps.LocalLocatorPath}).Directories()
	if err != nil {
		return "", err
	}
	matched := ""
	for _, candidate := range dirs {
		record, readErr := (deploy.Store{Dir: candidate}).Read()
		if errors.Is(readErr, deploy.ErrNotInstalled) || errors.Is(readErr, deploy.ErrNotManaged) {
			continue
		}
		if readErr != nil {
			return "", readErr
		}
		if record.Name == name {
			if matched != "" && matched != candidate {
				return "", result.New("precondition", "multiple local deployments have this name; use --dir")
			}
			matched = candidate
		}
	}
	if matched != "" {
		return matched, nil
	}
	return fallback, nil
}

func (s *Service) Autostart(ctx context.Context, action, name, dir, timeout string) (Result, error) {
	telemetry.SetStep(ctx, "resolve_target")
	if action != "status" && action != "enable" && action != "disable" {
		return Result{}, result.New("input", i18n.T("unknown autostart action"))
	}
	dir, err := s.namedLocalDir(name, dir)
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
		telemetry.SetEnvironment(ctx, record.Driver, record.ServiceManager, record.Region, record.CoreVersion)
		telemetry.SetStep(ctx, "autostart_"+action)
		if record.Uninstalled {
			if action != "status" {
				return result.New("precondition", "deployment is uninstalled; reinstall before changing autostart")
			}
			info = deploy.AutostartInfo{Enabled: false, Supported: record.ServiceManager != "process", Mechanism: record.ServiceManager}
			return nil
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
