package command_test

import (
	"encoding/json"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

func TestHumanHelpHasConsistentLabels(t *testing.T) {
	for _, args := range [][]string{{"--help"}, {"bot", "update", "--help"}, {"help", "bot", "update"}, {"completion", "--help"}} {
		code, out, diagnostic := executeCommand(t, nil, args...)
		if code != 0 || diagnostic != "" || !strings.Contains(out, "用法：") || !strings.Contains(out, "选项：") {
			t.Fatalf("help failed: %v code=%d stderr=%q output=%s", args, code, diagnostic, out)
		}
		for _, unexpected := range []string{"Usage:", "Available Commands:", "Global Flags:", "Help about any command", "help for lbctl"} {
			if strings.Contains(out, unexpected) {
				t.Fatalf("untranslated heading %q: %s", unexpected, out)
			}
		}
	}
}

func TestHelpGroupsExamplesAndApplicableFlags(t *testing.T) {
	_, root, _ := executeCommand(t, nil, "--help")
	for _, title := range []string{"连接与诊断：", "资源与内容：", "本机部署：", "工具与自动化："} {
		if !strings.Contains(root, title) {
			t.Fatalf("missing group %s: %s", title, root)
		}
	}
	_, help, _ := executeCommand(t, nil, "bot", "update", "--help")
	for _, part := range []string{"（必填）", "request.yaml", "\"name\":\"my-bot\"", "预检通过不代表"} {
		if !strings.Contains(help, part) {
			t.Fatalf("missing guidance %s: %s", part, help)
		}
	}
	_, local, _ := executeCommand(t, nil, "context", "use", "--help")
	for _, flag := range []string{"--api-key-stdin", "--endpoint", "--context", "--timeout"} {
		if strings.Contains(local, flag) {
			t.Fatalf("inapplicable flag %s: %s", flag, local)
		}
	}
}

func TestInputErrorsAreSpecificAndNeverEchoSuppliedValues(t *testing.T) {
	for _, test := range []struct {
		name string
		args []string
		want string
	}{
		{"missing id", []string{"bot", "update"}, "缺少位置参数 <id>"},
		{"missing required flag", []string{"bot", "create"}, "缺少必需选项 --file"},
		{"missing flag value", []string{"bot", "create", "--file"}, "--file 需要提供值"},
		{"invalid numeric value", []string{"monitoring", "messages", "--limit", "never-echo-secret"}, "--limit 的值无效"},
		{"extra argument", []string{"bot", "update", "id", "never-echo-secret"}, "位置参数数量不正确"},
		{"unknown help", []string{"help", "never-echo-secret"}, "未知帮助主题"},
		{"unknown flag", []string{"bot", "create", "--never-echo-secret"}, "未知选项或选项格式错误"},
	} {
		for _, format := range []string{"default", "json", "yaml"} {
			t.Run(test.name+"/"+format, func(t *testing.T) {
				args := append([]string{}, test.args...)
				if format != "default" {
					args = append([]string{"-o", format}, args...)
				}
				code, out, diagnostic := executeCommand(t, nil, args...)
				if code != 2 || diagnostic != "" || strings.Contains(out, "never-echo-secret") {
					t.Fatalf("unsafe or unclear error: code=%d stderr=%q output=%s", code, diagnostic, out)
				}
				if format == "default" {
					if !strings.Contains(out, "--help") || !strings.Contains(out, test.want) {
						t.Fatal("error omitted help hint")
					}
					return
				}
				var envelope map[string]any
				var err error
				if format == "json" {
					err = json.Unmarshal([]byte(out), &envelope)
				} else {
					err = yaml.Unmarshal([]byte(out), &envelope)
				}
				if err != nil || envelope["ok"] != false || envelope["error"].(map[string]any)["type"] != "input" || !strings.Contains(envelope["error"].(map[string]any)["message"].(string), test.want) {
					t.Fatalf("machine error contract changed: err=%v output=%s", err, out)
				}
			})
		}
	}
}
