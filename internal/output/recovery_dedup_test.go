package output

import (
	"bytes"
	"strings"
	"testing"
)

func TestRecoveryCommandsAppearOnce(t *testing.T) {
	for _, config := range []bool{false, true} {
		data := map[string]any{"action": "install", "step": "install_package"}
		steps := []any{}
		for _, action := range []string{"retry", "rollback"} {
			command := "install"
			if action == "rollback" {
				command = "uninstall"
			}
			args := []any{command, "--dir", "/example"}
			data[action+"_argv"] = args
			data[action+"_command"] = "lbctl " + command + " --dir /example"
			prefix := []any{"lbctl"}
			if config {
				prefix = append(prefix, "--config", "/custom.yaml")
			}
			steps = append(steps, map[string]any{"argv": append(prefix, args...), "description": action})
		}
		envelope := map[string]any{"ok": false, "data": data, "meta": map[string]any{"next_steps": steps}}
		var out bytes.Buffer
		if err := RenderCommand(&out, envelope, FormatDefault, "install"); err != nil {
			t.Fatal(err)
		}
		if strings.Count(out.String(), "lbctl ") != 2 || config && strings.Count(out.String(), "--config /custom.yaml") != 2 {
			t.Fatal(out.String())
		}
		for _, format := range []string{FormatJSON, FormatYAML} {
			out.Reset()
			if err := RenderCommand(&out, envelope, format, "install"); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(out.String(), "retry_command") || !strings.Contains(out.String(), "rollback_command") {
				t.Fatal(out.String())
			}
		}
	}
}
