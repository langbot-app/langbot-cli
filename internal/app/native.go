package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/langbot-app/langbot-cli/internal/deploy"
	"github.com/langbot-app/langbot-cli/internal/result"
)

func (s *Service) nativeOutput() io.Writer {
	if s.deps.LocalLogErr != nil {
		return s.deps.LocalLogErr
	}
	return io.Discard
}

func (s *Service) installNative(ctx context.Context, options InstallOptions) (Result, error) {
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
		version, err = s.latestRelease(ctx)
		if err != nil {
			return Result{}, err
		}
	}
	if _, err := deploy.CompareReleaseVersions("v0.0.0", version); err != nil {
		return Result{}, result.New("input", err.Error())
	}
	if options.Port < 1 || options.Port > 65535 {
		return Result{}, result.New("input", "invalid port")
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
	for _, extra := range record.Extras {
		if extra != "seekdb" && extra != "milvus" && extra != "valkey" {
			return Result{}, result.New("input", "unsupported --extra: "+extra)
		}
	}
	if existing, err := (deploy.Store{Dir: dir}).Read(); err == nil {
		if existing.Driver != deploy.DriverNative || existing.CoreVersion != record.CoreVersion || existing.Port != record.Port {
			return Result{}, result.New("precondition", "deployment already exists; use upgrade or another --name")
		}
		record = existing
	} else if err := deploy.ValidateInstallTarget(dir, true); err != nil {
		return Result{}, result.New("precondition", err.Error())
	}
	if !deploy.NativeRunning(dir) {
		if err := deploy.CheckNativePorts(record); err != nil {
			return Result{}, result.New("precondition", err.Error())
		}
	}
	data := map[string]any{"action": "install", "url": record.Endpoint, "driver": record.Driver, "instance_dir": dir, "version": version,
		"name": record.Name, "region": record.Region, "service_manager": record.ServiceManager, "checks": deploy.NativeCapabilities(), "record": record}
	if options.DryRun {
		data["dry_run"] = true
		return Result{Data: data}, nil
	}
	locator := deploy.Locator{Path: s.deps.LocalLocatorPath}
	err = locator.WithLock(ctx, func() error {
		return deploy.WithMutationLock(ctx, dir, func() error {
			if err := s.checkDeploymentRegistration(dir, record); err != nil {
				return err
			}
			if existing, err := (deploy.Store{Dir: dir}).Read(); err == nil {
				if existing.DeploymentID != record.DeploymentID {
					return result.New("precondition", "deployment changed")
				}
				if deploy.NativeRunning(dir) {
					return nil
				}
			} else if err := deploy.ValidateInstallTarget(dir, true); err != nil {
				return err
			}
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
			if err := deploy.PrepareNative(ctx, dir, record, s.nativeOutput()); err != nil {
				return err
			}
			if err := deploy.InstallNativeService(ctx, dir, record); err != nil {
				return err
			}
			if err := deploy.NativeStart(ctx, dir, record); err != nil {
				return err
			}
			if err := deploy.WaitNativeHealth(ctx, record, s.deps.LocalHTTP); err != nil {
				return err
			}
			if record.ServiceManager == "systemd-user" {
				fmt.Fprintln(s.nativeOutput(), "Run loginctl enable-linger to keep LangBot running after logout.")
			}
			for _, check := range deploy.NativeCapabilities() {
				fmt.Fprintf(s.nativeOutput(), "%s: %s (%s)\n", check.Name, check.Status, check.Reason)
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
	} else {
		data["step"] = "install_failed"
	}
	return Result{Data: data}, nativeMutationError(err)
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
			return fmt.Errorf("deployment changed")
		}
		if action != "start" {
			if err := deploy.NativeStop(ctx, diagnostics.Dir, current); err != nil {
				return err
			}
		}
		if action != "stop" {
			if err := deploy.NativeStart(ctx, diagnostics.Dir, current); err != nil {
				return err
			}
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
	return result.New("server", err.Error())
}
