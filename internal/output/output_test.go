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
		"error": map[string]any{
			"http_status": int64(403),
			"server_code": int64(9223372036854770000),
		},
	}
	if err := Render(&out, value, FormatJSON); err != nil {
		t.Fatal(err)
	}
	jsonOutput := out.String()
	if strings.Contains(jsonOutput, "secret-value") || strings.Contains(jsonOutput, "another-secret") || !strings.Contains(jsonOutput, "safe-key-id") || !strings.Contains(jsonOutput, `"http_status": 403`) {
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
