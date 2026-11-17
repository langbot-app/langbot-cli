package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/langbot-app/langbot-cli/internal/api"
	"github.com/langbot-app/langbot-cli/internal/deploy"
	"github.com/langbot-app/langbot-cli/internal/i18n"
	"github.com/langbot-app/langbot-cli/internal/progress"
	"github.com/langbot-app/langbot-cli/internal/result"
	"github.com/langbot-app/langbot-cli/internal/telemetry"
)

const upgradeTimeout = 20 * time.Minute

// Compose healthchecks only report their first result after the configured
// interval (30s in the official Compose file), so services read as "starting"
// for a while after Core already answers HTTP. Poll until they settle instead
// of failing an otherwise successful upgrade, start or restart on the first
// snapshot.
var (
	composeServiceReadyTimeout = 3 * time.Minute
	composeServicePollInterval = 2 * time.Second
)

func waitForComposeServices(ctx context.Context, status func(context.Context) (deploy.StatusResult, error)) (deploy.StatusResult, error) {
	deadline := time.NewTimer(composeServiceReadyTimeout)
	defer deadline.Stop()
	ticker := time.NewTicker(composeServicePollInterval)
	defer ticker.Stop()
	for {
		current, err := status(ctx)
		if err == nil && current.Status != "starting" && current.Status != "degraded" {
			return current, nil
		}
		select {
		case <-ctx.Done():
			return current, err
		case <-deadline.C:
			return current, err
		case <-ticker.C:
		}
	}
}

var imageDigestPattern = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)

type UpgradeOptions struct {
	CheckOptions
	Version string
	DryRun  bool
	Yes     bool
}

func (s *Service) Upgrade(ctx context.Context, options UpgradeOptions) (responseResult Result, responseErr error) {
	var recoveryEndpoint string
	defer func() {
		if responseErr == nil {
			return
		}
		data, _ := responseResult.Data.(map[string]any)
		if data == nil {
			data = map[string]any{}
		}
		data["action"], data["step"] = "upgrade", telemetry.CurrentStep(ctx)
		data["local_name"], data["local_dir"] = options.LocalName, options.LocalDir
		data["recovery_endpoint"] = recoveryEndpoint
		responseResult.Data = data
	}()
	if !options.DryRun && !options.Yes {
		return Result{}, result.New("precondition", i18n.T("Upgrade requires explicit --yes"))
	}
	targetVersion := strings.TrimSpace(options.Version)
	telemetry.SetStep(ctx, "resolve_version")
	if targetVersion == "" {
		var err error
		targetVersion, err = s.latestRelease(ctx)
		if err != nil {
			return Result{}, err
		}
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
	telemetry.SetStep(ctx, "validate_target")
	record, diagnostics, err := s.lifecycleTarget(operationCtx, options.CheckOptions)
	if err != nil {
		return Result{}, err
	}
	auth := localAuthAttempt{service: s, dir: diagnostics.Dir}
	defer func() { responseResult = auth.result(responseResult) }()
	recoveryEndpoint = record.Endpoint
	var response Result
	preview := record
	preview.CoreVersion = targetVersion
	s.deploymentContext(ctx, preview, diagnostics.Dir, "stored")
	if strings.TrimSpace(options.Version) == "" && record.CoreVersion != "" {
		// Without an explicit --version, being on the latest stable release is
		// a successful no-op rather than a failed precondition.
		if comparison, compareErr := deploy.CompareReleaseVersions(record.CoreVersion, targetVersion); compareErr == nil && comparison >= 0 {
			return Result{Data: map[string]any{"action": "upgrade", "driver": record.Driver, "dry_run": options.DryRun, "up_to_date": true, "changed": false,
				"source_version": record.CoreVersion, "target_version": record.CoreVersion, "latest_version": targetVersion}}, nil
		}
	}
	if record.Driver == deploy.DriverNative {
		progress.Plan(ctx, 12)
		data := map[string]any{"action": "upgrade", "version": targetVersion, "driver": record.Driver, "dry_run": options.DryRun,
			"source_version": record.CoreVersion, "target_version": targetVersion}
		if options.DryRun {
			return Result{Data: data}, nil
		}
		err := deploy.WithMutationLock(operationCtx, diagnostics.Dir, func() error {
			current, err := diagnostics.Store.Read()
			if err != nil {
				return err
			}
			if current.DeploymentID != record.DeploymentID {
				return result.New("precondition", i18n.T("deployment changed"))
			}
			backup, err := deploy.UpgradeNative(operationCtx, diagnostics.Dir, current, targetVersion, s.nativeOutput(), s.deps.LocalHTTP, func(next deploy.Record) { auth.configure(operationCtx, next) })
			data["backup_dir"] = backup
			data["completed"] = err == nil

			return err
		})
		if err == nil {
			auth.bind(operationCtx, preview)
		}
		return Result{Data: data}, nativeMutationError(err)
	}
	err = deploy.WithMutationLock(operationCtx, diagnostics.Dir, func() error {
		if err := deploy.CheckUpgradeInterrupted(diagnostics.Dir); err != nil {
			telemetry.SetStep(operationCtx, "interrupted")
			return result.New("recovery_required", err.Error())
		}
		current, readErr := diagnostics.Store.Read()
		if readErr != nil || current.DeploymentID != record.DeploymentID {
			return result.New("precondition", i18n.T("Local deployment binding changed"))
		}
		comparison, compareErr := deploy.CompareReleaseVersions(current.CoreVersion, targetVersion)
		if compareErr != nil {
			return result.New("input", compareErr.Error())
		}
		if comparison >= 0 {
			return result.New("precondition", i18n.T("Target version must be newer than current version"))
		}
		compose, targetComposeDigest, composeErr := deploy.ComposeForUpgrade(current, diagnostics.Dir, targetVersion)
		if composeErr != nil {
			return result.New("precondition", composeErr.Error())
		}
		if deploy.IsAdoptedLayout(current) {
			resolved, err := diagnostics.Runner.Compose(operationCtx, current, "config", "--format", "json")
			if err != nil {
				return result.New("precondition", i18n.T("Unable to parse adopted Compose configuration"))
			}
			if err := deploy.ValidateAdoptedUpgradeConfig(current, resolved.Stdout); err != nil {
				return result.New("precondition", err.Error())
			}
		}
		before, statusErr := diagnostics.Status(operationCtx)
		if statusErr != nil || (before.Status != "running" && before.Status != "stopped") {
			return result.New("precondition", i18n.T("Unable to confirm local deployment is running normally or stopped"))
		}
		if before.Status == "running" {
			info, infoErr := (api.Client{Transport: s.deps.Transport}).Info(operationCtx, api.Target{Endpoint: current.Endpoint, Timeout: 5 * time.Second})
			if infoErr != nil || info.Version != current.CoreVersion {
				return result.New("precondition", i18n.T("Running Core version does not match deployment record"))
			}
		} else if before.HTTP["reachable"] == true {
			return result.New("precondition", i18n.T("Deployment stopped but endpoint is occupied by another service"))
		}
		telemetry.SetStep(operationCtx, "backup_preflight")
		dataBytes, freeBytes, dataErr := deploy.InspectDockerUpgradeData(operationCtx, diagnostics.Runner, current)
		if dataErr != nil {
			return result.New("precondition", dataErr.Error())
		}
		oldCompose, readErr := os.ReadFile(current.ComposeFile)
		if readErr != nil {
			return result.New("precondition", i18n.T("Unable to read current Compose assets"))
		}
		sourceImage, err := deploy.ComposeSourceImage(oldCompose)
		if err != nil {
			return result.New("precondition", err.Error())
		}
		if deploy.IsAdoptedLayout(current) && composeDigest(oldCompose) != current.Asset.Digest {
			return result.New("precondition", i18n.T("Adopted Compose assets changed; run adopt again before upgrading"))
		}
		sum := sha256.Sum256(oldCompose)
		state := deploy.UpgradeState{
			DeploymentID: current.DeploymentID, SourceVersion: current.CoreVersion, TargetVersion: targetVersion,
			SourceImage: sourceImage, TargetImage: deploy.ImageRepository(current) + ":" + targetVersion,
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
		telemetry.SetStep(operationCtx, "image_pull")
		pullSettings := current
		if deploy.IsAdoptedLayout(current) {
			pullSettings.ImageRepositoryExplicit = true
		}
		selection, pullErr := (deploy.ImageSources{HTTP: s.deps.LocalHTTP}).Pull(operationCtx, diagnostics.Runner, pullSettings, targetVersion, true, func(selected deploy.ImageSelection) {
			if selected.Repository != deploy.ImageRepository(current) {
				progress.Info(ctx, []string{fmt.Sprintf(i18n.T("Image source changed: %s → %s"), deploy.ImageRepository(current), selected.Repository)})
			}
			progress.Info(ctx, []string{deploy.ImageSelectionMessage(selected)})
		})
		if pullErr != nil {
			return result.New("precondition", pullErr.Error())
		}
		compose = deploy.PinComposeImage(compose, state.TargetImage, selection.Reference)
		targetComposeDigest = composeDigest(compose)
		state.TargetImage, state.TargetDigest, state.TargetCompose = selection.Reference, selection.Digest, targetComposeDigest
		current.ImageRepository = selection.Repository
		plan["target_image"], plan["target_compose_digest"] = state.TargetImage, state.TargetCompose
		if !imageDigestPattern.MatchString(state.TargetDigest) {
			return result.New("precondition", i18n.T("Target image has no verifiable repository digest; deployment unchanged"))
		}
		plan["image_verification"] = "verified"
		plan["target_image_digest"] = state.TargetDigest
		latestCompose, err := os.ReadFile(current.ComposeFile)
		if err != nil || composeDigest(latestCompose) != state.SourceCompose {
			return result.New("precondition", i18n.T("Compose assets changed while pulling images; deployment unchanged"))
		}
		state.Phase = "image_verified"
		if err := deploy.WriteUpgradeState(diagnostics.Dir, state); err != nil {
			return result.New("server", i18n.T("Unable to record upgrade state; deployment unchanged"))
		}
		update := func(phase string) error {
			telemetry.SetStep(operationCtx, phase)
			state.Phase = phase
			if err := deploy.WriteUpgradeState(diagnostics.Dir, state); err != nil {
				return result.New("recovery_required", i18n.T("Unable to persist upgrade stage; inspect deployment state"))
			}
			return nil
		}
		fail := func(step, message string) error {
			telemetry.SetStep(ctx, step)
			state.Phase = "recovery_required"
			state.FailureStep = step
			_ = deploy.WriteUpgradeState(diagnostics.Dir, state)
			plan["upgrade"] = state
			return result.New("recovery_required", message+i18n.T("; run status and doctor to inspect deployment state"))
		}
		if err := update("stopping"); err != nil {
			return err
		}
		if before.Status == "running" {
			stopped, stopErr := diagnostics.Runner.Compose(operationCtx, current, "stop")
			if stopErr != nil {
				_ = stopped
				return fail("stop", i18n.T("Unable to confirm services stopped"))
			}
		}
		if err := update("stopped"); err != nil {
			return err
		}
		stoppedStatus, stoppedErr := diagnostics.Status(operationCtx)
		if stoppedErr != nil || stoppedStatus.Status != "stopped" {
			return fail("stop_readback", i18n.T("Unable to confirm all required services stopped"))
		}
		if _, _, err := deploy.InspectDockerUpgradeData(operationCtx, diagnostics.Runner, current); err != nil {
			return fail("backup_preflight", i18n.T("Data scope or space check failed after stopping services"))
		}
		telemetry.SetStep(operationCtx, "backup_data")
		backupDir, backupErr := deploy.CreateUpgradeBackup(operationCtx, current, state, diagnostics.Dir, diagnostics.Runner)
		if backupErr != nil {
			return fail("backup", i18n.T("Failed to create or verify a complete backup"))
		}
		state.BackupDir = backupDir
		plan["backup_dir"] = backupDir
		if err := update("backup_verified"); err != nil {
			return err
		}
		if err := update("switching"); err != nil {
			return err
		}
		latestCompose, err = os.ReadFile(current.ComposeFile)
		if err != nil || !strings.EqualFold(state.SourceCompose, composeDigest(latestCompose)) {
			return fail("compose_changed", i18n.T("Compose assets changed during backup"))
		}
		if err := deploy.WriteUpgradeCompose(current, compose); err != nil {
			return fail("compose_switch", i18n.T("Failed to replace Compose assets"))
		}
		if err := deploy.WriteDeployIdentity(current, s.deps.Version); err != nil {
			return fail("environment_switch", i18n.T("Failed to update deployment source environment variables"))
		}
		if err := update("compose_switched"); err != nil {
			return err
		}
		if err := update("starting"); err != nil {
			return err
		}
		authRecord := current
		authRecord.CoreVersion = targetVersion
		auth.configure(operationCtx, authRecord)
		if _, err := diagnostics.Runner.Compose(operationCtx, current, "up", "-d", "--pull", "never"); err != nil {
			return fail("compose_up", i18n.T("Failed to start the target version"))
		}
		if err := update("verifying"); err != nil {
			return err
		}
		if err := s.waitForLocalVersion(operationCtx, current, targetVersion); err != nil {
			return fail("http_version", i18n.T("Target HTTP health or version verification failed"))
		}
		finalStatus, finalErr := waitForComposeServices(operationCtx, diagnostics.Status)
		if finalErr != nil || finalStatus.Status != "running" {
			return fail("service_health", i18n.T("Not all required target services are ready"))
		}
		current.CoreVersion = targetVersion
		if deploy.IsAdoptedLayout(current) {
			current.Asset.Digest = targetComposeDigest
		} else {
			current.Asset = deploy.AssetInfo{Name: state.TargetImage, Digest: state.TargetDigest}
		}
		if err := diagnostics.Store.Write(operationCtx, current); err != nil {
			return fail("record_write", i18n.T("Failed to write target version deployment record"))
		}
		if err := update("completed"); err != nil {
			return err
		}
		plan["completed"] = true
		plan["upgrade"] = state
		if updated, err := diagnostics.Store.Read(); err == nil {
			finalStatus.Record = updated
		}
		finalStatus.Upgrade = &state
		plan["status"] = finalStatus
		return nil
	})
	if err == nil && !options.DryRun {
		auth.bind(operationCtx, preview)
	}
	return response, normalizeLocalMutationError(err)
}

func composeDigest(content []byte) string {
	sum := sha256.Sum256(content)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func (s *Service) waitForLocalVersion(ctx context.Context, record deploy.Record, version string) error {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		s.coreHealthProgress(ctx, record)
		info, err := (api.Client{Transport: s.deps.Transport}).Info(ctx, api.Target{Endpoint: record.Endpoint, Timeout: 5 * time.Second})
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
