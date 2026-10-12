package output

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

func statusSummaryFixture() map[string]any {
	return map[string]any{"ok": true, "data": map[string]any{
		"name": "production", "type": "self_hosted", "discovery": "complete",
		"connection":   map[string]any{"endpoint": "https://example.test", "reachable": true, "authentication": "verified"},
		"service":      map[string]any{"version": "v4.10.11", "edition": "community"},
		"identity":     map[string]any{"status": "verified", "instance_uuid": "full-instance-id", "workspace_uuid": "full-workspace-id"},
		"capabilities": map[string]any{"status": "verified", "operations": map[string]any{"sandbox.status": "unsupported"}},
		"runtime":      map[string]any{"management": "local_managed", "driver": "docker_compose", "status": "running", "binding": "verified", "dir": "/private/deployment"},
	}}
}

func TestStatusSummaryIsCompactAndDoesNotClaimAllCapabilitiesSupported(t *testing.T) {
	var out bytes.Buffer
	if err := RenderCommand(&out, statusSummaryFixture(), FormatDefault, "status"); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"当前环境: production（自部署）", "连接: 已连接", "认证: 已验证", "Core: v4.10.11", "部署: Docker Compose · 运行中", "能力清单: 已获取（支持情况见 lbctl capabilities）", "在当前命令后加 -o json 或 -o yaml"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("summary missing %q: %s", want, out.String())
		}
	}
	for _, unwanted := range []string{"full-instance-id", "full-workspace-id", "/private/deployment", "Capabilities: Verified", "Directory:"} {
		if strings.Contains(out.String(), unwanted) {
			t.Fatalf("summary exposed verbose or misleading detail %q: %s", unwanted, out.String())
		}
	}
	if len(strings.Split(strings.TrimSpace(out.String()), "\n")) > 8 {
		t.Fatalf("summary too long: %s", out.String())
	}
}

func TestStatusMachineFormatsKeepFullIdentityPathsAndCapabilityStates(t *testing.T) {
	for _, format := range []string{FormatJSON, FormatYAML} {
		t.Run(format, func(t *testing.T) {
			var out bytes.Buffer
			if err := RenderCommand(&out, statusSummaryFixture(), format, "status"); err != nil {
				t.Fatal(err)
			}
			var envelope map[string]any
			var err error
			if format == FormatJSON {
				err = json.Unmarshal(out.Bytes(), &envelope)
			} else {
				err = yaml.Unmarshal(out.Bytes(), &envelope)
			}
			if err != nil {
				t.Fatal(err)
			}
			data := envelope["data"].(map[string]any)
			identity := data["identity"].(map[string]any)
			if identity["instance_uuid"] != "full-instance-id" || identity["workspace_uuid"] != "full-workspace-id" || data["runtime"].(map[string]any)["dir"] != "/private/deployment" || data["capabilities"].(map[string]any)["operations"].(map[string]any)["sandbox.status"] != "unsupported" {
				t.Fatalf("machine contract changed: %s", out.String())
			}
			if strings.Contains(out.String(), "完整信息：") {
				t.Fatal("human hint leaked into machine output")
			}
		})
	}
}

func TestStatusSummaryRetainsIdentityMismatchAndRecoveryWarnings(t *testing.T) {
	value := statusSummaryFixture()
	data := value["data"].(map[string]any)
	data["identity"].(map[string]any)["status"] = "mismatch"
	data["runtime"].(map[string]any)["details"] = map[string]any{"upgrade": map[string]any{"phase": "recovery_required", "failure_step": "backup"}}
	var out bytes.Buffer
	if err := RenderCommand(&out, value, FormatDefault, "status"); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Workspace 身份: 不一致", "实际 Workspace: full-workspace-id", "升级: 需要恢复 (backup)"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("warning missing %q: %s", want, out.String())
		}
	}
}

func TestCapabilityPreconditionHintDoesNotChangeMachineError(t *testing.T) {
	value := map[string]any{"ok": false, "error": map[string]any{"type": "precondition", "message": "服务端未明确支持该操作"}}
	var human, machine bytes.Buffer
	if err := RenderCommand(&human, value, FormatDefault, "sandbox.status"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(human.String(), "对同一目标运行 lbctl capabilities") {
		t.Fatal("capability hint missing")
	}
	if err := RenderCommand(&machine, value, FormatJSON, "sandbox.status"); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(machine.String(), "建议：") || !strings.Contains(machine.String(), `"type": "precondition"`) {
		t.Fatalf("machine error changed: %s", machine.String())
	}
}
