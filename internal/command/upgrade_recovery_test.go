package command

import (
	"bytes"
	"context"
	"path/filepath"
	"testing"

	"github.com/langbot-app/langbot-cli/internal/app"
	"github.com/langbot-app/langbot-cli/internal/result"
)

func TestUpgradeRecoveryTargetsFailedStep(t *testing.T) {
	deps := normalizeDependencies(Dependencies{LookupEnv: func(string) (string, bool) { return "", false }, DefaultConfigPath: func() (string, error) { return filepath.Join(t.TempDir(), "config.yaml"), nil }})
	for _, test := range []struct{ step, command string }{
		{"resolve_version", "upgrade"}, {"validate_target", "upgrade"}, {"backup_preflight", "status"},
		{"backup", "status"}, {"image_pull", "doctor"}, {"stop_readback", "status"},
		{"compose_switch", "status"}, {"environment_switch", "status"}, {"http_version", "logs"},
		{"service_health", "logs"}, {"compose_up", "logs"}, {"write_record", "status"},
	} {
		t.Run(test.step, func(t *testing.T) {
			value := enrichResult(deps, &globalFlags{commandPath: "upgrade"}, app.Result{Data: map[string]any{"step": test.step, "recovery_endpoint": "http://127.0.0.1:15300"}}, result.New("precondition", "failed"))
			steps := value.Meta["next_steps"].([]map[string]any)
			argv := steps[0]["argv"].([]string)
			commandIndex := 1
			if test.command != "upgrade" {
				commandIndex = 3
			}
			if len(steps) != 1 || argv[commandIndex] != test.command {
				t.Fatal(steps)
			}
			if test.command != "upgrade" {
				if len(argv) != 4 || argv[1] != "--endpoint" || argv[2] != "http://127.0.0.1:15300" {
					t.Fatal(argv)
				}
			}
			var output bytes.Buffer
			if code := executeChinese(context.Background(), append(argv[1:], "--help"), Dependencies{Out: &output, Err: &output}); code != 0 {
				t.Fatalf("invalid recovery command: %v: %s", argv, output.String())
			}
		})
	}
}

func TestUpgradeAndDoctorSelectorsParse(t *testing.T) {
	for _, args := range [][]string{{"upgrade", "--name", "named", "--help"}, {"upgrade", "--dir", "custom directory", "--help"}, {"doctor", "--region", "cn", "--help"}} {
		var output bytes.Buffer
		if code := executeChinese(t.Context(), args, Dependencies{Out: &output, Err: &output}); code != 0 {
			t.Fatalf("%v: %s", args, output.String())
		}
	}
}
