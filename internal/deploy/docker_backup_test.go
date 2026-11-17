package deploy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type backupRunner struct {
	call func(...string) (CommandResult, error)
}

func (runner backupRunner) Docker(_ context.Context, args ...string) (CommandResult, error) {
	return runner.call(args...)
}
func (runner backupRunner) Compose(context.Context, Record, ...string) (CommandResult, error) {
	return CommandResult{}, errors.New("unexpected Compose command")
}

func TestDockerBackupUsesContainerAndVerifiesCopy(t *testing.T) {
	record, dir := upgradeFixture(t)
	config, err := os.ReadFile(filepath.Join(record.DataDir, "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var restored bool
	runner := backupRunner{call: func(args ...string) (CommandResult, error) {
		joined := strings.Join(args, " ")
		if !strings.Contains(joined, "--user 0:0") || !strings.Contains(joined, "--network none") || !strings.Contains(joined, "rockchin/langbot:v4.10.9") {
			t.Fatal(joined)
		}
		if args[len(args)-1] == inspectDockerData {
			if !strings.Contains(joined, "dst=/data,readonly") {
				t.Fatal(joined)
			}
			encoded, _ := json.Marshal(map[string]any{"config": string(config), "size": 100, "free": 10 << 30})
			return CommandResult{Stdout: string(encoded)}, nil
		}
		if args[len(args)-1] == "restore" {
			if !strings.Contains(joined, "dst=/backup,readonly") {
				t.Fatal(joined)
			}
			restored = true
			return CommandResult{}, nil
		}
		for _, arg := range args {
			if strings.HasSuffix(arg, ",dst=/backup") {
				backup := strings.TrimSuffix(strings.TrimPrefix(arg, "type=bind,src="), ",dst=/backup")
				return CommandResult{}, os.CopyFS(filepath.Join(backup, "data"), os.DirFS(record.DataDir))
			}
		}
		t.Fatal("missing backup mount")
		return CommandResult{}, nil
	}}
	if size, _, err := InspectDockerUpgradeData(t.Context(), runner, record); err != nil || size != 100 {
		t.Fatalf("%d: %v", size, err)
	}
	backup, err := CreateUpgradeBackup(t.Context(), record, UpgradeState{SourceVersion: record.CoreVersion}, dir, runner)
	if err != nil {
		t.Fatal(err)
	}
	if err := RestoreDockerUpgradeBackup(t.Context(), runner, record, backup); err != nil || !restored {
		t.Fatalf("restore: %v", err)
	}
	if err := os.WriteFile(filepath.Join(backup, "data", "langbot.db"), []byte("damaged"), 0o600); err != nil {
		t.Fatal(err)
	}
	restored = false
	if err := RestoreDockerUpgradeBackup(t.Context(), runner, record, backup); err == nil || restored {
		t.Fatal("corrupt backup reached container")
	}
}

func TestDockerBackupNonRootRoundTrip(t *testing.T) {
	image := os.Getenv("LBCTL_DOCKER_TEST_IMAGE")
	if image == "" {
		t.Skip("set LBCTL_DOCKER_TEST_IMAGE for Docker integration")
	}
	if os.Geteuid() == 0 {
		t.Fatal("Docker backup test must run as non-root")
	}
	record, dir := upgradeFixture(t)
	if err := os.WriteFile(record.ComposeFile, []byte("services:\n  langbot:\n    image: "+image+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runner := DockerRunner{}
	run := func(script string) string {
		t.Helper()
		output, err := runner.Docker(context.Background(), "run", "--rm", "--user", "0:0", "--mount", "type=bind,src="+record.DataDir+",dst=/data", "--entrypoint", "python", image, "-c", script)
		if err != nil {
			t.Fatalf("%v: %s", err, output.Stderr)
		}
		return output.Stdout
	}
	t.Cleanup(func() {
		run(fmt.Sprintf("import os\nfor root, dirs, files in os.walk('/data'):\n    for path in [root] + [os.path.join(root,name) for name in files]:\n        os.chown(path, %d, %d)", os.Getuid(), os.Getgid()))
	})
	run("import os; os.makedirs('/data/root-only', exist_ok=True); open('/data/root-only/marker','w').write('original'); os.chmod('/data/root-only',0o700)")
	if _, err := os.ReadFile(filepath.Join(record.DataDir, "root-only", "marker")); !os.IsPermission(err) {
		t.Fatalf("not a root-only fixture: %v", err)
	}
	if _, _, err := InspectDockerUpgradeData(t.Context(), runner, record); err != nil {
		t.Fatal(err)
	}
	backup, err := CreateUpgradeBackup(t.Context(), record, UpgradeState{}, dir, runner)
	if err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(filepath.Join(backup, "data", "root-only", "marker")); err != nil || string(data) != "original" {
		t.Fatalf("%s: %v", data, err)
	}
	run("open('/data/root-only/marker','w').write('changed'); open('/data/extra','w').write('extra')")
	if err := RestoreDockerUpgradeBackup(t.Context(), runner, record, backup); err != nil {
		t.Fatal(err)
	}
	if got := run("import os; assert not os.path.exists('/data/extra'); print(open('/data/root-only/marker').read())"); strings.TrimSpace(got) != "original" {
		t.Fatal(got)
	}
}
