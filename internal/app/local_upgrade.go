package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/langbot-app/langbot-cli/internal/api"
	"github.com/langbot-app/langbot-cli/internal/deploy"
	"github.com/langbot-app/langbot-cli/internal/result"
)

const upgradeTimeout = 20 * time.Minute

var imageDigestPattern = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)

type UpgradeOptions struct {
	CheckOptions
	Version string
	DryRun  bool
	Yes     bool
}

func (s *Service) Upgrade(ctx context.Context, options UpgradeOptions) (Result, error) {
	if !options.DryRun && !options.Yes {
		return Result{}, result.New("precondition", "升级需要显式 --yes")
	}
	targetVersion := strings.TrimSpace(options.Version)
	if targetVersion == "" {
		return Result{}, result.New("input", "upgrade 需要 --version <tag>")
	}
	if _, err := deploy.CompareReleaseVersions("v0.0.0", targetVersion); err != nil {
		return Result{}, result.New("input", err.Error())
	}
	timeout, err := operationTimeout(options.Timeout, options.TimeoutSet, upgradeTimeout)
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
		comparison, compareErr := deploy.CompareReleaseVersions(current.CoreVersion, targetVersion)
		if compareErr != nil {
			return result.New("input", compareErr.Error())
		}
		if comparison >= 0 {
			return result.New("precondition", "目标版本必须高于当前版本")
		}
		compose, targetComposeDigest, composeErr := deploy.ComposeForUpgrade(current, diagnostics.Dir, targetVersion)
		if composeErr != nil {
			return result.New("precondition", composeErr.Error())
		}
		before, statusErr := diagnostics.Status(operationCtx)
		if statusErr != nil || (before.Status != "running" && before.Status != "stopped") {
			return result.New("precondition", "无法确认本机部署处于正常运行或停止状态")
		}
		if before.Status == "running" {
			info, infoErr := (api.Client{Transport: s.deps.Transport}).Info(operationCtx, api.Target{Endpoint: current.Endpoint, Timeout: 5 * time.Second})
			if infoErr != nil || info.Version != current.CoreVersion {
				return result.New("precondition", "运行中的 Core 版本与部署记录不一致")
			}
		} else if before.HTTP["reachable"] == true {
			return result.New("precondition", "部署已停止，但 endpoint 被其他服务占用")
		}
		dataBytes, freeBytes, dataErr := deploy.InspectUpgradeData(current)
		if dataErr != nil {
			return result.New("precondition", dataErr.Error())
		}
		oldCompose, readErr := os.ReadFile(current.ComposeFile)
		if readErr != nil {
			return result.New("precondition", "无法读取当前 Compose 资产")
		}
		sum := sha256.Sum256(oldCompose)
		state := deploy.UpgradeState{
			DeploymentID: current.DeploymentID, SourceVersion: current.CoreVersion, TargetVersion: targetVersion,
			SourceImage: current.Asset.Name, TargetImage: deploy.DefaultImageRepository + ":" + targetVersion,
			SourceCompose: "sha256:" + hex.EncodeToString(sum[:]), TargetCompose: targetComposeDigest,
			Profile: current.Profile, Database: "sqlite", DataDir: current.DataDir, BeforeContainers: before.Containers,
			DiskFreeBytes: freeBytes, DataBytes: dataBytes, BeforeStatus: before.Status,
		}
		plan := map[string]any{
			"action": "upgrade", "source_version": state.SourceVersion, "target_version": targetVersion,
			"target_image": state.TargetImage, "image_verification": "pending_pull",
			"dir": diagnostics.Dir, "endpoint": current.Endpoint, "before_status": before.Status,
			"source_compose_digest": state.SourceCompose, "target_compose_digest": state.TargetCompose,
			"data_bytes": dataBytes, "disk_free_bytes": freeBytes, "database": state.Database, "profile": state.Profile,
		}
		response = Result{Data: plan}
		if options.DryRun {
			plan["dry_run"] = true
			return nil
		}
		// 官方镜像由 LangBot Release 发布流程生成；校验摘要后才停服。
		pulled, pullErr := diagnostics.Runner.Docker(operationCtx, "pull", state.TargetImage)
		if pullErr != nil {
			return mapDockerCommandError(pulled, pullErr, "拉取目标官方镜像失败，部署未修改")
		}
		inspected, inspectErr := diagnostics.Runner.Docker(operationCtx, "image", "inspect", "--format", "{{index .RepoDigests 0}}", state.TargetImage)
		if inspectErr != nil {
			return result.New("precondition", "无法验证目标镜像摘要，部署未修改")
		}
		state.TargetDigest = repositoryDigest(inspected.Stdout)
		if !imageDigestPattern.MatchString(state.TargetDigest) {
			return result.New("precondition", "目标镜像没有可验证的仓库摘要，部署未修改")
		}
		plan["image_verification"] = "verified"
		plan["target_image_digest"] = state.TargetDigest
		state.Phase = "image_verified"
		if err := deploy.WriteUpgradeState(diagnostics.Dir, state); err != nil {
			return result.New("server", "无法记录升级现场，部署未修改")
		}
		update := func(phase string) error {
			state.Phase = phase
			if err := deploy.WriteUpgradeState(diagnostics.Dir, state); err != nil {
				return result.New("recovery_required", "无法持久化升级阶段，请检查现场")
			}
			return nil
		}
		fail := func(step, message string) error {
			state.Phase = "recovery_required"
			state.FailureStep = step
			_ = deploy.WriteUpgradeState(diagnostics.Dir, state)
			plan["upgrade"] = state
			return result.New("recovery_required", message+"；请运行 status 和 doctor 检查现场")
		}
		if err := update("stopping"); err != nil {
			return err
		}
		if before.Status == "running" {
			stopped, stopErr := diagnostics.Runner.Compose(operationCtx, current, "stop")
			if stopErr != nil {
				_ = stopped
				return fail("stop", "停服结果无法确认")
			}
		}
		if err := update("stopped"); err != nil {
			return err
		}
		stoppedStatus, stoppedErr := diagnostics.Status(operationCtx)
		if stoppedErr != nil || stoppedStatus.Status != "stopped" {
			return fail("stop_readback", "未能确认所有必需服务已停止")
		}
		if _, _, err := deploy.InspectUpgradeData(current); err != nil {
			return fail("backup_preflight", "停服后数据范围或空间检查失败")
		}
		backupDir, backupErr := deploy.CreateUpgradeBackup(operationCtx, current, state)
		if backupErr != nil {
			return fail("backup", "创建或验证完整备份失败")
		}
		state.BackupDir = backupDir
		plan["backup_dir"] = backupDir
		if err := update("backup_verified"); err != nil {
			return err
		}
		if err := update("switching"); err != nil {
			return err
		}
		if err := deploy.WriteUpgradeCompose(current, compose); err != nil {
			return fail("compose_switch", "替换 Compose 资产失败")
		}
		if err := update("compose_switched"); err != nil {
			return err
		}
		if err := update("starting"); err != nil {
			return err
		}
		if _, err := diagnostics.Runner.Compose(operationCtx, current, "up", "-d", "--pull", "never"); err != nil {
			return fail("compose_up", "启动目标版本失败")
		}
		if err := update("verifying"); err != nil {
			return err
		}
		if err := s.waitForLocalVersion(operationCtx, current.Endpoint, targetVersion); err != nil {
			return fail("http_version", "目标版本 HTTP 健康或版本验证失败")
		}
		finalStatus, finalErr := diagnostics.Status(operationCtx)
		if finalErr != nil || finalStatus.Status != "running" {
			return fail("service_health", "目标版本必需服务未全部就绪")
		}
		current.CoreVersion = targetVersion
		current.Asset = deploy.AssetInfo{Name: state.TargetImage, Digest: state.TargetDigest}
		if err := diagnostics.Store.Write(operationCtx, current); err != nil {
			return fail("record_write", "写入目标版本部署记录失败")
		}
		if err := update("completed"); err != nil {
			return err
		}
		plan["completed"] = true
		plan["upgrade"] = state
		plan["status"] = finalStatus
		return nil
	})
	return response, normalizeLocalMutationError(err)
}

func (s *Service) waitForLocalVersion(ctx context.Context, endpoint, version string) error {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		info, err := (api.Client{Transport: s.deps.Transport}).Info(ctx, api.Target{Endpoint: endpoint, Timeout: 5 * time.Second})
		if err == nil && info.Version == version {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}
