package app

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/langbot-app/langbot-cli/internal/deploy"
	"github.com/langbot-app/langbot-cli/internal/i18n"
	"github.com/langbot-app/langbot-cli/internal/result"
)

type SystemConfigOptions struct {
	CheckOptions
	Action, Key, Value       string
	DryRun, Restart, Secrets bool
}

func (service *Service) SystemConfig(ctx context.Context, options SystemConfigOptions) (Result, error) {
	record, diagnostics, err := service.lifecycleTarget(ctx, options.CheckOptions)
	if err != nil {
		return Result{}, err
	}
	path := filepath.Join(record.DataDir, "config.yaml")
	if options.Action == "path" {
		return Result{Data: path}, nil
	}
	var response Result
	var restart bool
	perform := func() error {
		current, err := diagnostics.Store.Read()
		if err != nil || current.DeploymentID != record.DeploymentID {
			return result.New("precondition", i18n.T("Local deployment binding changed"))
		}
		raw, missing, err := deploy.ReadSystemConfig(ctx, diagnostics.Dir, current, service.localRunner())
		if err != nil {
			return result.New("precondition", i18n.T("Cannot read system configuration"))
		}
		config, err := deploy.ParseSystemConfig(raw)
		if err != nil {
			return result.New("input", err.Error())
		}
		if options.Action == "get" {
			if config.Node(options.Key) == nil {
				return result.New("input", i18n.T("System configuration key does not exist"))
			}
			response.Data = config.Value(options.Key, options.Secrets)
			return nil
		}
		old := config.Value(options.Key, false)
		existing := config.Node(options.Key) != nil
		warnings := []string{}
		var updated []byte
		switch options.Action {
		case "set":
			err = config.Set(options.Key, options.Value)
			if !existing && !deploy.KnownSystemKey(options.Key) {
				warnings = append(warnings, i18n.T("Unknown system configuration key"))
			}
		case "unset":
			err = config.Unset(options.Key)
		case "edit":
			updated, err = service.editSystemConfig(ctx, raw)
			if err == nil {
				config, err = deploy.ParseSystemConfig(updated)
			}
		default:
			return result.New("input", i18n.T("Invalid system configuration key"))
		}
		if err != nil {
			return result.New("input", err.Error())
		}
		if updated == nil {
			updated, err = config.Bytes()
			if err != nil {
				return err
			}
		}
		overrides, err := deploy.SystemConfigOverrides(ctx, current, service.localRunner(), options.Key)
		if err != nil {
			return err
		}
		if len(overrides) > 0 {
			warnings = append(warnings, fmt.Sprintf(i18n.T("Environment overrides take precedence over config.yaml: %s"), strings.Join(overrides, ", ")))
			if current.Driver == deploy.DriverDockerCompose && (options.Key == "box.enabled" || options.Key == "") {
				warnings = append(warnings, i18n.T("For Compose, set LANGBOT_BOX_ENABLED in .env, select the matching Box profile, and recreate the services with docker compose up -d."))
			}
		}
		changed := missing || !bytes.Equal(raw, updated)
		data := map[string]any{"path": path, "key": options.Key, "old": old, "new": config.Value(options.Key, false), "restart_required": changed, "restarted": false, "overridden_by_env": len(overrides) > 0, "warnings": warnings}
		response.Data = data
		if options.DryRun {
			data["dry_run"] = true
			return nil
		}
		if !changed {
			return nil
		}
		if err := deploy.WriteSystemConfig(ctx, diagnostics.Dir, current, service.localRunner(), updated); err != nil {
			return err
		}
		status, err := diagnostics.Status(ctx)
		if err == nil && (status.Status == "running" || status.Status == "degraded") {
			restart = options.Restart
			data["restart_command"] = "lbctl restart --dir " + nativeShellQuote(diagnostics.Dir)
		}
		return nil
	}
	if options.Action == "get" {
		err = perform()
	} else {
		err = deploy.WithMutationLock(ctx, diagnostics.Dir, perform)
	}
	if err == nil && restart {
		_, err = service.Restart(ctx, LifecycleOptions{CheckOptions: CheckOptions{LocalDir: diagnostics.Dir, Timeout: options.Timeout, TimeoutSet: options.TimeoutSet}})
		if err == nil {
			response.Data.(map[string]any)["restart_required"] = false
			response.Data.(map[string]any)["restarted"] = true
		}
	}
	return response, nativeMutationError(err)
}

func (service *Service) editSystemConfig(ctx context.Context, raw []byte) ([]byte, error) {
	editor, _ := service.deps.LookupEnv("EDITOR")
	if editor == "" {
		editor = "vi"
		if runtime.GOOS == "windows" {
			editor = "notepad"
		}
	}
	file, err := os.CreateTemp("", "lbctl-config-*.yaml")
	if err != nil {
		return nil, err
	}
	defer os.Remove(file.Name())
	_, err = file.Write(raw)
	closeErr := file.Close()
	if err != nil {
		return nil, err
	}
	if closeErr != nil {
		return nil, closeErr
	}
	args := []string{file.Name()}
	if _, err := exec.LookPath(editor); err != nil {
		parts := strings.Fields(editor)
		if len(parts) == 0 {
			return nil, fmt.Errorf("%s", i18n.T("Editor failed"))
		}
		editor = parts[0]
		args = append(parts[1:], file.Name())
	}
	command := exec.CommandContext(ctx, editor, args...)
	command.Stdin = service.deps.In
	command.Stdout, command.Stderr = service.deps.LocalLogErr, service.deps.LocalLogErr
	if err := command.Run(); err != nil {
		return nil, fmt.Errorf("%s", i18n.T("Editor failed"))
	}
	return os.ReadFile(file.Name())
}
