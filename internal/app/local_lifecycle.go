package app

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/langbot-app/langbot-cli/internal/api"
	"github.com/langbot-app/langbot-cli/internal/config"
	"github.com/langbot-app/langbot-cli/internal/deploy"
	endpointutil "github.com/langbot-app/langbot-cli/internal/endpoint"
	"github.com/langbot-app/langbot-cli/internal/i18n"
	"github.com/langbot-app/langbot-cli/internal/progress"
	"github.com/langbot-app/langbot-cli/internal/result"
	"github.com/langbot-app/langbot-cli/internal/telemetry"
)

var ansiEscapePattern = regexp.MustCompile(`\x1b\[[0-9;]*[A-Za-z]`)

type InstallOptions struct {
	Driver          string
	Name            string
	Region          string
	PyPIIndex       string
	PythonMirror    string
	ImageRepository string
	Extras          []string
	Dir             string
	Version         string
	Profile         string
	Port            int
	Project         string
	PluginDebugPort *int
	AdapterPorts    []string
	DryRun          bool
	Timeout         string
	TimeoutSet      bool
}

type AdoptOptions struct {
	Dir            string
	ComposeFile    string
	ComposeProject string
	Endpoint       string
	Profile        string
	Timeout        string
	TimeoutSet     bool
	APIKeyStdin    bool
}

type LifecycleOptions struct {
	CheckOptions
}

type LocalLogsOptions struct {
	CheckOptions
	Service string
	Tail    int
	Since   string
	Follow  bool
	Format  string
}

func (s *Service) Install(ctx context.Context, options InstallOptions) (Result, error) {
	telemetry.SetEnvironment(ctx, options.Driver, "", options.Region, options.Version)
	if options.Driver == deploy.DriverNative {
		return s.installNative(ctx, options)
	}
	progress.Plan(ctx, 6)
	timeout, err := operationTimeout(options.Timeout, options.TimeoutSet, 10*time.Minute)
	if err != nil {
		return Result{}, err
	}
	operationCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	dir, err := s.resolveLocalDir(options.Dir)
	if err != nil {
		return Result{}, result.New("input", i18n.T("Invalid deployment directory"))
	}
	version := strings.TrimSpace(options.Version)
	telemetry.SetStep(ctx, "resolve_version")
	if version == "" {
		version, err = s.latestRelease(operationCtx)
		if err != nil {
			return Result{}, err
		}
	}
	assets, err := deploy.NewInstallAssets(dir, version, options.Profile, options.Port, deploy.InstallNetworkOptions{
		Project: options.Project, PluginDebugPort: options.PluginDebugPort, AdapterPorts: options.AdapterPorts,
	})
	if err != nil {
		return Result{}, result.New("input", err.Error())
	}
	assets.SetDeployIdentity(s.deps.Version)
	settings := deploy.Record{Name: options.Name, Region: options.Region, PyPIIndex: options.PyPIIndex, PythonMirror: options.PythonMirror, ImageRepository: options.ImageRepository, ServiceManager: "docker_compose"}
	settings.ImageRepositoryExplicit = options.ImageRepository != ""
	if settings.Region != "" {
		settings.Region, err = deploy.ResolveRegion(operationCtx, settings.Region, s.deps.LocalHTTP)
		if err != nil {
			return Result{}, err
		}
	}
	deploy.ApplyRegion(&settings)
	settings.CoreVersion, settings.Driver, settings.Port = version, deploy.DriverDockerCompose, options.Port
	s.deploymentContext(ctx, settings, dir, options.Region)
	telemetry.SetEnvironment(ctx, "docker", settings.ServiceManager, settings.Region, version)
	if err := assets.SetImageRepository(settings.ImageRepository); err != nil {
		return Result{}, result.New("input", err.Error())
	}
	assets.SetPackageIndex(settings.PyPIIndex)
	runner := s.localRunner()
	telemetry.SetStep(ctx, "preflight")
	checks, preflightErr := deploy.PreflightInstall(operationCtx, assets, runner)
	plan := installPlanData(assets, checks)
	for key, value := range map[string]any{"url": assets.Record("", "").Endpoint, "driver": deploy.DriverDockerCompose, "instance_dir": dir, "version": version, "name": options.Name, "region": settings.Region, "service_manager": "docker_compose"} {
		plan[key] = value
	}
	if preflightErr != nil {
		return Result{Data: plan}, mapLocalError(preflightErr)
	}
	if options.DryRun {
		if err := s.checkDeploymentRegistration(assets.Dir, assets.Record("", "")); err != nil {
			return Result{Data: plan}, err
		}
		plan["dry_run"] = true
		return Result{Data: plan}, nil
	}

	var response Result
	locator := deploy.Locator{Path: s.deps.LocalLocatorPath}
	err = locator.WithLock(operationCtx, func() error {
		return deploy.WithMutationLock(operationCtx, assets.Dir, func() error {
			if err := s.checkDeploymentRegistration(assets.Dir, assets.Record("", "")); err != nil {
				return err
			}
			if err := deploy.ValidateInstallTarget(assets.Dir, true); err != nil {
				return result.New("precondition", i18n.T("Target directory is not empty; refusing to overwrite"))
			}
			occupied, occupiedErr := deploy.InstallProjectOccupied(operationCtx, runner, assets.Project)
			if occupiedErr != nil || occupied {
				return result.New("precondition", i18n.T("Managed Compose project is unavailable"))
			}
			telemetry.SetStep(ctx, "image_pull")
			selection, pullErr := (deploy.ImageSources{HTTP: s.deps.LocalHTTP}).Pull(operationCtx, runner, settings, version, false, func(selected deploy.ImageSelection) {
				progress.Info(ctx, []string{deploy.ImageSelectionMessage(selected)})
			})
			if pullErr != nil {
				return result.New("precondition", pullErr.Error())
			}
			settings.ImageRepository = selection.Repository
			assets.SetImageReference(selection.Reference)
			assets.SetPackageIndex(settings.PyPIIndex)
			imageDigest := selection.Digest
			deploymentID, idErr := deploy.NewDeploymentID()
			if idErr != nil {
				return result.New("internal", i18n.T("Failed to generate deployment identity"))
			}
			record := assets.Record(deploymentID, imageDigest)
			record.Name, record.Region, record.PyPIIndex, record.PythonMirror, record.ImageRepository, record.ServiceManager = settings.Name, settings.Region, settings.PyPIIndex, settings.PythonMirror, settings.ImageRepository, settings.ServiceManager
			record.ImageRepositoryExplicit = settings.ImageRepositoryExplicit
			store := deploy.Store{Dir: assets.Dir}
			telemetry.SetStep(ctx, "write_record")
			if err := store.Write(operationCtx, record); err != nil {
				return mapStoreWriteError(err)
			}
			if err := assets.Write(); err != nil {
				return result.New("server", i18n.T("Failed to write deployment assets"))
			}
			if err := locator.Write(assets.Dir); err != nil {
				return result.New("server", i18n.T("Failed to write local deployment locator"))
			}
			telemetry.SetStep(ctx, "compose_up")
			command, commandErr := runner.Compose(operationCtx, record, "up", "-d")
			if commandErr != nil {
				readbackCtx, readbackCancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
				defer readbackCancel()
				diagnostics := deploy.Diagnostics{Dir: assets.Dir, Store: store, Runner: runner, HTTP: s.deps.LocalHTTP}
				status, _ := diagnostics.Status(readbackCtx)
				if status.Status == "running" {
					response = Result{Data: map[string]any{"action": "install", "changed": true, "record": record, "status": status}}
					return nil
				}
				response = Result{Data: map[string]any{"action": "install", "changed": true, "step": "compose_up_failed", "record": record, "status": status}}
				return uncertainDockerError(command, commandErr, i18n.T("Unable to confirm installation result"))
			}
			telemetry.SetStep(ctx, "health_check")
			if err := s.waitForLocalHTTP(operationCtx, record); err != nil {
				response = Result{Data: lifecycleFailureData("install", record, "health_check_failed")}
				return result.New("result_unknown", i18n.T("Containers started but LangBot HTTP health is unconfirmed"))
			}
			diagnostics := deploy.Diagnostics{Dir: assets.Dir, Store: store, Runner: runner, HTTP: s.deps.LocalHTTP}
			status, statusErr := diagnostics.Status(operationCtx)
			response = Result{Data: map[string]any{"action": "install", "changed": true, "record": record, "status": status}}
			if statusErr != nil || status.Status != "running" {
				return result.New("result_unknown", i18n.T("Installation ran but final running state is unconfirmed"))
			}
			return nil
		})
	})
	if err != nil {
		if response.Data == nil {
			response.Data = plan
		}
		return response, normalizeLocalMutationError(err)
	}
	if data, ok := response.Data.(map[string]any); ok {
		if record, ok := data["record"].(deploy.Record); ok {
			installAutostartData(data, record, assets.Dir)
		}
		for _, key := range []string{"url", "driver", "instance_dir", "version", "name", "region", "service_manager"} {
			data[key] = plan[key]
		}
	}
	return response, nil
}

func (s *Service) Adopt(ctx context.Context, options AdoptOptions) (Result, error) {
	timeout, err := operationTimeout(options.Timeout, options.TimeoutSet, 2*time.Minute)
	if err != nil {
		return Result{}, err
	}
	operationCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	dir, err := s.resolveLocalDir(options.Dir)
	if err != nil {
		return Result{}, result.New("input", i18n.T("Invalid deployment record directory"))
	}
	composeFile, err := resolveComposeFile(options.ComposeFile)
	if err != nil {
		return Result{}, err
	}
	project := strings.TrimSpace(options.ComposeProject)
	if !validComposeProject(project) {
		return Result{}, result.New("input", i18n.T("Invalid Compose project name"))
	}
	endpoint, err := endpointutil.Normalize(strings.TrimSpace(options.Endpoint))
	if err != nil {
		return Result{}, err
	}
	profile := strings.ToLower(strings.TrimSpace(options.Profile))
	if profile == "" {
		profile = "basic"
	}
	if profile != "basic" && profile != "all" {
		return Result{}, result.New("input", i18n.T("profile only supports basic or all"))
	}
	if !loopbackEndpoint(endpoint) {
		return Result{}, result.New("precondition", i18n.T("adopt only accepts local loopback endpoints"))
	}
	target := api.Target{Endpoint: endpoint, Timeout: timeout}
	if options.APIKeyStdin {
		key, keyErr := readCredential(operationCtx, s.deps.In)
		if keyErr != nil {
			return Result{}, keyErr
		}
		credential, credentialErr := (config.Connection{}).WithCredential(config.Options{APIKeyStdin: true}, s.deps.LookupEnv, strings.NewReader(key))
		if credentialErr != nil {
			return Result{}, credentialErr
		}
		target.APIKey = credential.APIKey
	}
	client := api.Client{Transport: s.deps.Transport}
	info, err := client.Info(operationCtx, target)
	if err != nil {
		return Result{}, err
	}
	if environmentType(info.Edition) == "cloud" {
		return Result{}, result.New("precondition", i18n.T("Cloud services cannot be bound to a local lifecycle"))
	}
	record := deploy.Record{Driver: deploy.DriverDockerCompose, CoreVersion: info.Version, Profile: profile, ComposeProject: project, ComposeFile: composeFile, DataDir: filepath.Join(filepath.Dir(composeFile), "data"), Endpoint: endpoint, Port: endpointPort(endpoint)}
	if target.APIKey != "" {
		identity, identityErr := client.Context(operationCtx, target)
		if identityErr != nil {
			return Result{}, identityErr
		}
		record.InstanceUUID = identity.InstanceUUID
	}
	services, commandErr := s.localRunner().Compose(operationCtx, record, "config", "--services")
	if commandErr != nil {
		return Result{}, result.New("precondition", i18n.T("Unable to verify Compose project"))
	}
	if missing := missingRequiredServices(services.Stdout, profile); len(missing) != 0 {
		return Result{Data: map[string]any{"missing_services": missing}}, result.New("precondition", i18n.T("Compose project is missing required LangBot services"))
	}
	published, commandErr := s.localRunner().Compose(operationCtx, record, "port", "langbot", "5300")
	if commandErr != nil || !publishedPortMatches(published.Stdout, record.Port) {
		return Result{}, result.New("precondition", i18n.T("Compose langbot service is not published on the endpoint port"))
	}
	composeData, err := os.ReadFile(composeFile)
	if err != nil {
		return Result{}, result.New("precondition", i18n.T("Unable to read Compose file"))
	}
	sum := sha256.Sum256(composeData)
	record.Asset = deploy.AssetInfo{Name: filepath.Base(composeFile), Digest: "sha256:" + hex.EncodeToString(sum[:])}

	var changed bool
	locator := deploy.Locator{Path: s.deps.LocalLocatorPath}
	err = locator.WithLock(operationCtx, func() error {
		if err := deploy.WithMutationLock(operationCtx, dir, func() error {
			if err := s.checkDeploymentRegistration(dir, record); err != nil {
				return err
			}
			store := deploy.Store{Dir: dir}
			existing, readErr := store.Read()
			if readErr == nil {
				if sameDeployment(existing, record) {
					if deploy.IsAdoptedLayout(existing) && (existing.Asset != record.Asset || existing.CoreVersion != record.CoreVersion) {
						existing.Asset = record.Asset
						existing.CoreVersion = record.CoreVersion
						if err := store.Write(operationCtx, existing); err != nil {
							return mapStoreWriteError(err)
						}
						existing, readErr = store.Read()
						if readErr != nil {
							return mapStoreWriteError(readErr)
						}
						changed = true
					}
					record = existing
					return nil
				}
				return result.New("precondition", i18n.T("Another lbctl-managed deployment already exists"))
			}
			if !errors.Is(readErr, deploy.ErrNotInstalled) && !errors.Is(readErr, deploy.ErrNotManaged) {
				return mapStoreWriteError(readErr)
			}
			if errors.Is(readErr, deploy.ErrNotManaged) {
				if targetErr := deploy.ValidateInstallTarget(dir, true); targetErr != nil {
					return result.New("precondition", i18n.T("Deployment record directory not empty; refusing to overwrite"))
				}
			}
			deploymentID, idErr := deploy.NewDeploymentID()
			if idErr != nil {
				return result.New("internal", i18n.T("Failed to generate deployment identity"))
			}
			record.DeploymentID = deploymentID
			if err := store.Write(operationCtx, record); err != nil {
				return mapStoreWriteError(err)
			}
			record, readErr = store.Read()
			if readErr != nil {
				return mapStoreWriteError(readErr)
			}
			changed = true
			return nil
		}); err != nil {
			return err
		}
		if err := locator.Write(dir); err != nil {
			return result.New("server", i18n.T("Failed to write local deployment locator"))
		}
		return nil
	})
	if err != nil {
		return Result{}, normalizeLocalMutationError(err)
	}
	return Result{Data: map[string]any{"action": "adopt", "changed": changed, "record": record}}, nil
}

func (s *Service) Start(ctx context.Context, options LifecycleOptions) (Result, error) {
	return s.runLifecycle(ctx, "start", options)
}

func (s *Service) Stop(ctx context.Context, options LifecycleOptions) (Result, error) {
	return s.runLifecycle(ctx, "stop", options)
}

func (s *Service) Restart(ctx context.Context, options LifecycleOptions) (Result, error) {
	return s.runLifecycle(ctx, "restart", options)
}

func (s *Service) LocalLogs(ctx context.Context, options LocalLogsOptions) (Result, error) {
	record, diagnostics, err := s.lifecycleTarget(ctx, options.CheckOptions)
	if err != nil {
		return Result{}, err
	}
	if err := validateLogsOptions(options, record); err != nil {
		return Result{}, err
	}
	timeout, err := operationTimeout(options.Timeout, options.TimeoutSet, 30*time.Second)
	if err != nil {
		return Result{}, err
	}
	operationCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if record.Driver == deploy.DriverNative {
		var buffer strings.Builder
		err := deploy.NativeLogs(operationCtx, diagnostics.Dir, &buffer, options.Tail, false)
		return Result{Data: map[string]any{"lines": splitLogLines(buffer.String(), nil)}}, err
	}
	args := logArguments(options, false)
	command, commandErr := diagnostics.Runner.Compose(operationCtx, record, args...)
	if commandErr != nil {
		return Result{}, mapDockerCommandError(command, commandErr, i18n.T("Failed to read logs"))
	}
	lines := splitLogLines(command.Stdout, readLogSecrets(record))
	return Result{Data: map[string]any{"service": selectedLogService(options.Service), "lines": lines, "count": len(lines)}}, nil
}

func (s *Service) FollowLocalLogs(ctx context.Context, options LocalLogsOptions) error {
	record, diagnostics, err := s.lifecycleTarget(ctx, options.CheckOptions)
	if err != nil {
		return err
	}
	if err := validateLogsOptions(options, record); err != nil {
		return err
	}
	operationCtx := ctx
	cancel := func() {}
	if options.TimeoutSet {
		timeout, timeoutErr := operationTimeout(options.Timeout, true, 0)
		if timeoutErr != nil {
			return timeoutErr
		}
		operationCtx, cancel = context.WithTimeout(ctx, timeout)
	}
	defer cancel()
	stream, ok := diagnostics.Runner.(deploy.StreamRunner)
	if !ok && record.Driver != deploy.DriverNative {
		return result.New("incompatible", i18n.T("Current Docker runner does not support log following"))
	}
	out := s.deps.LocalLogOut
	if out == nil {
		out = io.Discard
	}
	secrets := readLogSecrets(record)
	if options.Format == "json" {
		out = newJSONLineWriter(out, selectedLogService(options.Service), secrets)
	} else {
		out = newSanitizedLineWriter(out, secrets)
	}
	errOut := s.deps.LocalLogErr
	if errOut == nil {
		errOut = io.Discard
	}
	safeErr := newSanitizedLineWriter(errOut, secrets)
	var streamErr error
	if record.Driver == deploy.DriverNative {
		streamErr = deploy.NativeLogs(operationCtx, diagnostics.Dir, out, options.Tail, true)
	} else {
		streamErr = stream.ComposeStream(operationCtx, record, out, safeErr, logArguments(options, true)...)
	}
	_ = safeErr.Flush()
	if streamErr != nil {
		if errors.Is(operationCtx.Err(), context.Canceled) {
			return result.New("network", i18n.T("Log following cancelled"))
		}
		if errors.Is(operationCtx.Err(), context.DeadlineExceeded) {
			return result.New("network", i18n.T("Log following timed out"))
		}
		return result.New("server", i18n.T("Log following failed"))
	}
	switch writer := out.(type) {
	case *jsonLineWriter:
		return writer.Flush()
	case *sanitizedLineWriter:
		return writer.Flush()
	}
	return nil
}

func (s *Service) runLifecycle(ctx context.Context, action string, options LifecycleOptions) (Result, error) {
	progress.Plan(ctx, 3)
	record, diagnostics, err := s.lifecycleTarget(ctx, options.CheckOptions)
	if err != nil {
		return Result{}, err
	}
	if record.Driver == deploy.DriverNative {
		return s.nativeLifecycle(ctx, diagnostics, record, action)
	}
	timeout, err := operationTimeout(options.Timeout, options.TimeoutSet, lifecycleTimeout(action))
	if err != nil {
		return Result{}, err
	}
	operationCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var response Result
	err = deploy.WithMutationLock(operationCtx, diagnostics.Dir, func() error {
		if err := deploy.CheckUpgradeInterrupted(diagnostics.Dir); err != nil {
			return result.New("recovery_required", err.Error())
		}
		current, readErr := diagnostics.Store.Read()
		if readErr != nil || current.DeploymentID != record.DeploymentID {
			return result.New("precondition", i18n.T("Local deployment binding changed"))
		}
		before, beforeErr := diagnostics.Status(operationCtx)
		if beforeErr != nil || before.Status == "unknown" {
			return result.New("precondition", i18n.T("Unable to confirm local deployment status; run doctor first"))
		}
		if (action == "start" && before.Status == "running") || (action == "stop" && before.Status == "stopped") {
			response = Result{Data: map[string]any{"action": action, "changed": false, "status": before}}
			return nil
		}
		if action == "restart" && before.Status == "stopped" {
			return result.New("precondition", i18n.T("Deployment is not running; use start"))
		}
		args := map[string][]string{"start": {"up", "-d"}, "stop": {"stop"}, "restart": {"restart"}}[action]
		telemetry.SetStep(ctx, "compose_"+action)
		command, commandErr := diagnostics.Runner.Compose(operationCtx, current, args...)
		if commandErr == nil && (action == "start" || action == "restart") {
			_ = s.waitForLocalHTTP(operationCtx, current)
		}
		readbackCtx, readbackCancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer readbackCancel()
		after, statusErr := diagnostics.Status(readbackCtx)
		response = Result{Data: map[string]any{"action": action, "changed": true, "status": after}}
		desired := after.Status == "running"
		if action == "stop" {
			desired = after.Status == "stopped"
		}
		if action == "restart" && commandErr != nil {
			return uncertainDockerError(command, commandErr, i18n.T("Unable to confirm restart result"))
		}
		if desired {
			return nil
		}
		if commandErr != nil {
			return uncertainDockerError(command, commandErr, i18n.T("Command may have executed but its result is unknown"))
		}
		if statusErr != nil {
			return result.New("result_unknown", i18n.T("Command executed but status readback failed"))
		}
		return result.New("result_unknown", i18n.T("Command executed but the expected state was not reached"))
	})
	return response, normalizeLocalMutationError(err)
}

func (s *Service) lifecycleTarget(ctx context.Context, options CheckOptions) (deploy.Record, deploy.Diagnostics, error) {
	telemetry.SetStep(ctx, "resolve_target")
	if options.LocalName != "" || options.LocalDir != "" {
		if options.ContextSet || options.EndpointSet || options.APIKeyStdin {
			return deploy.Record{}, deploy.Diagnostics{}, result.New("input", i18n.T("--name/--dir cannot be combined with connection flags"))
		}
		dir, err := s.namedLocalDir(options.LocalName, options.LocalDir)
		if err != nil {
			return deploy.Record{}, deploy.Diagnostics{}, err
		}
		diagnostics, err := s.localDiagnostics(dir)
		if err != nil {
			return deploy.Record{}, diagnostics, err
		}
		record, err := diagnostics.Store.Read()
		if err != nil {
			return record, diagnostics, nativeMutationError(err)
		}
		if record.Driver != deploy.DriverNative && record.Driver != deploy.DriverDockerCompose {
			return record, diagnostics, result.New("incompatible", i18n.T("unsupported local deployment driver"))
		}
		telemetry.SetEnvironment(ctx, record.Driver, record.ServiceManager, record.Region, record.CoreVersion)
		telemetry.SetDirectory(ctx, dir)
		telemetry.SetStep(ctx, "preflight")
		return record, diagnostics, nil
	}
	resolved, discoveryErr := s.discoverActiveEnvironment(ctx, options, false)
	if discoveryErr != nil && result.AsError(discoveryErr).Kind != "network" {
		return deploy.Record{}, deploy.Diagnostics{}, discoveryErr
	}
	if resolved.Environment.Identity.Status == "mismatch" || resolved.Environment.Connection.Authentication == "failed" {
		if discoveryErr != nil {
			return deploy.Record{}, deploy.Diagnostics{}, discoveryErr
		}
		return deploy.Record{}, deploy.Diagnostics{}, result.New("precondition", i18n.T("Current context identity or Workspace binding is not verified"))
	}
	if !resolved.LocalMatch || resolved.Diagnostics == nil {
		return deploy.Record{}, deploy.Diagnostics{}, result.New("precondition", i18n.T("Active Environment is not bound to a managed local deployment"))
	}
	if resolved.Environment.Type == "cloud" || !sameLocalEndpoint(resolved.Record.Endpoint, resolved.Connection.Endpoint) {
		return deploy.Record{}, deploy.Diagnostics{}, result.New("precondition", i18n.T("Active Environment does not match the local deployment endpoint"))
	}
	if resolved.Record.Driver != deploy.DriverDockerCompose && resolved.Record.Driver != deploy.DriverNative {
		return deploy.Record{}, deploy.Diagnostics{}, result.New("incompatible", i18n.T("Current local deployment does not support Docker Compose lifecycle operations"))
	}
	telemetry.SetEnvironment(ctx, resolved.Record.Driver, resolved.Record.ServiceManager, resolved.Record.Region, resolved.Record.CoreVersion)
	telemetry.SetDirectory(ctx, resolved.Diagnostics.Dir)
	telemetry.SetStep(ctx, "preflight")
	return resolved.Record, *resolved.Diagnostics, nil
}

func sameLocalEndpoint(left, right string) bool {
	first, firstErr := endpointutil.Normalize(left)
	second, secondErr := endpointutil.Normalize(right)
	return firstErr == nil && secondErr == nil && first == second
}

func (s *Service) localRunner() deploy.Runner {
	if s.deps.LocalRunner != nil {
		return s.deps.LocalRunner
	}
	return deploy.DockerRunner{}
}

func (s *Service) latestRelease(ctx context.Context) (string, error) {
	if s.deps.LatestRelease != nil {
		return s.deps.LatestRelease(ctx)
	}
	version, err := deploy.LatestCore(ctx, s.deps.LocalHTTP)
	if err != nil {
		return "", result.New("network", err.Error())
	}
	return version, nil
}

func (s *Service) waitForLocalHTTP(ctx context.Context, record deploy.Record) error {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		s.coreHealthProgress(ctx, record)
		_, err := (api.Client{Transport: s.deps.Transport}).Info(ctx, api.Target{Endpoint: record.Endpoint, Timeout: 5 * time.Second})
		if err == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func (s *Service) coreHealthProgress(ctx context.Context, record deploy.Record) {
	if !progress.HumanDisplay(ctx) {
		return
	}
	logCtx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	logs, _ := s.localRunner().Compose(logCtx, record, "logs", "--no-color", "--tail", "1", "langbot")
	progress.Health(ctx, logs.Stdout)
}

func operationTimeout(value string, set bool, fallback time.Duration) (time.Duration, error) {
	if !set {
		return fallback, nil
	}
	timeout, err := time.ParseDuration(strings.TrimSpace(value))
	if err != nil || timeout <= 0 {
		return 0, result.New("input", i18n.T("Invalid timeout"))
	}
	return timeout, nil
}

func lifecycleTimeout(action string) time.Duration {
	if action == "stop" {
		return time.Minute
	}
	return 2 * time.Minute
}

func installPlanData(assets deploy.InstallAssets, checks []deploy.Check) map[string]any {
	return map[string]any{
		"action": "install", "version": assets.Version, "image": assets.Image, "profile": assets.Profile,
		"dir": assets.Dir, "endpoint": fmt.Sprintf("http://127.0.0.1:%d", assets.Port), "compose_project": assets.Project,
		"compose_file": assets.ComposeFile, "data_dir": assets.DataDir, "asset_digest": assets.Digest, "checks": checks,
		"plugin_debug_port": assets.Ports.PluginDebugPort, "adapter_ports": assets.Ports.AdapterPorts,
	}
}

func lifecycleFailureData(action string, record deploy.Record, step string) map[string]any {
	return map[string]any{"action": action, "changed": true, "step": step, "record": record}
}

func repositoryDigest(value string) string {
	value = strings.TrimSpace(value)
	if index := strings.LastIndex(value, "@sha256:"); index >= 0 {
		return value[index+1:]
	}
	return ""
}

func mapDockerCommandError(command deploy.CommandResult, err error, message string) error {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return result.New("result_unknown", message)
	}
	lower := strings.ToLower(command.Stderr)
	if strings.Contains(lower, "permission denied") || strings.Contains(lower, "access denied") {
		return result.New("permission", i18n.T("Current user cannot access Docker daemon"))
	}
	if strings.Contains(lower, "no such host") || strings.Contains(lower, "timeout") || strings.Contains(lower, "connection") {
		return result.New("network", message)
	}
	return result.New("server", message)
}

func uncertainDockerError(command deploy.CommandResult, err error, message string) error {
	return result.New("result_unknown", message)
}

func mapStoreWriteError(err error) error {
	if errors.Is(err, deploy.ErrNotManaged) || errors.Is(err, deploy.ErrCorrupt) {
		return result.New("precondition", i18n.T("Local deployment record directory unavailable"))
	}
	return result.New("server", i18n.T("Failed to write local deployment record"))
}

func normalizeLocalMutationError(err error) error {
	if err == nil {
		return nil
	}
	var typed *result.Error
	if errors.As(err, &typed) {
		return typed
	}
	return mapLocalError(err)
}

func resolveComposeFile(value string) (string, error) {
	path, err := filepath.Abs(filepath.Clean(strings.TrimSpace(value)))
	if err != nil || strings.TrimSpace(value) == "" {
		return "", result.New("input", i18n.T("Invalid Compose file path"))
	}
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return "", result.New("precondition", i18n.T("Compose file does not exist or is unavailable"))
	}
	return path, nil
}

func validComposeProject(value string) bool {
	return deploy.ValidComposeProject(value)
}

func loopbackEndpoint(endpoint string) bool {
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return false
	}
	host := strings.ToLower(parsed.Hostname())
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func publishedPortMatches(value string, port int) bool {
	for _, field := range strings.Fields(value) {
		if strings.HasSuffix(strings.TrimSpace(field), ":"+strconv.Itoa(port)) {
			return true
		}
	}
	return false
}

func endpointPort(endpoint string) int {
	parsed, err := url.Parse(endpoint)
	if err == nil && parsed.Port() != "" {
		if port, parseErr := strconv.Atoi(parsed.Port()); parseErr == nil {
			return port
		}
	}
	if parsed != nil && parsed.Scheme == "https" {
		return 443
	}
	return 80
}

func missingRequiredServices(raw, profile string) []string {
	available := map[string]bool{}
	for _, line := range strings.Fields(raw) {
		available[strings.TrimSpace(line)] = true
	}
	required := []string{"langbot", "langbot_plugin_runtime"}
	if profile == "all" {
		required = append(required, "langbot_box")
	}
	missing := make([]string, 0)
	for _, service := range required {
		if !available[service] {
			missing = append(missing, service)
		}
	}
	return missing
}

func sameDeployment(left, right deploy.Record) bool {
	if left.InstanceUUID != "" && right.InstanceUUID != "" && left.InstanceUUID != right.InstanceUUID {
		return false
	}
	return left.Driver == right.Driver && left.Profile == right.Profile && left.ComposeProject == right.ComposeProject && filepath.Clean(left.ComposeFile) == filepath.Clean(right.ComposeFile) && left.Endpoint == right.Endpoint
}

func validateLogsOptions(options LocalLogsOptions, record deploy.Record) error {
	if record.Driver == deploy.DriverNative {
		if options.Service != "" && options.Service != "langbot" {
			return result.New("input", i18n.T("native logs only support --service langbot"))
		}
		if options.Since != "" {
			return result.New("input", i18n.T("native logs do not support --since; use --tail"))
		}
	}
	if options.Tail < 1 || options.Tail > 5000 {
		return result.New("input", i18n.T("tail must be between 1 and 5000"))
	}
	service := strings.TrimSpace(options.Service)
	if service == "" {
		return nil
	}
	allowed := map[string]bool{"langbot": true, "langbot_plugin_runtime": true}
	if record.Profile == "all" || record.Profile == "box" {
		allowed["langbot_box"] = true
	}
	if !allowed[service] {
		return result.New("input", i18n.T("service does not belong to the current deployment"))
	}
	return nil
}

func logArguments(options LocalLogsOptions, follow bool) []string {
	args := []string{"logs", "--no-color", "--tail", strconv.Itoa(options.Tail)}
	if strings.TrimSpace(options.Since) != "" {
		args = append(args, "--since", strings.TrimSpace(options.Since))
	}
	if follow {
		args = append(args, "--follow")
	}
	if service := strings.TrimSpace(options.Service); service != "" {
		args = append(args, service)
	}
	return args
}

func selectedLogService(service string) string {
	if strings.TrimSpace(service) == "" {
		return "all"
	}
	return strings.TrimSpace(service)
}

func splitLogLines(value string, secrets []string) []string {
	scanner := bufio.NewScanner(strings.NewReader(value))
	lines := make([]string, 0)
	for scanner.Scan() {
		lines = append(lines, sanitizeLogLine(scanner.Text(), secrets))
	}
	return lines
}

type jsonLineWriter struct {
	mu      sync.Mutex
	out     io.Writer
	service string
	secrets []string
	buffer  strings.Builder
}

func newJSONLineWriter(out io.Writer, service string, secrets []string) *jsonLineWriter {
	return &jsonLineWriter{out: out, service: service, secrets: secrets}
}

func (w *jsonLineWriter) Write(data []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.buffer.Write(data)
	for {
		value := w.buffer.String()
		index := strings.IndexByte(value, '\n')
		if index < 0 {
			break
		}
		if err := w.writeLine(value[:index]); err != nil {
			return 0, err
		}
		w.buffer.Reset()
		w.buffer.WriteString(value[index+1:])
	}
	return len(data), nil
}

func (w *jsonLineWriter) Flush() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.buffer.Len() == 0 {
		return nil
	}
	line := w.buffer.String()
	w.buffer.Reset()
	return w.writeLine(line)
}

func (w *jsonLineWriter) writeLine(line string) error {
	data, err := json.Marshal(map[string]any{"service": w.service, "line": sanitizeLogLine(line, w.secrets)})
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(w.out, string(data))
	return err
}

type sanitizedLineWriter struct {
	mu      sync.Mutex
	out     io.Writer
	secrets []string
	buffer  strings.Builder
}

func newSanitizedLineWriter(out io.Writer, secrets []string) *sanitizedLineWriter {
	return &sanitizedLineWriter{out: out, secrets: secrets}
}

func (w *sanitizedLineWriter) Write(data []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.buffer.Write(data)
	for {
		value := w.buffer.String()
		index := strings.IndexByte(value, '\n')
		if index < 0 {
			break
		}
		if _, err := fmt.Fprintln(w.out, sanitizeLogLine(value[:index], w.secrets)); err != nil {
			return 0, err
		}
		w.buffer.Reset()
		w.buffer.WriteString(value[index+1:])
	}
	return len(data), nil
}

func (w *sanitizedLineWriter) Flush() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.buffer.Len() == 0 {
		return nil
	}
	line := sanitizeLogLine(w.buffer.String(), w.secrets)
	w.buffer.Reset()
	_, err := fmt.Fprintln(w.out, line)
	return err
}

func sanitizeLogLine(line string, secrets []string) string {
	line = strings.ReplaceAll(line, `\u001b`, "\x1b")
	line = ansiEscapePattern.ReplaceAllString(line, "")
	for _, secret := range secrets {
		if secret != "" {
			line = strings.ReplaceAll(line, secret, "[REDACTED]")
		}
	}
	return line
}

func readLogSecrets(record deploy.Record) []string {
	if strings.TrimSpace(record.EnvFile) == "" {
		return nil
	}
	data, err := os.ReadFile(record.EnvFile)
	if err != nil {
		return nil
	}
	secrets := make([]string, 0, 2)
	for _, line := range strings.Split(string(data), "\n") {
		name, value, ok := strings.Cut(line, "=")
		if !ok || !strings.HasSuffix(strings.TrimSpace(name), "_TOKEN") {
			continue
		}
		value = strings.TrimSpace(value)
		if len(value) >= 8 {
			secrets = append(secrets, value)
		}
	}
	return secrets
}
