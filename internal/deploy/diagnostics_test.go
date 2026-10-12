package deploy

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type fakeRunner struct {
	docker  map[string]CommandResult
	compose CommandResult
	err     error
	calls   [][]string
}

func (f *fakeRunner) Docker(_ context.Context, args ...string) (CommandResult, error) {
	f.calls = append(f.calls, append([]string{"docker"}, args...))
	key := strings.Join(args, " ")
	result, ok := f.docker[key]
	if !ok {
		return CommandResult{}, f.err
	}
	return result, nil
}

func (f *fakeRunner) Compose(_ context.Context, _ Record, args ...string) (CommandResult, error) {
	f.calls = append(f.calls, append([]string{"docker", "compose"}, args...))
	return f.compose, f.err
}

func TestStoreWriteReadIsAtomicAndManaged(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "deployment")
	now := time.Date(2026, 9, 11, 8, 0, 0, 0, time.UTC)
	store := Store{Dir: dir, Now: func() time.Time { return now }}
	record := Record{CoreVersion: "v4.10.10", Profile: "basic", ComposeProject: "langbot", ComposeFile: filepath.Join(dir, "compose.yaml"), DataDir: filepath.Join(dir, "data")}
	if err := store.Write(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	got, err := store.Read()
	if err != nil {
		t.Fatal(err)
	}
	if got.SchemaVersion != RecordSchemaVersion || got.ManagedBy != managedMarker || got.Port != 5300 || !got.UpdatedAt.Equal(now) {
		t.Fatalf("unexpected record: %+v", got)
	}
	if _, err := os.Stat(filepath.Join(dir, recordFileName)); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, recordFileName), []byte("broken: ["), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Read(); err != ErrCorrupt {
		t.Fatalf("Read error = %v, want ErrCorrupt", err)
	}
}

func TestStoreWriteRejectsNonManagedComposeDirectory(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "compose.yaml"), []byte("services: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := (Store{Dir: dir}).Write(context.Background(), Record{CoreVersion: "v4.10.10"})
	if !errors.Is(err, ErrNotManaged) {
		t.Fatalf("Write error = %v, want ErrNotManaged", err)
	}
}

func TestStatusNotInstalledDoesNotCreateFiles(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "missing")
	diagnostics := Diagnostics{Dir: dir, Store: Store{Dir: dir}, Runner: &fakeRunner{}}
	got, err := diagnostics.Status(context.Background())
	if err != nil || got.Status != "not_installed" {
		t.Fatalf("status = %+v, err = %v", got, err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("status created deployment directory: %v", err)
	}
}

func TestStatusDistinguishesPartialAndHealthyServices(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{}`))}, nil
	})}
	dir := t.TempDir()
	store := Store{Dir: dir}
	composeFile := filepath.Join(dir, "compose.yaml")
	if err := store.Write(context.Background(), Record{Endpoint: "http://127.0.0.1:5300", ComposeProject: "langbot", ComposeFile: composeFile}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(composeFile, []byte("services: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runner := &fakeRunner{docker: map[string]CommandResult{"info --format {{.ServerVersion}}": {Stdout: "27"}}, compose: CommandResult{Stdout: `[{"Name":"langbot","Service":"langbot","State":"running","Health":"healthy"},{"Name":"runtime","Service":"langbot_plugin_runtime","State":"exited"}]`}}
	diagnostics := Diagnostics{Dir: dir, Store: store, Runner: runner, HTTP: client}
	got, err := diagnostics.Status(context.Background())
	if err != nil || got.Status != "degraded" {
		t.Fatalf("status = %+v, err = %v", got, err)
	}
	runner.compose.Stdout = `[{"Name":"langbot","Service":"langbot","State":"running","Health":"healthy"},{"Name":"runtime","Service":"langbot_plugin_runtime","State":"running","Health":"healthy"}]`
	got, err = diagnostics.Status(context.Background())
	if err != nil || got.Status != "running" {
		t.Fatalf("healthy status = %+v, err = %v", got, err)
	}
}

func TestStatusUsesRequiredServicesOnlyAndRejectsUnhealthy(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{}`))}, nil
	})}
	dir := t.TempDir()
	store := Store{Dir: dir}
	composeFile := filepath.Join(dir, "compose.yaml")
	if err := store.Write(context.Background(), Record{Profile: "basic", ComposeProject: "langbot", ComposeFile: composeFile}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(composeFile, []byte("services: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runner := &fakeRunner{
		docker:  map[string]CommandResult{"info --format {{.ServerVersion}}": {Stdout: "27"}},
		compose: CommandResult{Stdout: `[{"Service":"langbot","State":"running","Health":"healthy"},{"Service":"langbot_plugin_runtime","State":"running","Health":"healthy"},{"Service":"langbot_box","State":"exited"}]`},
	}
	diagnostics := Diagnostics{Dir: dir, Store: store, Runner: runner, HTTP: client}
	got, err := diagnostics.Status(context.Background())
	if err != nil || got.Status != "running" {
		t.Fatalf("optional service changed basic status: status=%+v err=%v", got, err)
	}
	runner.compose.Stdout = `[{"Service":"langbot","State":"running","Health":"unhealthy"},{"Service":"langbot_plugin_runtime","State":"running","Health":"healthy"}]`
	got, err = diagnostics.Status(context.Background())
	if err != nil || got.Status != "degraded" {
		t.Fatalf("unhealthy service was not degraded: status=%+v err=%v", got, err)
	}
}

func TestStatusSeparatesMissingComposeAndDockerCLI(t *testing.T) {
	dir := t.TempDir()
	composeFile := filepath.Join(dir, "compose.yaml")
	store := Store{Dir: dir}
	if err := store.Write(context.Background(), Record{ComposeProject: "langbot", ComposeFile: composeFile}); err != nil {
		t.Fatal(err)
	}
	diagnostics := Diagnostics{Dir: dir, Store: store, Runner: &fakeRunner{err: exec.ErrNotFound}}
	got, err := diagnostics.Status(context.Background())
	var problem Problem
	if !errors.As(err, &problem) || problem.Kind != "precondition" || got.Evidence["compose_asset"] != "missing_or_invalid" {
		t.Fatalf("missing Compose classification mismatch: status=%+v err=%v", got, err)
	}
	if err := os.WriteFile(composeFile, []byte("services: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err = diagnostics.Status(context.Background())
	if !errors.As(err, &problem) || problem.Kind != "precondition" || problem.Msg != "找不到可用的 Docker CLI" {
		t.Fatalf("missing Docker CLI classification mismatch: status=%+v err=%v", got, err)
	}
}

func TestDoctorOnCleanNestedDirIsReadOnly(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "missing", "nested", "deployment")
	runner := &fakeRunner{docker: map[string]CommandResult{
		"--version":                        {Stdout: "Docker version 27"},
		"info --format {{.ServerVersion}}": {Stdout: "27"},
		"compose version --short":          {Stdout: "v2.29.0"},
	}}
	checks, err := (Diagnostics{Dir: dir, Store: Store{Dir: dir}, Runner: runner}).Doctor(context.Background())
	if err != nil {
		t.Fatalf("clean nested directory failed doctor: %v, checks=%+v", err, checks)
	}
	if _, statErr := os.Stat(filepath.Join(base, "missing")); !os.IsNotExist(statErr) {
		t.Fatalf("doctor created deployment path: %v", statErr)
	}
}

func TestDoctorDoesNotClaimPermissionFailureWhenDaemonIsDown(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "missing")
	runner := &fakeRunner{
		docker: map[string]CommandResult{
			"--version":               {Stdout: "Docker version 27"},
			"compose version --short": {Stdout: "v2.29.0"},
		},
		err: errors.New("daemon unavailable"),
	}
	checks, _ := (Diagnostics{Dir: dir, Store: Store{Dir: dir}, Runner: runner}).Doctor(context.Background())
	for _, check := range checks {
		if check.Name == "docker_cli" && check.Status != "ok" {
			t.Fatalf("Docker CLI was conflated with daemon state: %+v", check)
		}
		if check.Name == "permission" && (check.Status != "warn" || !strings.Contains(check.Reason, "无法验证")) {
			t.Fatalf("daemon outage was reported as permission denial: %+v", check)
		}
	}
}

func TestDoctorDoesNotBindOrQueryUnmanagedComposeProject(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "docker-compose.yaml"), []byte("services: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runner := &fakeRunner{docker: map[string]CommandResult{
		"--version":                        {Stdout: "Docker version 27"},
		"info --format {{.ServerVersion}}": {Stdout: "27"},
		"compose version --short":          {Stdout: "v2.29.0"},
	}}
	checks, err := (Diagnostics{Dir: dir, Store: Store{Dir: dir}, Runner: runner}).Doctor(context.Background())
	if err != nil {
		t.Fatalf("unmanaged Compose should remain a warning: %v, checks=%+v", err, checks)
	}
	for _, call := range runner.calls {
		if len(call) > 2 && call[1] == "compose" && call[2] != "version" {
			t.Fatalf("doctor queried an unbound Compose project: %#v", call)
		}
	}
}

func TestDoctorDoesNotReportManagedPortAsExternalConflict(t *testing.T) {
	dir := t.TempDir()
	record := Record{Endpoint: "http://managed.local", Port: 1, Profile: "basic", ComposeFile: filepath.Join(dir, "compose.yaml")}
	store := Store{Dir: dir}
	if err := store.Write(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(record.ComposeFile, []byte("services: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runner := &fakeRunner{docker: map[string]CommandResult{
		"--version":                        {Stdout: "Docker version 27"},
		"info --format {{.ServerVersion}}": {Stdout: "27"},
		"compose version --short":          {Stdout: "v2.29.0"},
	}, compose: CommandResult{Stdout: `[{"Name":"langbot","Service":"langbot","State":"running"},{"Name":"runtime","Service":"langbot_plugin_runtime","State":"running"}]`}}
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{}`))}, nil
	})}
	checks, err := (Diagnostics{Dir: dir, Store: store, Runner: runner, HTTP: client}).Doctor(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, check := range checks {
		if check.Name == "default_port" && (check.Status != "ok" || check.Reason != "默认端口由受管部署使用") {
			t.Fatalf("managed port was reported as conflict: %+v", check)
		}
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) { return f(request) }

func TestParseContainersAcceptsComposeJSONLines(t *testing.T) {
	got, err := ParseContainers(`{"Name":"a","Service":"langbot","State":"running"}
{"Name":"b","Service":"runtime","State":"created"}`)
	if err != nil || len(got) != 2 || got[1].State != "created" {
		t.Fatalf("containers = %+v, err = %v", got, err)
	}
}

func TestDockerRunnerUsesCurrentLocalContext(t *testing.T) {
	t.Setenv("docker_host", "tcp://remote.example:2375")
	var calls [][]string
	runner := DockerRunner{Exec: func(_ context.Context, command string, args ...string) (CommandResult, error) {
		calls = append(calls, append([]string{command}, args...))
		joined := strings.Join(args, " ")
		switch joined {
		case "context show":
			return CommandResult{Stdout: "desktop-linux\n"}, nil
		case "context inspect desktop-linux --format {{.Endpoints.docker.Host}}":
			return CommandResult{Stdout: "unix:///tmp/docker.sock\n"}, nil
		}
		return CommandResult{}, nil
	}}
	if _, err := runner.Docker(context.Background(), "info"); err != nil {
		t.Fatal(err)
	}
	if _, err := runner.Compose(context.Background(), Record{ComposeProject: "langbot"}, "ps"); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 6 || len(calls[2]) < 3 || calls[2][1] != "--context" || calls[2][2] != "desktop-linux" || len(calls[5]) < 3 || calls[5][1] != "--context" || calls[5][2] != "desktop-linux" {
		t.Fatalf("runner commands did not use current local context: %#v", calls)
	}
	for _, key := range []string{"DOCKER_HOST=", "DOCKER_CONTEXT=", "DOCKER_TLS_VERIFY=", "DOCKER_CERT_PATH="} {
		for _, env := range localDockerEnvironment() {
			if strings.HasPrefix(strings.ToUpper(env), key) {
				t.Fatalf("remote Docker environment was not filtered: %s", env)
			}
		}
	}
}

func TestDockerRunnerRejectsRemoteContext(t *testing.T) {
	runner := DockerRunner{Exec: func(_ context.Context, _ string, args ...string) (CommandResult, error) {
		switch strings.Join(args, " ") {
		case "context show":
			return CommandResult{Stdout: "production\n"}, nil
		case "context inspect production --format {{.Endpoints.docker.Host}}":
			return CommandResult{Stdout: "ssh://docker.example\n"}, nil
		default:
			t.Fatalf("不应该执行远程 Docker 命令: %v", args)
			return CommandResult{}, nil
		}
	}}
	result, err := runner.Docker(context.Background(), "info")
	if err == nil || !strings.Contains(result.Stderr, "只允许本机 daemon") {
		t.Fatalf("result = %+v, err = %v", result, err)
	}
}

func TestDockerRunnerVersionDoesNotResolveContext(t *testing.T) {
	var calls [][]string
	runner := DockerRunner{Exec: func(_ context.Context, command string, args ...string) (CommandResult, error) {
		calls = append(calls, append([]string{command}, args...))
		return CommandResult{Stdout: "Docker version 29.0.0"}, nil
	}}
	if _, err := runner.Docker(context.Background(), "--version"); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 1 || strings.Join(calls[0], " ") != "docker --version" {
		t.Fatalf("calls = %#v", calls)
	}
}

func TestDockerRunnerRejectsEmptyCommand(t *testing.T) {
	runner := DockerRunner{Exec: func(context.Context, string, ...string) (CommandResult, error) {
		t.Fatal("空命令不应该执行 Docker")
		return CommandResult{}, nil
	}}
	if _, err := runner.Docker(context.Background()); err == nil {
		t.Fatal("Docker() 应该拒绝空命令")
	}
	if _, err := runner.Compose(context.Background(), Record{}); err == nil {
		t.Fatal("Compose() 应该拒绝空命令")
	}
}

func TestComposeV2Compatibility(t *testing.T) {
	for value, want := range map[string]bool{
		"v2.29.0": true,
		"2.29.0":  true,
		"5.5.1":   true,
		"1.29.2":  false,
		"unknown": false,
	} {
		if got := isComposeV2(value); got != want {
			t.Errorf("isComposeV2(%q) = %v, want %v", value, got, want)
		}
	}
}
