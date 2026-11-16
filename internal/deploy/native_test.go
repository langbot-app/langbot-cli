package deploy

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestNativeEnvironmentPreservesWebhookConfiguration(t *testing.T) {
	_, record := nativeFixture(t)
	record.Endpoint = "http://127.0.0.1:15300"
	record.Port = 15300
	readEnvironment := func() map[string]string {
		env := map[string]string{}
		for _, entry := range NativeEnvironment(record) {
			name, value, _ := strings.Cut(entry, "=")
			env[name] = value
		}
		return env
	}
	if got := readEnvironment()["API__WEBHOOK_PREFIX"]; got != record.Endpoint {
		t.Fatalf("initial webhook = %q", got)
	}
	if err := os.WriteFile(filepath.Join(record.DataDir, "config.yaml"), []byte("api:\n  webhook_prefix: https://bot.example.com\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, exists := readEnvironment()["API__WEBHOOK_PREFIX"]; exists {
		t.Fatal("restart overrides user webhook")
	}
	if got := readEnvironment()["API__PORT"]; got != "15300" {
		t.Fatalf("managed API port = %q", got)
	}
}

func TestNativePortsRejectRuntimeConflicts(t *testing.T) {
	for _, port := range []int{5401, 5410} {
		t.Run(fmt.Sprint(port), func(t *testing.T) {
			listener, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			err = CheckNativePorts(Record{Port: 0})
			if err == nil || !strings.Contains(err.Error(), fmt.Sprint(port)) || !strings.Contains(err.Error(), "Docker") {
				t.Fatalf("missing conflict diagnostic: %v", err)
			}
		})
	}
	for _, port := range []int{5401, 5410} {
		if err := CheckNativePorts(Record{Port: port}); err == nil {
			t.Fatalf("HTTP overlaps runtime port %d", port)
		}
	}
	if err := CheckNativePorts(Record{Port: 0}); err != nil {
		t.Fatalf("ports not released: %v", err)
	}
}

func TestProbeNativePortRejectsListeningSockets(t *testing.T) {
	for _, address := range []string{":0", "0.0.0.0:0", "127.0.0.1:0", "[::1]:0"} {
		t.Run(address, func(t *testing.T) {
			network := "tcp"
			if address == "0.0.0.0:0" {
				network = "tcp4"
			}
			listener, err := net.Listen(network, address)
			if err != nil {
				if address == "[::1]:0" {
					t.Skipf("IPv6 unavailable: %v", err)
				}
				t.Fatal(err)
			}
			defer listener.Close()
			port := listener.Addr().(*net.TCPAddr).Port
			if err := probeNativePort(port); err == nil || !strings.Contains(err.Error(), "already accepting connections") {
				t.Fatalf("listener %s was not detected by TCP dial: %v", listener.Addr(), err)
			}
		})
	}
}

func TestNativeExecutablePreservesStableSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink requires Windows privileges")
	}
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	link := filepath.Join(dir, "lbctl")
	if err := os.Symlink(binary, link); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	got, err := nativeExecutable()
	if err != nil || got != link {
		t.Fatalf("stable executable = %q, %v", got, err)
	}
}

func nativeFixture(t *testing.T) (string, Record) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "instance")
	record := Record{Driver: DriverNative, DeploymentID: "native-test", Name: "test", CoreVersion: "v4.11.7", Port: 5300, DataDir: filepath.Join(dir, "data"), ServiceManager: "process"}
	if err := (Store{Dir: dir}).Write(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(record.DataDir, 0o700); err != nil {
		t.Fatal(err)
	}
	return dir, record
}

func TestNativeUpgradeRestoresVersionAndData(t *testing.T) {
	for _, failure := range []string{"", "prepare", "health"} {
		t.Run(failure, func(t *testing.T) {
			dir, record := nativeFixture(t)
			record.Autostart = new(false)
			if err := (Store{Dir: dir}).Write(context.Background(), record); err != nil {
				t.Fatal(err)
			}
			dataPath := filepath.Join(record.DataDir, "state.txt")
			if err := os.WriteFile(dataPath, []byte("original"), 0o600); err != nil {
				t.Fatal(err)
			}
			calls := []string{}
			operations := nativeUpgradeOperations{
				prepare: func(context.Context, string, Record, io.Writer) error {
					calls = append(calls, "prepare")
					if failure == "prepare" {
						return fmt.Errorf("download failed")
					}
					return nil
				},
				stop: func(_ context.Context, _ string, record Record) error {
					calls = append(calls, "stop:"+record.CoreVersion)
					return nil
				},
				start: func(_ context.Context, _ string, record Record) error {
					if record.Autostart == nil || *record.Autostart {
						t.Fatal("upgrade re-enabled autostart")
					}
					calls = append(calls, "start:"+record.CoreVersion)
					if record.CoreVersion == "v4.11.8" {
						return os.WriteFile(dataPath, []byte("migrated"), 0o600)
					}
					return nil
				},
				health: func(_ context.Context, record Record, _ *http.Client) error {
					if failure == "health" && record.CoreVersion == "v4.11.8" {
						return fmt.Errorf("healthz 503")
					}
					return nil
				},
			}
			backup, err := upgradeNative(context.Background(), dir, record, "v4.11.8", io.Discard, nil, operations)
			current, readErr := (Store{Dir: dir}).Read()
			if readErr != nil {
				t.Fatal(readErr)
			}
			if current.Autostart == nil || *current.Autostart {
				t.Fatal("autostart preference was not preserved")
			}
			data, readErr := os.ReadFile(dataPath)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if failure == "" {
				if err != nil || backup == "" || current.CoreVersion != "v4.11.8" || string(data) != "migrated" {
					t.Fatalf("upgrade: %s %+v %v", data, current, err)
				}
			} else if err == nil || current.CoreVersion != "v4.11.7" || string(data) != "original" {
				t.Fatalf("rollback: %s %+v %v", data, current, err)
			}
			if failure == "prepare" && !reflect.DeepEqual(calls, []string{"prepare"}) {
				t.Fatalf("stopped before preparing: %v", calls)
			}
			if failure == "health" && !strings.Contains(err.Error(), "previous version and data restored") {
				t.Fatal(err)
			}
		})
	}
}

func TestNativeBackupExcludesRebuildableDataAndRetainsThree(t *testing.T) {
	dir, record := nativeFixture(t)
	for _, name := range []string{"config.yaml", "cache/file", "plugins/example/.venv/file", "plugins/example/state"} {
		path := filepath.Join(record.DataDir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("data"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	var backup string
	for count := 0; count < 4; count++ {
		var err error
		backup, err = BackupNative(context.Background(), dir, record)
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := pruneNativeBackups(dir); err != nil {
		t.Fatal(err)
	}
	backups, _ := filepath.Glob(filepath.Join(dir, "backups", "native-*"))
	if len(backups) != 3 {
		t.Fatal(backups)
	}
	for _, name := range []string{"config.yaml", "plugins/example/state"} {
		if _, err := os.Stat(filepath.Join(backup, "data", name)); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"cache", "plugins/example/.venv"} {
		if _, err := os.Stat(filepath.Join(backup, "data", name)); !os.IsNotExist(err) {
			t.Fatalf("unexpected backup: %s", name)
		}
	}
}

func TestNativeEnvironmentAndPinnedUVPlatforms(t *testing.T) {
	for _, platform := range []string{"linux/amd64", "linux/arm64", "darwin/amd64", "darwin/arm64", "windows/amd64", "windows/arm64"} {
		if asset := UVAssets[platform]; asset.Name == "" || len(asset.SHA256) != 64 {
			t.Fatal(platform)
		}
	}
	env := strings.Join(NativeEnvironment(Record{ToolsDir: "/managed/tools", DataDir: "/managed/data", Port: 5310, PyPIIndex: "https://example.test/simple"}), "\n")
	for _, value := range []string{"API__PORT=5310", "LANGBOT_DATA_ROOT=/managed/data", "UV_PYTHON_PREFERENCE=only-managed", "LANGBOT_PLUGIN_PYPI_INDEX_URL=https://example.test/simple"} {
		if !strings.Contains(env, value) {
			t.Fatalf("missing %s", value)
		}
	}
}

func TestExtractUVOnlySelectsExecutable(t *testing.T) {
	var buffer bytes.Buffer
	gzipWriter := gzip.NewWriter(&buffer)
	archive := tar.NewWriter(gzipWriter)
	for _, name := range []string{"uv-platform/uvx", "uv-platform/uv"} {
		content := []byte(name)
		if err := archive.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(content)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := archive.Write(content); err != nil {
			t.Fatal(err)
		}
	}
	archive.Close()
	gzipWriter.Close()
	binary, err := extractUV(buffer.Bytes(), "uv.tar.gz", "uv")
	if err != nil || string(binary) != "uv-platform/uv" {
		t.Fatalf("%s %v", binary, err)
	}
}

func TestNativeLogRotationAndFollowCancellation(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "logs"), 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "logs", "langbot.log")
	log := &rotatingLog{path: path}
	chunk := bytes.Repeat([]byte("x"), 6<<20)
	for count := 0; count < 6; count++ {
		if _, err := log.Write(chunk); err != nil {
			t.Fatal(err)
		}
	}
	log.file.Close()
	files, _ := filepath.Glob(path + "*")
	if len(files) != 4 {
		t.Fatalf("unbounded logs: %v", files)
	}
	if err := os.WriteFile(path, []byte("first\nsecond\nthird\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := NativeLogs(context.Background(), dir, &output, 2, false); err != nil || output.String() != "second\nthird\n" {
		t.Fatalf("%s %v", output.String(), err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := NativeLogs(ctx, dir, io.Discard, 2, true); err != context.DeadlineExceeded {
		t.Fatal(err)
	}
}

func TestNativeHealthUsesHealthz(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/healthz" {
			t.Errorf("wrong path %s", request.URL.Path)
		}
		writer.WriteHeader(200)
	}))
	defer server.Close()
	if err := NativeHealth(context.Background(), Record{Endpoint: server.URL}, server.Client()); err != nil {
		t.Fatal(err)
	}
}

func TestNativeHealthFailsWhenProcessExited(t *testing.T) {
	dir, record := nativeFixture(t)
	record.Endpoint = "http://127.0.0.1:1"
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	err := WaitNativeHealth(ctx, record, nil)
	if err == nil || !strings.Contains(err.Error(), "process exited") {
		t.Fatalf("%s: %v", dir, err)
	}
}

func TestDockerMirrorSettingsSurviveUpgrade(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "instance")
	assets, err := NewInstallAssets(dir, "v4.11.7", "basic", 5300, InstallNetworkOptions{})
	if err != nil {
		t.Fatal(err)
	}
	repository, index := "docker.langbot.app/rockchin/langbot", "https://mirrors.aliyun.com/pypi/simple"
	if err := assets.SetImageRepository(repository); err != nil {
		t.Fatal(err)
	}
	assets.SetPackageIndex(index)
	record := assets.Record("mirror-test", "")
	record.ImageRepository, record.PyPIIndex = repository, index
	if err := (Store{Dir: dir}).Write(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	if err := assets.Write(); err != nil {
		t.Fatal(err)
	}
	if err := ValidateOwnedLayout(record, dir); err != nil {
		t.Fatal(err)
	}
	content, _, err := ComposeForUpgrade(record, dir, "v4.11.8")
	if err != nil || !strings.Contains(string(content), repository+":v4.11.8") || strings.Count(string(content), index) != 2 {
		t.Fatalf("%s %v", content, err)
	}
}
