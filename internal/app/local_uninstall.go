package app

import (
	"context"
	"errors"
	"path/filepath"
	"time"

	"github.com/langbot-app/langbot-cli/internal/deploy"
	"github.com/langbot-app/langbot-cli/internal/i18n"
	"github.com/langbot-app/langbot-cli/internal/progress"
	"github.com/langbot-app/langbot-cli/internal/result"
	"github.com/langbot-app/langbot-cli/internal/telemetry"
)

type UninstallOptions struct {
	CheckOptions
	PurgeData bool
	Yes       bool
}

func (s *Service) Uninstall(ctx context.Context, options UninstallOptions) (Result, error) {
	if !options.Yes {
		return Result{}, result.New("precondition", i18n.T("Uninstall requires explicit --yes"))
	}
	timeout, err := operationTimeout(options.Timeout, options.TimeoutSet, 2*time.Minute)
	if err != nil {
		return Result{}, err
	}
	operationCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	record, diagnostics, err := s.lifecycleTarget(operationCtx, options.CheckOptions)
	if err != nil {
		return Result{}, err
	}
	var response Result
	total := 3
	if record.Driver == deploy.DriverNative && !record.Uninstalled {
		total = 5
	}
	if options.PurgeData {
		total++
	}
	progress.Plan(ctx, total)
	if record.Driver == deploy.DriverNative {
		err := deploy.WithMutationLock(operationCtx, diagnostics.Dir, func() error {
			current, err := diagnostics.Store.Read()
			if err != nil {
				return err
			}
			if current.DeploymentID != record.DeploymentID {
				return result.New("precondition", i18n.T("deployment changed"))
			}
			if current.Uninstalled && !options.PurgeData {
				return nil
			}
			if !current.Uninstalled {
				telemetry.SetStep(ctx, "stop_service")
				if err := deploy.NativeStop(operationCtx, diagnostics.Dir, current); err != nil {
					return err
				}
				telemetry.SetStep(ctx, "remove_service")
				if err := deploy.RemoveNativeService(operationCtx, current); err != nil {
					return err
				}
			}
			if options.PurgeData {
				telemetry.SetStep(ctx, "purge_data")
				if current.DataDir != filepath.Join(diagnostics.Dir, "data") {
					return result.New("precondition", i18n.T("invalid data directory"))
				}
				if err := deploy.RemoveNativeFiles(operationCtx, current.DataDir); err != nil {
					return err
				}
			}
			telemetry.SetStep(ctx, "remove_venvs")
			if err := deploy.RemoveNativeFiles(operationCtx, filepath.Join(diagnostics.Dir, "venvs")); err != nil {
				return err
			}
			current.Uninstalled = true
			return diagnostics.Store.Write(operationCtx, current)
		})
		return Result{Data: map[string]any{"action": "uninstall", "driver": record.Driver, "data_preserved": !options.PurgeData,
			"backups_preserved": true, "record_preserved": true, "project_removed": err == nil, "purge_data": options.PurgeData}}, nativeMutationError(err)
	}
	err = deploy.WithMutationLock(operationCtx, diagnostics.Dir, func() error {
		if err := deploy.CheckUpgradeInterrupted(diagnostics.Dir); err != nil {
			return result.New("recovery_required", err.Error())
		}
		current, readErr := diagnostics.Store.Read()
		if readErr != nil || current.DeploymentID != record.DeploymentID {
			return result.New("precondition", i18n.T("Local deployment binding changed"))
		}
		if options.PurgeData {
			if err := deploy.ValidatePurgeData(diagnostics.Dir, current); err != nil {
				return result.New("precondition", err.Error())
			}
		}
		before, err := composeContainers(operationCtx, diagnostics.Runner, current)
		if err != nil {
			return result.New("precondition", i18n.T("Unable to confirm container status for managed Compose project"))
		}
		data := map[string]any{
			"action": "uninstall", "changed": len(before) > 0, "project": current.ComposeProject,
			"purge_data": options.PurgeData, "data_dir": current.DataDir,
			"record_preserved": true, "backups_preserved": true,
		}
		response = Result{Data: data}
		telemetry.SetStep(ctx, "compose_down")
		command, commandErr := diagnostics.Runner.Compose(operationCtx, current, "down", "--remove-orphans")
		readbackCtx, readbackCancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer readbackCancel()
		after, readbackErr := composeContainers(readbackCtx, diagnostics.Runner, current)
		if readbackErr != nil {
			return result.New("result_unknown", i18n.T("Uninstall ran but Compose project readback failed"))
		}
		if len(after) != 0 {
			if commandErr != nil {
				return uncertainDockerError(command, commandErr, i18n.T("Unable to confirm uninstall result"))
			}
			return result.New("result_unknown", i18n.T("Uninstall ran but managed containers still exist"))
		}
		data["project_removed"] = true
		if options.PurgeData {
			telemetry.SetStep(ctx, "purge_data")
			purged, recoveryPath, purgeErr := deploy.PurgeData(diagnostics.Dir, current)
			data["data_purged"] = purged
			if recoveryPath != "" {
				data["recovery_path"] = recoveryPath
			}
			if purgeErr != nil {
				return result.New("recovery_required", i18n.T("Data cleanup incomplete; inspect preserved cleanup state"))
			}
			if purged {
				data["changed"] = true
			}
		}
		return nil
	})
	return response, normalizeLocalMutationError(err)
}

func composeContainers(ctx context.Context, runner deploy.Runner, record deploy.Record) ([]deploy.Container, error) {
	command, err := runner.Compose(ctx, record, "ps", "-a", "--format", "json")
	if err != nil {
		return nil, err
	}
	containers, err := deploy.ParseContainers(command.Stdout)
	if err != nil {
		return nil, errors.New(i18n.T("Invalid Compose container status format"))
	}
	return containers, nil
}
