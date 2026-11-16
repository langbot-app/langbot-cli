package command

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/langbot-app/langbot-cli/internal/app"
	"github.com/langbot-app/langbot-cli/internal/result"
)

func TestNativePortConflictRecovery(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	root := t.TempDir()
	t.Setenv("LANGBOT_HOME", root)
	t.Setenv("LBCTL_NO_UPDATE_CHECK", "1")
	port := strconv.Itoa(listener.Addr().(*net.TCPAddr).Port)
	args := []string{"install", "--driver", "native", "--yes", "--region", "global", "--version", "v4.11.7", "--port", port}
	for _, format := range []string{"", "json"} {
		var output bytes.Buffer
		commandArgs := append([]string{}, args...)
		if format != "" {
			commandArgs = append(commandArgs, "--output", format)
		}
		code := Execute(context.Background(), commandArgs, Dependencies{Out: &output, Err: io.Discard, LocalDir: filepath.Join(root, "instance")})
		if code != 6 || strings.Contains(output.String(), "context show") || !strings.Contains(output.String(), "释放冲突端口") {
			t.Fatalf("%d: %s", code, output.String())
		}
		if format == "json" {
			var envelope struct {
				Data map[string]any `json:"data"`
				Meta map[string]any `json:"meta"`
			}
			if err := json.Unmarshal(output.Bytes(), &envelope); err != nil {
				t.Fatal(err)
			}
			if envelope.Data["step"] != "check_ports" || envelope.Meta["next_steps"] == nil || envelope.Data["rollback_argv"] != nil {
				t.Fatal(output.String())
			}
			for _, step := range envelope.Meta["next_steps"].([]any) {
				if strings.Contains(step.(map[string]any)["description"].(string), "Workspace") {
					t.Fatal(output.String())
				}
			}
		}
	}
}

func TestInstallServiceFailureRecoveryCommands(t *testing.T) {
	deps := normalizeDependencies(Dependencies{LookupEnv: func(string) (string, bool) { return "", false }, DefaultConfigPath: func() (string, error) { return filepath.Join(t.TempDir(), "config.yaml"), nil }})
	value := enrichResult(deps, &globalFlags{commandPath: "install"}, app.Result{Data: map[string]any{
		"step": "install_service", "retry_argv": []string{"install", "--dir", "中文 dir"}, "rollback_argv": []string{"uninstall", "--endpoint", "http://127.0.0.1:5300", "--yes"},
	}}, result.New("server", "service registration failed"))
	steps := value.Meta["next_steps"].([]map[string]any)
	if len(steps) != 2 || steps[0]["argv"].([]string)[1] != "install" || steps[1]["argv"].([]string)[1] != "uninstall" {
		t.Fatal(steps)
	}
}
