package deploy

import (
	"context"
	"errors"
	"fmt"
	"github.com/langbot-app/langbot-cli/internal/i18n"
	"github.com/langbot-app/langbot-cli/internal/progress"
	"io"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/langbot-app/langbot-cli/internal/telemetry"
)

func NativeCapabilities() []Check {
	available := func(names ...string) bool {
		for _, name := range names {
			if _, err := exec.LookPath(name); err == nil {
				return true
			}
		}
		return false
	}
	checks := []Check{}
	for _, item := range []struct {
		name, reason string
		ok           bool
	}{
		{"box_sandbox", i18n.T("Box sandbox requires Docker, Podman or nsjail"), available("docker", "podman", "nsjail")},
		{"stdio_mcp", i18n.T("stdio MCP requires node and npx"), available("node") && available("npx")},
		{"audio", i18n.T("Audio processing requires ffmpeg"), available("ffmpeg")},
	} {
		status := "ok"
		if !item.ok {
			status = "warn"
		}
		checks = append(checks, Check{Name: item.name, Status: status, Reason: item.reason})
	}
	return checks
}

func NativeHealth(ctx context.Context, record Record, client *http.Client) error {
	if client == nil {
		client = &http.Client{Timeout: 3 * time.Second}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(record.Endpoint, "/")+"/healthz", nil)
	if err != nil {
		return err
	}
	response, err := client.Do(req)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf(i18n.T("healthz: HTTP %d"), response.StatusCode)
	}
	return nil
}

func WaitNativeHealth(ctx context.Context, record Record, client *http.Client) error {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		progress.HealthFile(ctx, filepath.Join(filepath.Dir(record.DataDir), "logs", "langbot.log"))
		if err := NativeHealth(ctx, record, client); err == nil {
			return nil
		}
		if !NativeRunning(filepath.Dir(record.DataDir)) {
			return errors.New(i18n.T("native process exited before becoming healthy; inspect logs"))
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func nativeCopyData(ctx context.Context, source, target string) error {
	return filepath.WalkDir(source, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		rel, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		if entry.IsDir() {
			switch entry.Name() {
			case "__pycache__", ".cache", "cache", ".venv", "venvs":
				if rel != "." {
					return filepath.SkipDir
				}
			}
			return os.MkdirAll(filepath.Join(target, rel), 0o700)
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf(i18n.T("cannot back up symlink %s"), rel)
		}
		input, err := os.Open(path)
		if err != nil {
			return err
		}
		defer input.Close()
		output, err := os.OpenFile(filepath.Join(target, rel), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(output, contextReader{ctx: ctx, Reader: input})
		closeErr := output.Close()
		if copyErr != nil {
			return copyErr
		}
		return closeErr
	})
}

func BackupNative(ctx context.Context, dir string, record Record) (string, error) {
	telemetry.SetStep(ctx, "backup_data")
	root := filepath.Join(dir, "backups")
	if err := os.MkdirAll(root, 0o700); err != nil {
		return "", err
	}
	backup := filepath.Join(root, "native-"+time.Now().UTC().Format("20060102T150405.000000000"))
	if err := nativeCopyData(ctx, record.DataDir, filepath.Join(backup, "data")); err != nil {
		return "", err
	}
	return backup, nil
}

func pruneNativeBackups(dir string) error {
	backups, err := filepath.Glob(filepath.Join(dir, "backups", "native-*"))
	if err != nil {
		return err
	}
	sort.Strings(backups)
	for len(backups) > 3 {
		if err := os.RemoveAll(backups[0]); err != nil {
			return err
		}
		backups = backups[1:]
	}
	return nil
}

func UpgradeNative(ctx context.Context, dir string, current Record, version string, out io.Writer, client *http.Client, beforeStart func(Record)) (string, error) {
	return upgradeNative(ctx, dir, current, version, progress.Output(ctx, out), client, nativeUpgradeOperations{PrepareNative, NativeStart, NativeStop, WaitNativeHealth, beforeStart})
}

type nativeUpgradeOperations struct {
	prepare     func(context.Context, string, Record, io.Writer) error
	start       func(context.Context, string, Record) error
	stop        func(context.Context, string, Record) error
	health      func(context.Context, Record, *http.Client) error
	beforeStart func(Record)
}

func upgradeNative(ctx context.Context, dir string, current Record, version string, out io.Writer, client *http.Client, operations nativeUpgradeOperations) (string, error) {
	comparison, err := CompareReleaseVersions(current.CoreVersion, version)
	if err != nil {
		return "", err
	}
	if comparison >= 0 {
		return "", errors.New(i18n.T("target version must be newer"))
	}
	next := current
	next.CoreVersion = version
	if err := operations.prepare(ctx, dir, next, out); err != nil {
		return "", err
	}
	telemetry.SetStep(ctx, "stop_service")
	if err := operations.stop(ctx, dir, current); err != nil {
		return "", err
	}
	backup, err := BackupNative(ctx, dir, current)
	if err != nil {
		_ = operations.start(ctx, dir, current)
		return "", err
	}
	store := Store{Dir: dir}
	telemetry.SetStep(ctx, "write_record")
	err = store.Write(ctx, next)
	if err == nil && operations.beforeStart != nil {
		operations.beforeStart(next)
	}
	if err == nil {
		telemetry.SetStep(ctx, "start_service")
		err = operations.start(ctx, dir, next)
	}
	if err == nil {
		telemetry.SetStep(ctx, "health_check")
		healthCtx, cancel := context.WithTimeout(ctx, 3*time.Minute)
		err = operations.health(healthCtx, next, client)
		cancel()
	}
	if err == nil {
		return backup, pruneNativeBackups(dir)
	}
	recoveryCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Minute)
	defer cancel()
	if stopErr := operations.stop(recoveryCtx, dir, next); stopErr != nil {
		return backup, fmt.Errorf(i18n.T("upgrade failed: %v; rollback stop failed: %w"), err, stopErr)
	}
	failedData := filepath.Join(backup, "failed-data")
	if restoreErr := os.Rename(current.DataDir, failedData); restoreErr != nil {
		return backup, fmt.Errorf(i18n.T("rollback data move: %w"), restoreErr)
	}
	if restoreErr := nativeCopyData(recoveryCtx, filepath.Join(backup, "data"), current.DataDir); restoreErr != nil {
		return backup, fmt.Errorf(i18n.T("rollback data restore: %w"), restoreErr)
	}
	if restoreErr := store.Write(recoveryCtx, current); restoreErr != nil {
		return backup, restoreErr
	}
	if startErr := operations.start(recoveryCtx, dir, current); startErr != nil {
		return backup, fmt.Errorf(i18n.T("rollback start: %w"), startErr)
	}
	if healthErr := operations.health(recoveryCtx, current, client); healthErr != nil {
		return backup, fmt.Errorf(i18n.T("rollback health: %w"), healthErr)
	}
	_ = pruneNativeBackups(dir)
	return backup, fmt.Errorf(i18n.T("upgrade failed; previous version and data restored: %w"), err)
}
