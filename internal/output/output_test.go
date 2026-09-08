package output

import (
	"bytes"
	"errors"
	"strings"
	"testing"
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

func TestRenderTableKeepsOverallErrorSeparateFromRows(t *testing.T) {
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
	if err := Render(&out, value, FormatTable); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(out.String(), "\n")
	for _, line := range lines {
		if strings.Contains(line, "alpha") && strings.Contains(line, "permission") {
			t.Fatalf("overall error was attached to a successful row: %s", line)
		}
	}
	for _, want := range []string{"ok", "error", "meta", "checks", "alpha", "beta", "permission", "endpoint"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("table output omitted %q: %s", want, out.String())
		}
	}
}

func TestRenderTableSupportsResourceLists(t *testing.T) {
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
	if err := Render(&out, value, FormatTable); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "bots") || !strings.Contains(out.String(), "bot-a") {
		t.Fatalf("resource table omitted list rows: %s", out.String())
	}
	if strings.Contains(out.String(), "bot-secret") {
		t.Fatalf("resource table exposed sensitive configuration: %s", out.String())
	}
}

func TestRenderTableSupportsManagedResourceLists(t *testing.T) {
	for _, key := range []string{"tasks", "bases", "files", "servers", "plugins", "skills"} {
		t.Run(key, func(t *testing.T) {
			var out bytes.Buffer
			value := map[string]any{
				"ok":   true,
				"data": map[string]any{key: []any{map[string]any{"uuid": "resource-a", "name": "Resource A"}}},
			}
			if err := Render(&out, value, FormatTable); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(out.String(), key) || !strings.Contains(out.String(), "resource-a") {
				t.Fatalf("table output omitted %s rows: %s", key, out.String())
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
