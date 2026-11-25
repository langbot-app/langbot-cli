package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/langbot-app/langbot-cli/internal/deploy"
	"github.com/langbot-app/langbot-cli/internal/result"
)

type uninstallRunner struct {
	containers bool
	downCalls  int
	dockerCall func(...string) (deploy.CommandResult, error)
	downErr    error
}

func (r *uninstallRunner) Docker(_ context.Context, args ...string) (deploy.CommandResult, error) {
	if len(args) > 0 && args[0] == "run" && r.dockerCall != nil {
		return r.dockerCall(args...)
	}
	return deploy.CommandResult{Stdout: "29"}, nil
}

func (r *uninstallRunner) Compose(_ context.Context, _ deploy.Record, args ...string) (deploy.CommandResult, error) {
	call := strings.Join(args, " ")
	switch call {
	case "ps -a --format json":
		if !r.containers {
			return deploy.CommandResult{Stdout: "[]"}, nil
		}
		return deploy.CommandResult{Stdout: `[{"Service":"langbot","State":"exited"},{"Service":"langbot_plugin_runtime","State":"exited"}]`}, nil
	case "down --remove-orphans":
		r.downCalls++
		if r.downErr != nil {
			return deploy.CommandResult{}, r.downErr
		}
		r.containers = false
		return deploy.CommandResult{}, nil
	default:
		return deploy.CommandResult{}, fmt.Errorf("unexpected Compose command: %s", call)
	}
}

func TestUninstallPurgePermissionFallbackAfterDown(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("requires Unix permissions and a non-root user")
	}
	for _, mode := range []os.FileMode{0, 0o500} {
		for _, fail := range []bool{false, true} {
			t.Run(fmt.Sprintf("mode=%o/fail=%v", mode, fail), func(t *testing.T) {
				service, runner, record, dir := uninstallFixture(t, true)
				protected := filepath.Join(record.DataDir, "migration-backups")
				if err := os.Mkdir(protected, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(protected, "backup.db"), []byte("backup"), 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(protected, mode); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() {
					os.Chmod(protected, 0o700)
					os.Chmod(filepath.Join(dir, ".purge-data-"+record.DeploymentID, "migration-backups"), 0o700)
				})
				calls := 0
				var mounted string
				runner.dockerCall = func(args ...string) (deploy.CommandResult, error) {
					calls++
					if runner.downCalls != 1 || runner.containers {
						t.Fatal("cleanup must run after containers are removed")
					}
					joined := strings.Join(args, " ")
					for _, expected := range []string{"--rm", "--network none", "--user 0:0", "--entrypoint python", "rockchin/langbot:"} {
						if !strings.Contains(joined, expected) {
							t.Fatalf("missing %s: %s", expected, joined)
						}
					}
					for _, arg := range args {
						if strings.HasPrefix(arg, "type=bind,src=") {
							mounted = strings.TrimSuffix(strings.TrimPrefix(arg, "type=bind,src="), ",dst=/data")
						}
					}
					if fail {
						return deploy.CommandResult{Stderr: "cleanup denied"}, errors.New("helper failed")
					}
					if err := os.Chmod(filepath.Join(mounted, "migration-backups"), 0o700); err != nil {
						t.Fatal(err)
					}
					entries, err := os.ReadDir(mounted)
					if err != nil {
						t.Fatal(err)
					}
					for _, entry := range entries {
						if err := os.RemoveAll(filepath.Join(mounted, entry.Name())); err != nil {
							t.Fatal(err)
						}
					}
					return deploy.CommandResult{}, nil
				}
				value, err := service.Uninstall(t.Context(), UninstallOptions{
					CheckOptions: CheckOptions{Endpoint: record.Endpoint, EndpointSet: true}, PurgeData: true, Yes: true,
				})
				data := value.Data.(map[string]any)
				if calls != 1 || data["project_removed"] != true {
					t.Fatalf("calls=%d result=%#v err=%v", calls, data, err)
				}
				if fail {
					if err == nil || result.AsError(err).Kind != "recovery_required" || data["data_purged"] != false || data["recovery_path"] != mounted || !strings.Contains(err.Error(), mounted) || !strings.Contains(err.Error(), "cleanup denied") {
						t.Fatalf("result=%#v err=%v", data, err)
					}
					if _, statErr := os.Stat(mounted); statErr != nil {
						t.Fatal(statErr)
					}
				} else if err != nil || data["data_purged"] != true {
					t.Fatalf("result=%#v err=%v", data, err)
				}
			})
		}
	}
}

func TestUninstallDoesNotPurgeWhenDownFails(t *testing.T) {
	service, runner, record, _ := uninstallFixture(t, true)
	runner.downErr = errors.New("down failed")
	runner.dockerCall = func(...string) (deploy.CommandResult, error) {
		t.Fatal("cleanup must not run while containers remain")
		return deploy.CommandResult{}, nil
	}
	_, err := service.Uninstall(t.Context(), UninstallOptions{
		CheckOptions: CheckOptions{Endpoint: record.Endpoint, EndpointSet: true}, PurgeData: true, Yes: true,
	})
	if err == nil || !runner.containers {
		t.Fatalf("err=%v containers=%v", err, runner.containers)
	}
	if _, err := os.Stat(filepath.Join(record.DataDir, "config.yaml")); err != nil {
		t.Fatal(err)
	}
}

type uninstallTransport struct{ version string }

func (t uninstallTransport) RoundTrip(*http.Request) (*http.Response, error) {
	body := fmt.Sprintf(`{"code":0,"data":{"version":%q,"edition":"community"},"msg":"ok"}`, t.version)
	return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
}

func uninstallFixture(t *testing.T, containers bool) (*Service, *uninstallRunner, deploy.Record, string) {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, "deployment")
	assets, err := deploy.NewInstallAssets(dir, "v4.10.10", "basic", 15300, deploy.InstallNetworkOptions{})
	if err != nil {
		t.Fatal(err)
	}
	record := assets.Record("local-uninstall", "sha256:test")
	if err := (deploy.Store{Dir: dir}).Write(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	if err := assets.Write(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(record.DataDir, "config.yaml"), []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	runner := &uninstallRunner{containers: containers}
	transport := uninstallTransport{version: record.CoreVersion}
	service := New(Dependencies{
		LocalDir: dir, LocalRunner: runner, Transport: transport,
		DefaultConfigPath: func() (string, error) { return filepath.Join(root, "cli.yaml"), nil },
	})
	return service, runner, record, dir
}

func TestUninstallPreservesDataAndRecord(t *testing.T) {
	service, runner, record, dir := uninstallFixture(t, true)
	value, err := service.Uninstall(context.Background(), UninstallOptions{
		CheckOptions: CheckOptions{Endpoint: record.Endpoint, EndpointSet: true}, Yes: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	data := value.Data.(map[string]any)
	if data["project_removed"] != true || data["record_preserved"] != true || runner.downCalls != 1 {
		t.Fatalf("result=%#v down=%d", data, runner.downCalls)
	}
	for _, path := range []string{record.DataDir, record.ComposeFile, record.EnvFile, filepath.Join(dir, "deployment.yaml")} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("默认卸载误删了 %s: %v", path, err)
		}
	}
}

func TestUninstallPurgeRemovesDataAfterProject(t *testing.T) {
	service, runner, record, dir := uninstallFixture(t, true)
	backup := filepath.Join(dir, "backups", "keep", "manifest.yaml")
	if err := os.MkdirAll(filepath.Dir(backup), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(backup, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	value, err := service.Uninstall(context.Background(), UninstallOptions{
		CheckOptions: CheckOptions{Endpoint: record.Endpoint, EndpointSet: true}, PurgeData: true, Yes: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	data := value.Data.(map[string]any)
	if data["data_purged"] != true || runner.downCalls != 1 {
		t.Fatalf("result=%#v down=%d", data, runner.downCalls)
	}
	if _, err := os.Stat(record.DataDir); !os.IsNotExist(err) {
		t.Fatalf("数据目录未删除: %v", err)
	}
	for _, path := range []string{backup, record.ComposeFile, filepath.Join(dir, "deployment.yaml")} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("清理误删了 %s: %v", path, err)
		}
	}
}

func TestUninstallRequiresConfirmationAndBlocksInterruptedUpgrade(t *testing.T) {
	service, runner, record, dir := uninstallFixture(t, true)
	options := UninstallOptions{CheckOptions: CheckOptions{Endpoint: record.Endpoint, EndpointSet: true}}
	if _, err := service.Uninstall(context.Background(), options); result.AsError(err).Kind != "precondition" || runner.downCalls != 0 {
		t.Fatalf("缺少确认仍执行卸载: %v, down=%d", err, runner.downCalls)
	}
	if err := deploy.WriteUpgradeState(dir, deploy.UpgradeState{
		DeploymentID: record.DeploymentID, SourceVersion: "v4.10.9", TargetVersion: "v4.10.10", Phase: "compose_switched",
	}); err != nil {
		t.Fatal(err)
	}
	options.Yes = true
	if _, err := service.Uninstall(context.Background(), options); result.AsError(err).Kind != "recovery_required" || runner.downCalls != 0 {
		t.Fatalf("升级现场未阻止卸载: %v, down=%d", err, runner.downCalls)
	}
}

func TestUninstallPurgeRejectsAdoptedComposeBeforeDown(t *testing.T) {
	service, runner, record, dir := uninstallFixture(t, true)
	custom := record
	custom.ComposeProject = "custom"
	custom.Asset = deploy.AssetInfo{Name: "compose.yaml", Digest: "sha256:test"}
	if err := (deploy.Store{Dir: dir}).Write(context.Background(), custom); err != nil {
		t.Fatal(err)
	}
	_, err := service.Uninstall(context.Background(), UninstallOptions{
		CheckOptions: CheckOptions{Endpoint: record.Endpoint, EndpointSet: true}, PurgeData: true, Yes: true,
	})
	if result.AsError(err).Kind != "precondition" || runner.downCalls != 0 {
		t.Fatalf("自定义 Compose 清理没有在 down 前拒绝: %v, down=%d", err, runner.downCalls)
	}
	if _, statErr := os.Stat(record.DataDir); statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("数据目录状态异常: %v", statErr)
	}
}

func TestNativeUninstallPurgeReportsDataPurged(t *testing.T) {
	// A retired native deployment skips service teardown, so purge exercises
	// only the data removal and the reported summary.
	service, _, record, dir := uninstallFixture(t, false)
	record.Driver, record.Uninstalled = deploy.DriverNative, true
	if err := (deploy.Store{Dir: dir}).Write(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	value, err := service.Uninstall(context.Background(), UninstallOptions{
		CheckOptions: CheckOptions{LocalDir: dir}, PurgeData: true, Yes: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	data := value.Data.(map[string]any)
	if data["project_removed"] != true || data["data_purged"] != true {
		t.Fatalf("result=%#v", data)
	}
	if _, err := os.Stat(record.DataDir); !os.IsNotExist(err) {
		t.Fatalf("data dir not removed: %v", err)
	}
}
