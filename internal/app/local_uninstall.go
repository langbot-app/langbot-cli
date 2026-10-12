package app

import (
	"context"
	"errors"
	"time"

	"github.com/langbot-app/langbot-cli/internal/deploy"
	"github.com/langbot-app/langbot-cli/internal/result"
)

type UninstallOptions struct {
	CheckOptions
	PurgeData bool
	Yes       bool
}

func (s *Service) Uninstall(ctx context.Context, options UninstallOptions) (Result, error) {
	if !options.Yes {
		return Result{}, result.New("precondition", "卸载需要显式 --yes")
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
	err = deploy.WithMutationLock(operationCtx, diagnostics.Dir, func() error {
		if err := deploy.CheckUpgradeInterrupted(diagnostics.Dir); err != nil {
			return result.New("recovery_required", err.Error())
		}
		current, readErr := diagnostics.Store.Read()
		if readErr != nil || current.DeploymentID != record.DeploymentID {
			return result.New("precondition", "本机部署绑定已变更")
		}
		if options.PurgeData {
			if err := deploy.ValidatePurgeData(diagnostics.Dir, current); err != nil {
				return result.New("precondition", err.Error())
			}
		}
		before, err := composeContainers(operationCtx, diagnostics.Runner, current)
		if err != nil {
			return result.New("precondition", "无法确认受管 Compose project 的容器状态")
		}
		data := map[string]any{
			"action": "uninstall", "changed": len(before) > 0, "project": current.ComposeProject,
			"purge_data": options.PurgeData, "data_dir": current.DataDir,
			"record_preserved": true, "backups_preserved": true,
		}
		response = Result{Data: data}
		command, commandErr := diagnostics.Runner.Compose(operationCtx, current, "down", "--remove-orphans")
		readbackCtx, readbackCancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer readbackCancel()
		after, readbackErr := composeContainers(readbackCtx, diagnostics.Runner, current)
		if readbackErr != nil {
			return result.New("result_unknown", "卸载命令已执行，但无法回读 Compose project")
		}
		if len(after) != 0 {
			if commandErr != nil {
				return uncertainDockerError(command, commandErr, "卸载命令结果无法确认")
			}
			return result.New("result_unknown", "卸载命令已执行，但受管容器仍然存在")
		}
		data["project_removed"] = true
		if options.PurgeData {
			purged, recoveryPath, purgeErr := deploy.PurgeData(diagnostics.Dir, current)
			data["data_purged"] = purged
			if recoveryPath != "" {
				data["recovery_path"] = recoveryPath
			}
			if purgeErr != nil {
				return result.New("recovery_required", "数据清理未完成，请检查保留的清理现场")
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
		return nil, errors.New("Compose 容器状态格式无效")
	}
	return containers, nil
}
