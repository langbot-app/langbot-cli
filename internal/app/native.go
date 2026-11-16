package app

import (
	"context"
	"errors"
	"fmt"
	"github.com/langbot-app/langbot-cli/internal/i18n"
	"github.com/langbot-app/langbot-cli/internal/progress"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/langbot-app/langbot-cli/internal/deploy"
	"github.com/langbot-app/langbot-cli/internal/result"
	"github.com/langbot-app/langbot-cli/internal/telemetry"
)

func (s *Service) nativeOutput() io.Writer {
	if s.deps.LocalLogErr != nil {
		return s.deps.LocalLogErr
	}
	return io.Discard
}

func (s *Service) installNative(ctx context.Context, options InstallOptions) (Result, error) {
	ctx = telemetry.TrackSteps(ctx)
	telemetry.SetEnvironment(ctx, "native", "", options.Region, options.Version)
	timeout, err := operationTimeout(options.Timeout, options.TimeoutSet, 20*time.Minute)
	if err != nil {
		return Result{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	dir, err := s.resolveLocalDir(options.Dir)
	if err != nil {
		return Result{}, err
	}
	version := options.Version
	if version == "" {
		if existing, readErr := (deploy.Store{Dir: dir}).Read(); readErr == nil && existing.Driver == deploy.DriverNative {
			version = existing.CoreVersion
		}
	}
	telemetry.SetStep(ctx, "resolve_version")
	if version == "" {
		version, err = s.latestRelease(ctx)
		if err != nil {
			return Result{}, err
		}
	}
	if _, err := deploy.CompareReleaseVersions("v0.0.0", version); err != nil {
		return Result{}, result.New("input", err.Error())
	}
	if options.Port < 1 || options.Port > 65535 {
		return Result{}, result.New("input", i18n.T("invalid port"))
	}
	home, err := deploy.Home()
	if err != nil {
		return Result{}, err
	}
	region, err := deploy.ResolveRegion(ctx, options.Region, s.deps.LocalHTTP)
	if err != nil {
		return Result{}, err
	}
	id, err := deploy.NewDeploymentID()
	if err != nil {
		return Result{}, err
	}
	record := deploy.Record{Driver: deploy.DriverNative, DeploymentID: id, Name: options.Name, CoreVersion: version, Port: options.Port,
		DataDir: filepath.Join(dir, "data"), Endpoint: fmt.Sprintf("http://127.0.0.1:%d", options.Port), ToolsDir: filepath.Join(home, "tools"),
		Region: region, PyPIIndex: options.PyPIIndex, PythonMirror: options.PythonMirror, Extras: options.Extras, ServiceManager: deploy.SelectNativeManager(ctx)}
	deploy.ApplyRegion(&record)
	s.deploymentContext(ctx, record, dir, options.Region)
	telemetry.SetEnvironment(ctx, record.Driver, record.ServiceManager, record.Region, record.CoreVersion)
	telemetry.SetStep(ctx, "validate_target")
	record.Autostart = new(record.ServiceManager != "process")
	for _, extra := range record.Extras {
		if extra != "seekdb" && extra != "milvus" && extra != "valkey" {
			return Result{}, result.New("input", i18n.T("unsupported --extra: ")+extra)
		}
	}
	if existing, err := (deploy.Store{Dir: dir}).Read(); err == nil {
		if existing.Driver != deploy.DriverNative || existing.CoreVersion != record.CoreVersion || existing.Port != record.Port {
			data := map[string]any{"action": "install", "step": "validate_target", "preflight": true}
			if existing.Driver == deploy.DriverNative {
				nativeInstallRecovery(data, dir, existing, false)
			}
			return Result{Data: data}, result.New("precondition", fmt.Sprintf(i18n.T("deployment already exists (%s, %s, port %d); reuse its installation options or use lbctl start --endpoint %s"), existing.Driver, existing.CoreVersion, existing.Port, nativeShellQuote(existing.Endpoint)))
		}
		record = existing
		record.Uninstalled = false
		if !deploy.NativeRunning(dir) {
			record.ServiceManager = deploy.NativeInstallManager(record)
		}
		if record.Autostart == nil {
			record.Autostart = new(record.ServiceManager != "process")
		}
	} else if err := deploy.ValidateInstallTarget(dir, true); err != nil {
		return Result{}, result.New("precondition", err.Error())
	}
	data := map[string]any{"action": "install", "url": record.Endpoint, "driver": record.Driver, "instance_dir": dir, "version": version,
		"name": record.Name, "region": record.Region, "service_manager": record.ServiceManager, "checks": deploy.NativeCapabilities(), "record": record}
	telemetry.SetStep(ctx, "register")
	if err := s.checkDeploymentRegistration(dir, record); err != nil {
		data["step"] = "register"
		data["preflight"] = true
		return Result{Data: data}, err
	}
	if !deploy.NativeRunning(dir) {
		telemetry.SetStep(ctx, "check_ports")
		if err := deploy.CheckNativePorts(record); err != nil {
			data["step"] = "check_ports"
			data["preflight"] = true
			data["checks"] = append(data["checks"].([]deploy.Check), deploy.Check{Name: "ports", Status: "error", Reason: err.Error()})
			var conflict *deploy.NativePortError
			if errors.As(err, &conflict) {
				data["conflict_port"] = conflict.Port
			}
			nativeInstallRecovery(data, dir, record, false)
			return Result{Data: data}, result.New("precondition", err.Error())
		}
		data["checks"] = append(data["checks"].([]deploy.Check), deploy.Check{Name: "ports", Status: "ok", Reason: i18n.T("Native deployment ports are available")})
	}
	if options.DryRun {
		data["dry_run"] = true
		return Result{Data: data}, nil
	}
	locator := deploy.Locator{Path: s.deps.LocalLocatorPath}
	telemetry.SetStep(ctx, "register")
	logPath := filepath.Join(dir, "logs", "install.log")
	var installLog *os.File
	defer func() {
		if installLog != nil {
			installLog.Close()
		}
	}()
	err = locator.WithLock(ctx, func() error {
		return deploy.WithMutationLock(ctx, dir, func() error {
			if err := s.checkDeploymentRegistration(dir, record); err != nil {
				return err
			}
			if existing, err := (deploy.Store{Dir: dir}).Read(); err == nil {
				if existing.DeploymentID != record.DeploymentID {
					return result.New("precondition", i18n.T("deployment changed"))
				}
				if deploy.NativeRunning(dir) {
					return nil
				}
			} else if err := deploy.ValidateInstallTarget(dir, true); err != nil {
				return err
			}
			telemetry.SetStep(ctx, "write_record")
			if err := (deploy.Store{Dir: dir}).Write(ctx, record); err != nil {
				return err
			}
			data["changed"] = true
			if err := locator.Write(dir); err != nil {
				return err
			}
			if err := os.MkdirAll(record.DataDir, 0o700); err != nil {
				return err
			}
			telemetry.SetStep(ctx, "prepare_logs")
			if err := os.MkdirAll(filepath.Dir(logPath), 0o700); err != nil {
				return err
			}
			var logErr error
			installLog, logErr = os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
			if logErr != nil {
				return logErr
			}
			if err := deploy.PrepareNative(ctx, dir, record, io.MultiWriter(progress.Output(ctx, s.nativeOutput()), installLog)); err != nil {
				return err
			}
			telemetry.SetStep(ctx, "install_service")
			if err := deploy.InstallNativeService(ctx, dir, record); err != nil {
				return err
			}
			telemetry.SetStep(ctx, "start_service")
			if err := deploy.NativeStart(ctx, dir, record); err != nil {
				return err
			}
			telemetry.SetStep(ctx, "health_check")
			if err := deploy.WaitNativeHealth(ctx, record, s.deps.LocalHTTP); err != nil {
				return err
			}
			if record.ServiceManager == "systemd-user" {
				fmt.Fprintln(progress.Output(ctx, s.nativeOutput()), i18n.Human("Run loginctl enable-linger to keep LangBot running after logout."))
			}
			for _, check := range deploy.NativeCapabilities() {
				fmt.Fprintf(progress.Output(ctx, s.nativeOutput()), "%s: %s (%s)\n", check.Name, check.Status, i18n.Human(check.Reason))
			}
			return nil
		})
	})
	if saved, readErr := (deploy.Store{Dir: dir}).Read(); readErr == nil {
		data["record"] = saved
	}
	if err == nil {
		data["changed"] = true
		data["status"] = map[string]any{"status": "running"}
		installAutostartData(data, record, dir)
	} else {
		data["step"] = telemetry.CurrentStep(ctx)
		for key, path := range map[string]string{"log_path": logPath, "runtime_log_path": filepath.Join(dir, "logs", "langbot.log")} {
			if info, statErr := os.Stat(path); statErr == nil && info.Mode().IsRegular() {
				data[key] = path
			}
		}
		nativeInstallRecovery(data, dir, record, data["changed"] == true)
		if installLog != nil {
			fmt.Fprintf(installLog, "%s: %v\n", telemetry.CurrentStep(ctx), err)
		}
	}
	return Result{Data: data}, nativeMutationError(err)
}

func nativeInstallRecovery(data map[string]any, dir string, record deploy.Record, rollback bool) {
	command := fmt.Sprintf("lbctl install --driver native --dir %s --version %s --port %d --yes", nativeShellQuote(dir), record.CoreVersion, record.Port)
	args := []string{"install", "--driver", "native", "--dir", dir, "--version", record.CoreVersion, "--port", fmt.Sprint(record.Port), "--yes"}
	for _, option := range [][2]string{{"--name", record.Name}, {"--region", record.Region}, {"--pypi-index", record.PyPIIndex}, {"--python-mirror", record.PythonMirror}, {"--extra", strings.Join(record.Extras, ",")}} {
		if option[1] != "" {
			args = append(args, option[0], option[1])
			command += " " + option[0] + " " + nativeShellQuote(option[1])
		}
	}
	data["retry_command"], data["retry_argv"] = command, args
	if rollback {
		data["rollback_command"] = "lbctl uninstall --dir " + nativeShellQuote(dir) + " --yes"
		data["rollback_argv"] = []string{"uninstall", "--dir", dir, "--yes"}
	}
}

func nativeShellQuote(value string) string {
	if runtime.GOOS == "windows" {
		return "'" + strings.ReplaceAll(value, "'", "''") + "'"
	}
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

func (s *Service) nativeLifecycle(ctx context.Context, diagnostics deploy.Diagnostics, record deploy.Record, action string) (Result, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	err := deploy.WithMutationLock(ctx, diagnostics.Dir, func() error {
		current, err := diagnostics.Store.Read()
		if err != nil {
			return err
		}
		if current.DeploymentID != record.DeploymentID {
			return errors.New(i18n.T("deployment changed"))
		}
		if action != "start" {
			telemetry.SetStep(ctx, "stop_service")
			if err := deploy.NativeStop(ctx, diagnostics.Dir, current); err != nil {
				return err
			}
		}
		if action != "stop" {
			telemetry.SetStep(ctx, "start_service")
			if err := deploy.NativeStart(ctx, diagnostics.Dir, current); err != nil {
				return err
			}
			telemetry.SetStep(ctx, "health_check")
			return deploy.WaitNativeHealth(ctx, current, s.deps.LocalHTTP)
		}
		return nil
	})
	data := map[string]any{"action": action, "record": record}
	if err == nil {
		state := "running"
		if action == "stop" {
			state = "stopped"
		}
		data["changed"] = true
		data["status"] = map[string]any{"status": state}
	}
	return Result{Data: data}, nativeMutationError(err)
}

func nativeMutationError(err error) error {
	if err == nil {
		return nil
	}
	var typed *result.Error
	if errors.As(err, &typed) {
		return typed
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, deploy.ErrNativeInterrupted) {
		return result.New("interrupted", err.Error())
	}
	if errors.Is(err, deploy.ErrNativeDependency) {
		return result.New("dependency", err.Error())
	}
	return result.New("server", err.Error())
}
