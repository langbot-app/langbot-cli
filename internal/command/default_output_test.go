package command_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/langbot-app/langbot-cli/internal/command"
)

func TestDefaultVersionUsesConciseOutput(t *testing.T) {
	code, out, diagnostic := executeCommand(t, nil, "version")
	if code != 0 || diagnostic != "" {
		t.Fatalf("version failed: code=%d diagnostic=%q output=%q", code, diagnostic, out)
	}
	if strings.TrimSpace(out) != "test" {
		t.Fatalf("default version output = %q, want one version line", out)
	}
}

func TestMachineOutputsPreserveResultEnvelope(t *testing.T) {
	for _, format := range []string{"json", "yaml"} {
		t.Run(format, func(t *testing.T) {
			code, out, diagnostic := executeCommand(t, nil, "--output", format, "version")
			if code != 0 || diagnostic != "" {
				t.Fatalf("version failed: code=%d diagnostic=%q output=%q", code, diagnostic, out)
			}
			if format == "json" {
				var envelope map[string]any
				if err := json.Unmarshal([]byte(out), &envelope); err != nil {
					t.Fatalf("JSON output is invalid: %v", err)
				}
				if envelope["ok"] != true || envelope["meta"] != nil {
					t.Fatalf("unexpected JSON envelope: %#v", envelope)
				}
				data, ok := envelope["data"].(map[string]any)
				if !ok || data["version"] != "test" || data["commit"] != "test" || data["build_date"] != "test" {
					t.Fatalf("version result fields were not preserved: %#v", envelope["data"])
				}
				return
			}
			for _, want := range []string{"ok: true", "version: test", "commit: test", "build_date: test"} {
				if !strings.Contains(out, want) {
					t.Fatalf("YAML output omitted %q: %s", want, out)
				}
			}
		})
	}
}

func TestSchemaDefaultsToJSONIncludingEarlyValidationErrors(t *testing.T) {
	for _, args := range [][]string{
		{"schema", "--command", "version"},
		{"schema", "--unknown-flag"},
		{"--config=", "schema"},
	} {
		t.Run(strings.Join(args, "_"), func(t *testing.T) {
			code, out, diagnostic := executeCommand(t, nil, args...)
			wantCode := 2
			if len(args) > 1 && args[1] == "--command" {
				wantCode = 0
			}
			if code != wantCode || diagnostic != "" || !strings.HasPrefix(strings.TrimSpace(out), "{") {
				t.Fatalf("schema did not use JSON on early error: code=%d diagnostic=%q output=%q", code, diagnostic, out)
			}
			var envelope map[string]any
			if err := json.Unmarshal([]byte(out), &envelope); err != nil {
				t.Fatalf("schema output is not JSON: %v", err)
			}
			if args[1] == "--command" && envelope["ok"] != true {
				t.Fatalf("schema command failed: %s", out)
			}
		})
	}

	code, out, diagnostic := executeCommand(t, nil, "--output", "yaml", "schema", "--command", "version")
	if code != 0 || diagnostic != "" || !strings.Contains(out, "schema_version: 1") || !strings.Contains(out, "default_output: default") {
		t.Fatalf("explicit YAML schema output is invalid: code=%d diagnostic=%q output=%s", code, diagnostic, out)
	}
}

func TestRemovedOutputFormatValuesAreRejected(t *testing.T) {
	for _, value := range []string{"", "table", "human", "HUMAN"} {
		t.Run(value, func(t *testing.T) {
			code, out, diagnostic := executeCommand(t, nil, "--output="+value, "version")
			if code != 2 || diagnostic != "" || strings.HasPrefix(strings.TrimSpace(out), "{") || !strings.Contains(out, "输出格式无效") {
				t.Fatalf("format %q was not rejected as a default-mode input error: code=%d diagnostic=%q output=%q", value, code, diagnostic, out)
			}
		})
	}
}

func TestDefaultErrorsAreDefaultAndMachineErrorsStayStructured(t *testing.T) {
	code, out, diagnostic := executeCommand(t, nil, "unknown-command")
	if code != 2 || diagnostic != "" || strings.HasPrefix(strings.TrimSpace(out), "{") || !strings.Contains(out, "错误：未知命令或参数") || strings.Contains(out, "Type:") {
		t.Fatalf("default error output is not concise: code=%d diagnostic=%q output=%q", code, diagnostic, out)
	}

	for _, format := range []string{"json", "yaml"} {
		code, out, diagnostic = executeCommand(t, nil, "--output", format, "unknown-command")
		if code != 2 || diagnostic != "" {
			t.Fatalf("%s error failed: code=%d diagnostic=%q output=%q", format, code, diagnostic, out)
		}
		if format == "json" {
			var envelope map[string]any
			if err := json.Unmarshal([]byte(out), &envelope); err != nil || envelope["ok"] != false {
				t.Fatalf("JSON error is not a result envelope: %q", out)
			}
		} else if !strings.Contains(out, "ok: false") || !strings.Contains(out, "type: input") {
			t.Fatalf("YAML error is not a result envelope: %q", out)
		}
	}
}

func TestAliasAndContextNameDoNotSelectSchemaOutput(t *testing.T) {
	code, out, diagnostic := executeCommand(t, nil, "kb")
	if code != 0 || diagnostic != "" || strings.HasPrefix(strings.TrimSpace(out), "{") || !strings.Contains(out, "管理知识库") {
		t.Fatalf("kb alias unexpectedly selected machine output: code=%d diagnostic=%q output=%q", code, diagnostic, out)
	}

	configPath := filepath.Join(t.TempDir(), "config.yaml")
	code, out, diagnostic = executeCommandWithConfig(t, configPath, nil, "context", "add", "schema", "--endpoint", "http://example.test")
	if code != 0 || diagnostic != "" || strings.HasPrefix(strings.TrimSpace(out), "{") || !strings.Contains(out, "schema") {
		t.Fatalf("context named schema unexpectedly selected machine output: code=%d diagnostic=%q output=%q", code, diagnostic, out)
	}
}

func TestServerVersionUsesOneInfoRequest(t *testing.T) {
	var calls atomic.Int32
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.Method != http.MethodGet || request.URL.Path != "/api/v1/system/info" {
			return nil, &unexpectedRequestError{method: request.Method, path: request.URL.Path}
		}
		calls.Add(1)
		return &http.Response{
			StatusCode: http.StatusOK,
			Status:     "200 OK",
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"code":0,"data":{"version":"remote-v4","edition":"cloud"}}`)),
			Request:    request,
		}, nil
	})
	code, out, diagnostic := executeCommandWithTransport(t, transport, "--endpoint", "https://example.test", "version", "--server")
	if code != 0 || diagnostic != "" || calls.Load() != 1 {
		t.Fatalf("server version request count/result invalid: code=%d calls=%d diagnostic=%q output=%q", code, calls.Load(), diagnostic, out)
	}
	for _, want := range []string{"CLI: test", "Server Version: remote-v4", "Edition: cloud"} {
		if !strings.Contains(out, want) {
			t.Fatalf("server version output omitted %q: %q", want, out)
		}
	}
}

func executeCommand(t *testing.T, transport http.RoundTripper, args ...string) (int, string, string) {
	return executeCommandWithConfigAndTransport(t, filepath.Join(t.TempDir(), "config.yaml"), transport, nil, args...)
}

func executeCommandWithConfig(t *testing.T, configPath string, transport http.RoundTripper, args ...string) (int, string, string) {
	return executeCommandWithConfigAndTransport(t, configPath, transport, nil, args...)
}

func executeCommandWithTransport(t *testing.T, transport http.RoundTripper, args ...string) (int, string, string) {
	return executeCommandWithConfigAndTransport(t, filepath.Join(t.TempDir(), "config.yaml"), transport, nil, args...)
}

func executeCommandWithConfigAndTransport(t *testing.T, configPath string, transport http.RoundTripper, input io.Reader, args ...string) (int, string, string) {
	t.Helper()
	if input == nil {
		input = strings.NewReader("")
	}
	var out, diagnostic bytes.Buffer
	code := command.Execute(context.Background(), args, command.Dependencies{
		In: input, Out: &out, Err: &diagnostic, Transport: transport,
		LookupEnv:         func(string) (string, bool) { return "", false },
		DefaultConfigPath: func() (string, error) { return configPath, nil },
		Version:           "test", Commit: "test", BuildDate: "test",
	})
	return code, out.String(), diagnostic.String()
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

type unexpectedRequestError struct {
	method string
	path   string
}

func (e *unexpectedRequestError) Error() string {
	return "unexpected request: " + e.method + " " + e.path
}
