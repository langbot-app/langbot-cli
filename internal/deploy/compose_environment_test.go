package deploy

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestComposeWorkingPath(t *testing.T) {
	for _, test := range []struct {
		path, platform, want string
	}{
		{`D:\LB-DEV\LangBot-selfhost\docker`, "windows", "/d/LB-DEV/LangBot-selfhost/docker"},
		{`C:/Users/Test User/deployment`, "windows", "/c/Users/Test User/deployment"},
		{"/srv/langbot", "linux", "/srv/langbot"},
		{"/Users/test/langbot", "darwin", "/Users/test/langbot"},
	} {
		if got := composeWorkingPath(test.path, test.platform); got != test.want {
			t.Errorf("composeWorkingPath(%q, %q) = %q, want %q", test.path, test.platform, got, test.want)
		}
	}
}

type composeProcessResult struct {
	Directory string
	PWD       string
	Relative  string
}

func TestComposeCommandRunsInDeploymentDirectory(t *testing.T) {
	t.Setenv("PWD", filepath.Join(t.TempDir(), "unrelated-caller"))
	deploymentDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(deploymentDir, "relative.txt"), []byte("compose-root"), 0o600); err != nil {
		t.Fatal(err)
	}
	record := Record{ComposeFile: filepath.Join(deploymentDir, "compose.yaml"), ComposeProject: "test"}
	command := (DockerRunner{}).composeCommand(context.Background(), "desktop-linux", record, "up", "-d")
	if !strings.Contains(strings.Join(command.Args, "\n"), "--project-directory\n"+deploymentDir) {
		t.Fatal("Compose project directory was not set")
	}
	// Reuse the actual command's environment and directory with a child process.
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	command.Path = executable
	command.Args = []string{executable, "-test.run=^TestComposeEnvironmentChildProcess$"}
	command.Env = append(command.Env, "LBCTL_COMPOSE_ENVIRONMENT_CHILD=1")
	output, err := command.Output()
	if err != nil {
		t.Fatal(err)
	}
	var got composeProcessResult
	if err := json.Unmarshal(output, &got); err != nil {
		t.Fatalf("invalid child output: %s: %v", output, err)
	}
	actualDir, err := filepath.EvalSymlinks(got.Directory)
	if err != nil {
		t.Fatal(err)
	}
	expectedDir, err := filepath.EvalSymlinks(deploymentDir)
	if err != nil {
		t.Fatal(err)
	}
	if actualDir != expectedDir || got.PWD != composeWorkingPath(deploymentDir, runtime.GOOS) || got.Relative != "compose-root" {
		t.Fatalf("child process did not use the deployment environment: %+v", got)
	}
}

func TestComposeEnvironmentChildProcess(t *testing.T) {
	if os.Getenv("LBCTL_COMPOSE_ENVIRONMENT_CHILD") != "1" {
		return
	}
	directory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile("relative.txt")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.NewEncoder(os.Stdout).Encode(composeProcessResult{directory, os.Getenv("PWD"), string(data)}); err != nil {
		t.Fatal(err)
	}
	os.Exit(0)
}
