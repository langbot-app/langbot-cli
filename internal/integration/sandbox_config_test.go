package integration_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

const sandboxWorkspaceKey = "fixture-workspace-private-key"
const sandboxE2BKey = "fixture-e2b-private-key"

type sandboxContract struct {
	mu                                sync.Mutex
	mode                              string
	permissions                       []string
	settings                          map[string]any
	requests                          []string
	puts, resets, checks, tests, gets int
	lastBody                          map[string]any
}

func newSandboxContract() *sandboxContract {
	common := map[string]any{"timeout_sec": true, "idle_ttl_sec": true, "max_sessions": true}
	local := map[string]any{"timeout_sec": true, "idle_ttl_sec": true, "max_sessions": true, "cpus": true, "memory_mb": true, "pids_limit": true, "workspace_quota_mb": true}
	return &sandboxContract{permissions: []string{"workspace.view", "workspace.update"}, settings: map[string]any{
		"backend": "docker", "effective_backend": "docker", "enabled": true, "e2b_configurable": true,
		"e2b":    map[string]any{"api_url": "https://api.example.test", "sandbox_url": "https://sandbox.example.test", "template": "base", "api_key_configured": true, "api_key": "fixture-stored-key-must-not-escape", "source": "workspace"},
		"limits": map[string]any{}, "limit_caps": map[string]any{"timeout_sec": 300, "idle_ttl_sec": 86400, "max_sessions": 4, "cpus": 2, "memory_mb": 2048, "pids_limit": 128, "workspace_quota_mb": 1024},
		"backends": []any{map[string]any{"name": "docker", "available": true, "limit_capabilities": local}, map[string]any{"name": "e2b", "available": true, "template_validated": false, "template_validation": "unsupported", "limit_capabilities": common}},
	}}
}

func (f *sandboxContract) serve(w http.ResponseWriter, request *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = append(f.requests, request.Method+" "+request.URL.RequestURI())
	if request.Header.Get("X-API-Key") != sandboxWorkspaceKey {
		w.WriteHeader(401)
		fmt.Fprint(w, `{"code":401,"data":null}`)
		return
	}
	respond := func(data any) { _ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": data}) }
	switch request.URL.Path {
	case "/api/v1/system/context":
		workspace := "workspace-a"
		if f.mode == "wrong-workspace" {
			workspace = "workspace-b"
		}
		respond(map[string]any{"instance_uuid": "instance-a", "workspace_uuid": workspace, "api_key_id": "key-a", "permissions": f.permissions})
		return
	case "/api/v1/system/capabilities":
		operations := map[string]any{}
		if f.mode != "old-core" {
			for _, action := range []string{"get", "update", "reset", "check", "test"} {
				operations["workspace.sandbox."+action] = map[string]any{"supported": true}
			}
		}
		respond(map[string]any{"schema_version": 1, "operations": operations})
		return
	}
	const path = "/api/v1/workspaces/current/sandbox"
	if request.URL.Path == path && request.Method == "DELETE" {
		f.resets++
		if f.mode == "reset-unknown" {
			w.WriteHeader(500)
			fmt.Fprint(w, `{"code":"fixture_error","data":null}`)
			return
		}
		if f.mode != "reset-ignored" {
			f.settings["backend"], f.settings["limits"] = nil, map[string]any{}
			f.settings["e2b"] = map[string]any{"source": "deployment", "api_key_configured": false}
		}
		if f.mode == "reset-bad-ack" {
			respond(map[string]any{})
			return
		}
		if f.mode == "reset-bad-ack" {
			respond(map[string]any{})
			return
		}
		respond(map[string]any{"reset": true})
		return
	}
	if request.URL.Path == path && request.Method == "GET" {
		f.gets++
		if f.mode == "reset-readback-failed" && f.resets > 0 {
			w.WriteHeader(500)
			fmt.Fprint(w, `{"code":"fixture_error","data":null}`)
			return
		}
		if f.mode == "reset-readback-failed" && f.resets > 0 {
			w.WriteHeader(500)
			fmt.Fprint(w, `{"code":"fixture_error","data":null}`)
			return
		}
		if f.mode == "old-sdk" {
			w.WriteHeader(400)
			fmt.Fprint(w, `{"code":"validation_error","msg":"Workspace sandbox settings require an upgraded Box SDK and Runtime","data":null}`)
			return
		}
		if f.mode == "malformed" {
			respond(map[string]any{"backends": []any{}})
			return
		}
		if f.mode == "mismatch" && f.puts > 0 {
			f.settings["backend"] = "nsjail"
		}
		respond(f.settings)
		return
	}
	if request.URL.Path != path && request.URL.Path != path+"/check" && request.URL.Path != path+"/test" {
		type unknown struct{}
		w.WriteHeader(404)
		respond(unknown{})
		return
	}
	if err := json.NewDecoder(request.Body).Decode(&f.lastBody); err != nil {
		w.WriteHeader(400)
		return
	}
	if f.mode == "old-sdk-test" || f.mode == "private-rejection" {
		w.WriteHeader(400)
		if f.mode == "old-sdk-test" {
			fmt.Fprint(w, `{"code":"validation_error","msg":"Workspace sandbox settings require an upgraded Box SDK and Runtime","data":null}`)
		} else {
			_ = json.NewEncoder(w).Encode(map[string]any{"code": sandboxE2BKey, "msg": sandboxE2BKey, "data": nil})
		}
		return
	}
	if request.Method == "PUT" && request.URL.Path == path {
		f.puts++
		f.settings["backend"] = f.lastBody["backend"]
		if limits, ok := f.lastBody["limits"].(map[string]any); ok {
			f.settings["limits"] = limits
		}
		if e2b, ok := f.lastBody["e2b"].(map[string]any); ok {
			public := f.settings["e2b"].(map[string]any)
			for key, value := range e2b {
				if key != "api_key" {
					public[key] = value
				}
			}
		}
		if f.mode == "save-unknown" {
			w.WriteHeader(500)
			fmt.Fprint(w, `{"code":"fixture_error","data":null}`)
			return
		}
		respond(f.settings)
		return
	}
	if request.Method == "POST" {
		if request.URL.Path == path+"/check" {
			f.checks++
		} else {
			f.tests++
		}
		e2b := f.settings["backends"].([]any)[1].(map[string]any)
		if draft, ok := f.lastBody["e2b"].(map[string]any); ok {
			if key, ok := draft["api_key"].(string); ok {
				e2b["reason"] = "fixture diagnostic " + key + " " + sandboxWorkspaceKey
			}
		}
		if f.tests > 0 {
			e2b["template_validated"], e2b["template_validation"] = true, "ready"
		}
		if f.mode == "unsupported-test" {
			e2b["template_validated"], e2b["template_validation"] = false, "unsupported"
		}
		if f.mode == "check-failed" || f.mode == "cleanup-failed" {
			e2b["available"], e2b["template_validated"] = false, false
			if f.mode == "cleanup-failed" {
				e2b["reason"], e2b["cleanup_reason"] = "template_cleanup_failed", "template_cleanup_failed"
			}
		}
		respond(f.settings)
		return
	}
	w.WriteHeader(405)
}

func runSandboxBinary(t *testing.T, binary, config, input, format string, args ...string) execution {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	arguments := []string{"--config", config, "--timeout", "2s"}
	if format != "" {
		arguments = append(arguments, "-o", format)
	}
	arguments = append(arguments, args...)
	command := exec.CommandContext(ctx, binary, arguments...)
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		if !strings.HasPrefix(strings.ToUpper(name), "LANGBOT_") && name != "SANDBOX_WORKSPACE_KEY" && name != "SANDBOX_E2B_KEY" {
			command.Env = append(command.Env, entry)
		}
	}
	command.Env = append(command.Env, "SANDBOX_WORKSPACE_KEY="+sandboxWorkspaceKey, "SANDBOX_E2B_KEY="+sandboxE2BKey)
	command.Stdin = strings.NewReader(input)
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	err := command.Run()
	code := 0
	if failure, ok := err.(*exec.ExitError); ok {
		code = failure.ExitCode()
	} else if err != nil {
		t.Fatalf("binary execution failed: %v", err)
	}
	for _, secret := range []string{sandboxWorkspaceKey, sandboxE2BKey, "fixture-stored-key-must-not-escape"} {
		if strings.Contains(stdout.String()+stderr.String(), secret) {
			t.Fatal("sandbox command leaked a credential")
		}
	}
	data := map[string]any{}
	if format == "json" && json.Unmarshal(stdout.Bytes(), &data) != nil {
		t.Fatalf("invalid result envelope: %s %s", &stdout, &stderr)
	}
	return execution{code: code, out: stdout.String(), data: data}
}

func TestCompiledSandboxConfigHTTPContract(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "lbctl")
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	build := exec.Command("go", "build", "-o", binary, "./cmd/lbctl")
	build.Dir = filepath.Join("..", "..")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build failed: %v %s", err, output)
	}
	for _, test := range []struct {
		name, action, body, mode                string
		input, format                           string
		flags                                   []string
		permissions                             []string
		code, gets, puts, resets, checks, tests int
	}{
		{name: "query refresh", action: "get", flags: []string{"--refresh"}, gets: 1},
		{name: "reset overrides", action: "reset", flags: []string{"--yes"}, gets: 1, resets: 1},
		{name: "reset preview", action: "reset", flags: []string{"--dry-run"}},
		{name: "reset confirmation", action: "reset", code: 2},
		{name: "reset permission", action: "reset", flags: []string{"--yes"}, permissions: []string{"workspace.view"}, code: 4},
		{name: "reset readback permission", action: "reset", flags: []string{"--yes"}, permissions: []string{"workspace.update"}, code: 4},
		{name: "reset old Core", action: "reset", mode: "old-core", flags: []string{"--yes"}, code: 6},
		{name: "reset wrong workspace", action: "reset", mode: "wrong-workspace", flags: []string{"--yes"}, code: 6},
		{name: "reset ignored", action: "reset", mode: "reset-ignored", flags: []string{"--yes"}, code: 10, gets: 1, resets: 1},
		{name: "reset unknown", action: "reset", mode: "reset-unknown", flags: []string{"--yes"}, code: 7, resets: 1},
		{name: "reset missing acknowledgement", action: "reset", mode: "reset-bad-ack", flags: []string{"--yes"}, code: 7, resets: 1},
		{name: "reset readback failed", action: "reset", mode: "reset-readback-failed", flags: []string{"--yes"}, code: 10, resets: 1, gets: 1},
		{name: "reset missing acknowledgement", action: "reset", mode: "reset-bad-ack", flags: []string{"--yes"}, code: 7, resets: 1},
		{name: "reset readback failed", action: "reset", mode: "reset-readback-failed", flags: []string{"--yes"}, code: 10, resets: 1, gets: 1},
		{name: "YAML query", action: "get", format: "yaml", gets: 1},
		{name: "human check", action: "check", format: "human", checks: 1},
		{name: "save limits", action: "update", body: `{"backend":"docker","limits":{"timeout_sec":60,"idle_ttl_sec":120,"max_sessions":2,"cpus":0.5,"memory_mb":64,"pids_limit":16,"workspace_quota_mb":128}}`, flags: []string{"--yes"}, gets: 2, puts: 1},
		{name: "save E2B secret from env", action: "update", body: `{"backend":"e2b","e2b":{"template":"custom"}}`, flags: []string{"--yes", "--e2b-api-key-env", "SANDBOX_E2B_KEY"}, gets: 2, puts: 1},
		{name: "preserve E2B key", action: "update", body: `{"backend":"e2b","e2b":{"template":"custom"}}`, flags: []string{"--yes"}, gets: 2, puts: 1},
		{name: "save nsjail", action: "update", body: `{"backend":"nsjail"}`, flags: []string{"--yes"}, gets: 2, puts: 1},
		{name: "save dry run", action: "update", body: `{"backend":"docker","limits":{"cpus":1}}`, flags: []string{"--dry-run"}, gets: 1},
		{name: "save requires confirmation", action: "update", body: `{"backend":"docker"}`, code: 2},
		{name: "saved connection check", action: "check", checks: 1, permissions: []string{"workspace.update"}},
		{name: "draft check with private diagnostics", action: "check", body: `{"e2b":{"template":"base"}}`, flags: []string{"--e2b-api-key-env", "SANDBOX_E2B_KEY"}, checks: 1},
		{name: "draft from stdin", action: "check", input: `{"e2b":{"api_key":"fixture-e2b-private-key"}}`, flags: []string{"--file", "-"}, checks: 1},
		{name: "Workspace key from stdin", action: "check", input: sandboxWorkspaceKey, flags: []string{"--api-key-stdin", "--e2b-api-key-env", "SANDBOX_E2B_KEY"}, checks: 1},
		{name: "stdin conflict", action: "check", input: `{}`, flags: []string{"--file", "-", "--api-key-stdin"}, code: 2},
		{name: "duplicate key sources", action: "check", body: `{"e2b":{"api_key":"fixture-e2b-private-key"}}`, flags: []string{"--e2b-api-key-env", "SANDBOX_E2B_KEY"}, code: 2},
		{name: "private rejection", action: "check", flags: []string{"--e2b-api-key-env", "SANDBOX_E2B_KEY"}, mode: "private-rejection", code: 2},
		{name: "check failure is not HTTP success", action: "check", mode: "check-failed", code: 6, checks: 1},
		{name: "explicit temporary test", action: "test", flags: []string{"--yes"}, permissions: []string{"workspace.update"}, tests: 1},
		{name: "test requires confirmation", action: "test", code: 2},
		{name: "test dry run creates nothing", action: "test", flags: []string{"--dry-run"}},
		{name: "cleanup failure", action: "test", mode: "cleanup-failed", flags: []string{"--yes"}, code: 10, tests: 1},
		{name: "unsupported test", action: "test", mode: "unsupported-test", flags: []string{"--yes"}, code: 8, tests: 1},
		{name: "old SDK test rejection", action: "test", mode: "old-sdk-test", flags: []string{"--yes"}, code: 8},
		{name: "unsupported limit", action: "update", body: `{"backend":"e2b","limits":{"memory_mb":64}}`, flags: []string{"--yes"}, code: 6, gets: 1},
		{name: "server cap exceeded", action: "update", body: `{"backend":"docker","limits":{"cpus":99}}`, flags: []string{"--yes"}, code: 2, gets: 1},
		{name: "boolean limit rejected", action: "update", body: `{"backend":"docker","limits":{"max_sessions":true}}`, flags: []string{"--yes"}, code: 2},
		{name: "invalid backend", action: "update", body: `{"backend":"invalid"}`, flags: []string{"--yes"}, code: 2},
		{name: "permission rejected", action: "check", permissions: []string{"workspace.view"}, code: 4},
		{name: "save requires readback permission", action: "update", body: `{"backend":"docker"}`, flags: []string{"--yes"}, permissions: []string{"workspace.update"}, code: 4},
		{name: "old Core", action: "get", mode: "old-core", code: 6},
		{name: "old SDK", action: "get", mode: "old-sdk", code: 8, gets: 1},
		{name: "malformed settings", action: "get", mode: "malformed", code: 8, gets: 1},
		{name: "uncertain save", action: "update", body: `{"backend":"e2b"}`, mode: "save-unknown", flags: []string{"--yes"}, code: 7, gets: 1, puts: 1},
		{name: "workspace mismatch", action: "check", mode: "wrong-workspace", code: 6},
		{name: "temporary endpoint rejected", action: "update", body: `{"backend":"docker"}`, flags: []string{"--yes", "--endpoint", "http://invalid.example.test"}, code: 6},
		{name: "save readback mismatch", action: "update", body: `{"backend":"e2b"}`, flags: []string{"--yes"}, mode: "mismatch", code: 10, puts: 1, gets: 2},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newSandboxContract()
			fixture.mode = test.mode
			if test.permissions != nil {
				fixture.permissions = test.permissions
			}
			server := httptest.NewServer(http.HandlerFunc(fixture.serve))
			defer server.Close()
			dir := t.TempDir()
			config := filepath.Join(dir, "config.yaml")
			text := fmt.Sprintf("version: 1\ncurrent_context: sandbox\ncontexts:\n  sandbox:\n    endpoint: %s\n    credential:\n      type: env\n      name: SANDBOX_WORKSPACE_KEY\n    expected_workspace_uuid: workspace-a\n", server.URL)
			if err := os.WriteFile(config, []byte(text), 0o600); err != nil {
				t.Fatal(err)
			}
			args := []string{"sandbox", "config", test.action}
			if test.body != "" {
				file := filepath.Join(dir, "request.json")
				if err := os.WriteFile(file, []byte(test.body), 0o600); err != nil {
					t.Fatal(err)
				}
				args = append(args, "--file", file)
			}
			args = append(args, test.flags...)
			format := test.format
			if format == "" {
				format = "json"
			} else if format == "human" {
				format = ""
			}
			got := runSandboxBinary(t, binary, config, test.input, format, args...)
			fixture.mu.Lock()
			defer fixture.mu.Unlock()
			if got.code != test.code || fixture.gets != test.gets || fixture.puts != test.puts || fixture.resets != test.resets || fixture.checks != test.checks || fixture.tests != test.tests {
				t.Fatalf("exit=%d requests=%v get/put/check/test=%d/%d/%d/%d result=%s", got.code, fixture.requests, fixture.gets, fixture.puts, fixture.checks, fixture.tests, got.out)
			}
			if test.name == "query refresh" {
				if !strings.Contains(got.out, `"api_key_configured": true`) || fixture.requests[len(fixture.requests)-1] != "GET /api/v1/workspaces/current/sandbox?refresh=true" {
					t.Fatal("query lost credential availability or refresh selector")
				}
			}
			if test.name == "reset readback failed" {
				data, _ := got.data["data"].(map[string]any)
				if data["server_write"] != true || data["saved"] != true {
					t.Fatal("readback failure lost the committed reset state")
				}
			}
			if test.name == "reset readback failed" {
				data, _ := got.data["data"].(map[string]any)
				if data["server_write"] != true || data["saved"] != true {
					t.Fatal("readback failure lost the committed reset state")
				}
			}
			if test.name == "save E2B secret from env" && fixture.lastBody["e2b"].(map[string]any)["api_key"] != sandboxE2BKey {
				t.Fatal("private environment key was not mapped into the request")
			}
			if test.name == "YAML query" && !strings.Contains(got.out, "api_key_configured: true") {
				t.Fatal("YAML credential availability missing")
			}
			if test.name == "human check" && !strings.Contains(got.out, "未创建沙箱") {
				t.Fatal("human check result omits its scope")
			}
			if test.name == "preserve E2B key" {
				if _, exists := fixture.lastBody["e2b"].(map[string]any)["api_key"]; exists {
					t.Fatal("omitted credential was replaced")
				}
			}
			if test.name == "cleanup failure" && !strings.Contains(got.out, "template_cleanup_failed") {
				t.Fatal("cleanup diagnostics lost")
			}
			if test.name == "old SDK" && !strings.Contains(got.out, "Box SDK") {
				t.Fatal("upgrade requirement lost")
			}
			if test.name == "uncertain save" {
				if data := got.data["data"].(map[string]any); data["saved"] != "unknown" || data["server_write"] != "unknown" {
					t.Fatal("uncertain save was reported as a known rejection")
				}
			}
			stored, _ := os.ReadFile(config)
			if string(stored) != text {
				t.Fatal("sandbox operation modified the local context file")
			}
		})
	}
}
