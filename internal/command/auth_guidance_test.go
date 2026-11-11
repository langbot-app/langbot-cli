package command_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/langbot-app/langbot-cli/internal/command"
	"go.yaml.in/yaml/v3"
)

func TestAuthenticationSetupGuidance(t *testing.T) {
	const secret = "never-echo-this-api-key"
	for _, test := range []struct {
		name, reason, variable, input string
		env                           map[string]string
		custom                        bool
		stdin                         bool
		override                      bool
		status                        int
	}{
		{name: "no source", reason: "missing_api_key", variable: "LANGBOT_API_KEY"},
		{name: "missing custom variable", custom: true, reason: "environment_variable_unavailable", variable: "DEV_KEY"},
		{name: "empty custom variable", custom: true, env: map[string]string{"DEV_KEY": " "}, reason: "environment_variable_unavailable", variable: "DEV_KEY"},
		{name: "empty global overrides custom", custom: true, env: map[string]string{"DEV_KEY": secret, "LANGBOT_API_KEY": ""}, reason: "environment_variable_unavailable", variable: "LANGBOT_API_KEY"},
		{name: "empty stdin", stdin: true, reason: "empty_stdin", variable: "LANGBOT_API_KEY"},
		{name: "changed target", custom: true, override: true, reason: "endpoint_changed", variable: "LANGBOT_API_KEY"},
		{name: "rejected custom key", custom: true, env: map[string]string{"DEV_KEY": secret}, status: 401, reason: "authentication_rejected", variable: "DEV_KEY"},
		{name: "rejected stdin key", stdin: true, input: secret, status: 401, reason: "authentication_rejected"},
	} {
		for _, format := range []string{"default", "json", "yaml"} {
			t.Run(test.name+"/"+format, func(t *testing.T) {
				configPath := filepath.Join(t.TempDir(), "config.yaml")
				args := []string{"--endpoint", "http://localhost:5300", "whoami"}
				if test.custom {
					fixture := "version: 1\ncurrent_context: dev\ncontexts:\n  dev:\n    endpoint: http://localhost:5300\n    credential:\n      type: env\n      name: DEV_KEY\n"
					if err := os.WriteFile(configPath, []byte(fixture), 0600); err != nil {
						t.Fatal(err)
					}
					args = []string{"whoami"}
				}
				if test.stdin {
					args = append(args, "--api-key-stdin")
				}
				if test.override {
					args = append(args, "--endpoint", "http://other.local:5300")
				}
				if format != "default" {
					args = append(args, "-o", format)
				}
				calls := 0
				transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
					calls++
					if test.status == 0 {
						t.Fatal("missing credentials must fail before an authenticated request")
					}
					if request.Header.Get("X-API-Key") != secret {
						t.Fatal("credential precedence changed")
					}
					return &http.Response{StatusCode: test.status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"code":-1,"msg":"` + secret + `"}`))}, nil
				})
				var out, diagnostic bytes.Buffer
				code := command.Execute(context.Background(), args, command.Dependencies{
					In: strings.NewReader(test.input), Out: &out, Err: &diagnostic, Transport: transport,
					DefaultConfigPath: func() (string, error) { return configPath, nil },
					LookupEnv:         func(name string) (string, bool) { value, ok := test.env[name]; return value, ok },
				})
				if code != 3 || diagnostic.Len() != 0 || strings.Contains(out.String(), secret) {
					t.Fatalf("unsafe or invalid auth result: code=%d out=%s stderr=%s", code, &out, &diagnostic)
				}
				if test.status != 0 && calls != 1 {
					t.Fatalf("unexpected authenticated calls: %d", calls)
				}
				if format == "default" {
					if !strings.Contains(out.String(), "API Key 管理") || !strings.Contains(out.String(), "lbctl whoami") {
						t.Fatalf("missing setup steps: %s", &out)
					}
					if test.reason == "authentication_rejected" {
						if !strings.Contains(out.String(), "过期或已吊销") || strings.Contains(out.String(), "Read-Host") {
							t.Fatalf("rejected key misreported as missing: %s", &out)
						}
					} else if !strings.Contains(out.String(), "$env:"+test.variable) || !strings.Contains(out.String(), "export "+test.variable) {
						t.Fatalf("wrong environment setup guidance: %s", &out)
					}
					return
				}
				var envelope map[string]any
				var err error
				if format == "json" {
					err = json.Unmarshal(out.Bytes(), &envelope)
				} else {
					err = yaml.Unmarshal(out.Bytes(), &envelope)
				}
				if err != nil {
					t.Fatal(err)
				}
				failure := envelope["error"].(map[string]any)
				guide := failure["guidance"].(map[string]any)
				if envelope["ok"] != false || failure["type"] != "auth" || failure["reason"] != test.reason {
					t.Fatalf("wrong machine result: %+v", envelope)
				}
				if test.variable != "" && guide["environment_variable"] != test.variable {
					t.Fatalf("wrong variable: %+v", guide)
				}
				if test.reason == "authentication_rejected" {
					if _, ok := guide["examples"]; ok {
						t.Fatal("rejected key should not be treated as unset")
					}
				} else if guide["examples"].(map[string]any)["powershell"] == "[REDACTED]" {
					t.Fatal("safe setup command was incorrectly masked")
				}
			})
		}
	}
}

func TestPublicInfoStillWorksWithoutAPIKey(t *testing.T) {
	code, out, diagnostic := executeCommand(t, roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Path != "/api/v1/system/info" || request.Header.Get("X-API-Key") != "" {
			t.Fatal("public info unexpectedly needs authentication")
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"code":0,"data":{"version":"v4.10.11","edition":"community"}}`))}, nil
	}), "--endpoint", "http://localhost:5300", "raw", "-o", "json")
	if code != 0 || diagnostic != "" || !strings.Contains(out, `"ok": true`) {
		t.Fatalf("public info failed: %d %s %s", code, out, diagnostic)
	}
}
