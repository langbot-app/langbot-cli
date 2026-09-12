package output

import (
	"bytes"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

func TestRenderPreservesNumbersAndRedactsSecrets(t *testing.T) {
	var out bytes.Buffer
	value := map[string]any{
		"api_key":        "secret-value",
		"api_key_secret": "another-secret",
		"api_key_id":     "safe-key-id",
		"token_count":    12,
		"error": map[string]any{
			"http_status": int64(403),
			"server_code": int64(9223372036854770000),
		},
	}
	if err := Render(&out, value, FormatJSON); err != nil {
		t.Fatal(err)
	}
	jsonOutput := out.String()
	if strings.Contains(jsonOutput, "secret-value") || strings.Contains(jsonOutput, "another-secret") || !strings.Contains(jsonOutput, "safe-key-id") || !strings.Contains(jsonOutput, `"token_count": 12`) || !strings.Contains(jsonOutput, `"http_status": 403`) {
		t.Fatalf("unexpected JSON output: %s", jsonOutput)
	}
	out.Reset()
	if err := Render(&out, value, FormatYAML); err != nil {
		t.Fatal(err)
	}
	yamlOutput := out.String()
	if strings.Contains(yamlOutput, "secret-value") || strings.Contains(yamlOutput, "another-secret") || !strings.Contains(yamlOutput, "safe-key-id") || !strings.Contains(yamlOutput, "server_code: 9223372036854770000") {
		t.Fatalf("unexpected YAML output: %s", yamlOutput)
	}
}

func TestRenderPreservesOnlyConfirmedSecretPlaceholders(t *testing.T) {
	var out bytes.Buffer
	value := map[string]any{
		"authorization": "***",
		"api_key":       map[string]any{"primary": "***", "fallback": ""},
		"password":      "real-secret",
	}
	if err := Render(&out, value, FormatJSON); err != nil {
		t.Fatal(err)
	}
	result := out.String()
	if !strings.Contains(result, `"authorization": "***"`) || !strings.Contains(result, `"primary": "***"`) {
		t.Fatalf("masked placeholders were not preserved: %s", result)
	}
	if strings.Contains(result, "real-secret") || !strings.Contains(result, `"password": "[REDACTED]"`) {
		t.Fatalf("real secret was not redacted: %s", result)
	}
}

func TestRenderDefaultVersionAndDetail(t *testing.T) {
	var out bytes.Buffer
	version := map[string]any{"ok": true, "data": map[string]any{
		"version": "v1.0.0", "server_version": "v4.10.10", "server_edition": "community",
	}}
	if err := RenderCommand(&out, version, FormatDefault, "version"); err != nil {
		t.Fatal(err)
	}
	if got := out.String(); !strings.Contains(got, "CLI: v1.0.0") || !strings.Contains(got, "Server Version: v4.10.10") || !strings.Contains(got, "Edition: community") {
		t.Fatalf("unexpected version output: %q", got)
	}

	out.Reset()
	detail := map[string]any{"ok": true, "data": map[string]any{
		"skill": map[string]any{"name": "demo", "instructions": "line 1\nline 2"},
	}}
	if err := RenderCommand(&out, detail, FormatDefault, "skill.get"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Skill:") || !strings.Contains(out.String(), "line 1") || !strings.Contains(out.String(), "line 2") {
		t.Fatalf("detail output lost readable content: %q", out.String())
	}
}

func TestNormalizeFormatRejectsRemovedTableAndEmptyValue(t *testing.T) {
	for _, format := range []string{"table", ""} {
		if _, err := NormalizeFormat(format); err == nil {
			t.Fatalf("NormalizeFormat(%q) unexpectedly succeeded", format)
		}
	}
}

func TestRenderDefaultActionKeepsExecutionFacts(t *testing.T) {
	var out bytes.Buffer
	value := map[string]any{"ok": false, "data": map[string]any{
		"operation": "skill.install.upload", "skill_name": "demo", "file": "skill.zip",
		"task_id": "42", "step": "install_unknown", "submitted": "unknown", "verified": false,
		"result": map[string]any{"message": "partial"},
	}, "error": map[string]any{"type": "result_unknown", "message": "结果无法确认"}}
	if err := RenderCommand(&out, value, FormatDefault, "skill.install.upload"); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"错误：结果无法确认", "skill.install.upload", "skill.zip", "42", "install_unknown", "unknown", "false", "partial"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("default action output omitted %q: %s", want, out.String())
		}
	}
	if strings.Contains(strings.ToLower(out.String()), "progress") || strings.Contains(out.String(), "%") {
		t.Fatalf("default action output invented progress: %s", out.String())
	}
}

func TestRenderDefaultPreservesFileContentChecksAndModelGroups(t *testing.T) {
	var out bytes.Buffer
	file := map[string]any{"ok": true, "data": map[string]any{"path": "README.md", "content": "line 1\nline 2"}}
	if err := RenderCommand(&out, file, FormatDefault, "skill.file.read"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "line 1") || !strings.Contains(out.String(), "line 2") || strings.Contains(out.String(), `line 1\nline 2`) {
		t.Fatalf("file content was not readable: %q", out.String())
	}

	out.Reset()
	checks := map[string]any{"ok": true, "data": map[string]any{"checks": []any{
		map[string]any{"context": "alpha", "reachable": true, "diagnostic_ok": true},
		map[string]any{"context": "beta", "reachable": false, "diagnostic_ok": false, "error": map[string]any{"type": "network", "message": "offline"}},
	}}}
	if err := RenderCommand(&out, checks, FormatDefault, "context.check"); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"alpha", "beta", "network", "offline", "false"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("check output omitted %q: %s", want, out.String())
		}
	}

	out.Reset()
	models := map[string]any{"ok": true, "data": map[string]any{"models": map[string]any{
		"llm":       []any{map[string]any{"uuid": "llm-full-uuid", "name": "chat"}},
		"embedding": []any{}, "rerank": []any{map[string]any{"uuid": "rerank-full-uuid", "name": "rank"}},
	}}}
	if err := RenderCommand(&out, models, FormatDefault, "model.list"); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"llm", "embedding", "rerank", "llm-full-uuid", "rerank-full-uuid"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("model group output omitted %q: %s", want, out.String())
		}
	}
}

func TestMachineJSONAndYAMLRoundTripSameSafeData(t *testing.T) {
	value := map[string]any{"ok": true, "data": map[string]any{
		"uuid": "full-resource-uuid", "name": "line\nname", "enabled": false,
		"api_key": "secret-value", "items": []any{"one", "two"},
	}}
	var jsonOut, yamlOut bytes.Buffer
	if err := Render(&jsonOut, value, FormatJSON); err != nil {
		t.Fatal(err)
	}
	if err := Render(&yamlOut, value, FormatYAML); err != nil {
		t.Fatal(err)
	}
	var fromJSON, fromYAML map[string]any
	if err := json.Unmarshal(jsonOut.Bytes(), &fromJSON); err != nil {
		t.Fatal(err)
	}
	if err := yaml.Unmarshal(yamlOut.Bytes(), &fromYAML); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(fromJSON, fromYAML) {
		t.Fatalf("JSON and YAML safe data differ: json=%#v yaml=%#v", fromJSON, fromYAML)
	}
	if strings.Contains(jsonOut.String(), "secret-value") || strings.Contains(yamlOut.String(), "secret-value") || !strings.Contains(jsonOut.String(), "full-resource-uuid") {
		t.Fatalf("machine output lost redaction or full UUID: json=%s yaml=%s", jsonOut.String(), yamlOut.String())
	}
}

func TestRenderDefaultEscapesControlsAndSecrets(t *testing.T) {
	var out bytes.Buffer
	value := map[string]any{"ok": true, "data": map[string]any{
		"name": "visible\x1b[31m\tvalue", "password": "secret-value",
	}}
	if err := RenderCommand(&out, value, FormatDefault, "plugin.config.get"); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "secret-value") || !strings.Contains(out.String(), "\\u001b") || !strings.Contains(out.String(), "\\t") {
		t.Fatalf("default output leaked secret or controls: %q", out.String())
	}
}

func TestRenderDefaultKeepsOverallErrorSeparateFromRows(t *testing.T) {
	var out bytes.Buffer
	value := map[string]any{
		"ok": false,
		"data": map[string]any{
			"checks": []any{
				map[string]any{"context": "alpha", "reachable": true},
				map[string]any{"context": "beta", "reachable": false},
			},
		},
		"error": map[string]any{"type": "permission", "message": "检查失败"},
		"meta":  map[string]any{"endpoint": "https://example.test"},
	}
	if err := RenderCommand(&out, value, FormatDefault, "context.check"); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(out.String(), "\n")
	for _, line := range lines {
		if strings.Contains(line, "alpha") && strings.Contains(line, "permission") {
			t.Fatalf("overall error was attached to a successful row: %s", line)
		}
	}
	for _, want := range []string{"错误：检查失败", "Checks:", "alpha", "beta"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("default output omitted %q: %s", want, out.String())
		}
	}
	if strings.Contains(out.String(), "Type:") || strings.Contains(out.String(), "permission") {
		t.Fatalf("default output exposed machine error fields: %s", out.String())
	}
}

func TestRenderDefaultErrorKeepsUsefulDiagnostics(t *testing.T) {
	var out bytes.Buffer
	value := map[string]any{
		"ok": false,
		"error": map[string]any{
			"type": "server", "message": "请求失败", "http_status": 503,
			"server_code": "UPSTREAM_BUSY", "request_id": "request-a",
		},
	}
	if err := RenderCommand(&out, value, FormatDefault, "status"); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"错误：请求失败", "HTTP 状态：503", "服务端错误码：UPSTREAM_BUSY", "请求 ID：request-a"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("default error omitted %q: %s", want, out.String())
		}
	}
	if strings.Contains(out.String(), "Type:") || strings.Contains(out.String(), "server\n") {
		t.Fatalf("default error exposed machine type: %s", out.String())
	}
}

func TestRenderDefaultTargetIsConcise(t *testing.T) {
	var out bytes.Buffer
	value := map[string]any{
		"ok":    false,
		"error": map[string]any{"type": "not_found", "message": "服务返回 HTTP 404", "http_status": 404},
		"meta": map[string]any{
			"context": "production", "endpoint": "https://cloud.langbot.app",
			"credential_source": "env:LANGBOT_PRODUCTION_API_KEY",
		},
	}
	if err := RenderCommand(&out, value, FormatDefault, "whoami"); err != nil {
		t.Fatal(err)
	}
	if out.String() != "错误：服务返回 HTTP 404\n目标：production (https://cloud.langbot.app)\n" {
		t.Fatalf("default target/error output is not concise: %q", out.String())
	}
}

func TestRenderDefaultSummarizesSingleContextCheck(t *testing.T) {
	var out bytes.Buffer
	value := map[string]any{
		"ok": true,
		"data": map[string]any{
			"reachable": true, "diagnostic_ok": true, "server_version": "v4.10.10", "server_edition": "cloud",
			"identity": map[string]any{
				"instance_uuid": "instance-a", "workspace_uuid": "workspace-a", "api_key_id": "key-a",
				"permissions": []any{"resource.view", "resource.manage"},
			},
		},
	}
	if err := RenderCommand(&out, value, FormatDefault, "context.check"); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Reachable: true", "Server Version: v4.10.10", "Instance: instance-a", "Workspace: workspace-a", "API Key ID: key-a"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("single check output omitted %q: %s", want, out.String())
		}
	}
	if strings.Contains(out.String(), "resource.manage") {
		t.Fatalf("single check output is not concise: %s", out.String())
	}
}

func TestRenderDefaultSupportsResourceLists(t *testing.T) {
	var out bytes.Buffer
	value := map[string]any{
		"ok": true,
		"data": map[string]any{
			"bots": []any{
				map[string]any{
					"uuid":           "bot-a",
					"name":           "Bot A",
					"adapter_config": map[string]any{"token": "bot-secret"},
				},
			},
		},
	}
	if err := RenderCommand(&out, value, FormatDefault, "bot.list"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Bots:") || !strings.Contains(out.String(), "bot-a") {
		t.Fatalf("resource default output omitted list rows: %s", out.String())
	}
	if strings.Contains(out.String(), "bot-secret") {
		t.Fatalf("resource table exposed sensitive configuration: %s", out.String())
	}
}

func TestRenderDefaultSupportsManagedResourceLists(t *testing.T) {
	commands := map[string]string{
		"tasks": "task.list", "bases": "knowledge-base.list", "files": "knowledge-base.file.list",
		"servers": "mcp-server.list", "plugins": "plugin.list", "skills": "skill.list",
	}
	for _, key := range []string{"tasks", "bases", "files", "servers", "plugins", "skills"} {
		t.Run(key, func(t *testing.T) {
			var out bytes.Buffer
			item := map[string]any{"name": "Resource A"}
			expected := "Resource A"
			switch key {
			case "tasks":
				item, expected = map[string]any{"id": "resource-a", "task_type": "extension-operation", "status": "running"}, "resource-a"
			case "files":
				item, expected = map[string]any{"uuid": "resource-a", "file_name": "document.pdf", "status": "completed"}, "document.pdf"
			case "plugins":
				item = map[string]any{"author": "author-a", "name": "Resource A"}
			}
			value := map[string]any{
				"ok": true, "data": map[string]any{key: []any{item}},
			}
			if err := RenderCommand(&out, value, FormatDefault, commands[key]); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(out.String(), expected) {
				t.Fatalf("default output omitted %s rows: %s", key, out.String())
			}
		})
	}
}

func TestRenderPipelineListYAML(t *testing.T) {
	var out bytes.Buffer
	value := map[string]any{
		"ok": true,
		"data": map[string]any{
			"pipelines": []any{
				map[string]any{
					"uuid":   "pipeline-a",
					"name":   "Pipeline A",
					"config": map[string]any{"provider": map[string]any{"api_key": "pipeline-secret"}},
				},
			},
		},
	}
	if err := Render(&out, value, FormatYAML); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "pipeline-a") || !strings.Contains(out.String(), "Pipeline A") {
		t.Fatalf("resource YAML omitted list rows: %s", out.String())
	}
	if strings.Contains(out.String(), "pipeline-secret") {
		t.Fatalf("resource YAML exposed sensitive configuration: %s", out.String())
	}
}

func TestRenderReturnsEncodingAndWriterErrors(t *testing.T) {
	var out bytes.Buffer
	if err := Render(&out, map[string]any{"bad": make(chan int)}, FormatJSON); err == nil {
		t.Fatal("expected JSON encoding error")
	}
	failing := failWriter{err: errors.New("write failed")}
	if err := Render(failing, map[string]any{"ok": true}, FormatJSON); err == nil {
		t.Fatal("expected writer error")
	}
}

type failWriter struct{ err error }

func (w failWriter) Write([]byte) (int, error) { return 0, w.err }
