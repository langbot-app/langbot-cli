package app

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/langbot-app/langbot-cli/internal/result"
)

func TestPluginSkillAndMCPFlowsRespectDryRunAndWait(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	packagePath := filepath.Join(t.TempDir(), "extension.zip")
	if err := os.WriteFile(packagePath, []byte("package"), 0600); err != nil {
		t.Fatal(err)
	}
	var pluginWrites, skillWrites, mcpWrites atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/system/context":
			fmt.Fprint(w, `{"code":0,"data":{"instance_uuid":"instance-a","workspace_uuid":"workspace-a","api_key_id":"key-a","permissions":["resource.manage","resource.view"]}}`)
		case "/api/v1/system/capabilities":
			fmt.Fprint(w, `{"code":0,"data":{"schema_version":1,"operations":{"plugin.install.github":{"supported":true},"plugin.get":{"supported":true},"skill.install.upload":{"supported":true},"mcp_server.get":{"supported":true},"mcp_server.test":{"supported":true},"task.get":{"supported":true}}}}`)
		case "/api/v1/plugins/install/github":
			pluginWrites.Add(1)
			fmt.Fprint(w, `{"code":0,"data":{"task_id":42}}`)
		case "/api/v1/skills/install/upload/preview":
			skillWrites.Add(1)
			fmt.Fprint(w, `{"code":0,"data":{"skills":[{"name":"demo","instructions":"safe"}]}}`)
		case "/api/v1/skills/install/upload":
			skillWrites.Add(1)
			fmt.Fprint(w, `{"code":0,"data":{"skills":[{"name":"demo"}]}}`)
		case "/api/v1/mcp/servers/server-a":
			fmt.Fprint(w, `{"code":0,"data":{"server":{"uuid":"server-id","name":"server-a","type":"stdio","config":{"token":"secret"}}}}`)
		case "/api/v1/mcp/servers/server-a/test":
			mcpWrites.Add(1)
			fmt.Fprint(w, `{"code":0,"data":{"task_id":43}}`)
		case "/api/v1/system/tasks/42":
			fmt.Fprint(w, `{"code":0,"data":{"id":42,"task_type":"user","kind":"plugin-operation","status":"succeeded","error":null,"result":null,"created_at":1}}`)
		case "/api/v1/system/tasks/43":
			fmt.Fprint(w, `{"code":0,"data":{"id":43,"task_type":"user","kind":"mcp-operation","status":"failed","error":{"type":"connect_failed","message":"connection failed"},"result":null,"created_at":1}}`)
		default:
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprint(w, `{"code":404,"data":null}`)
		}
	}))
	defer server.Close()

	service := New(Dependencies{
		LookupEnv: func(name string) (string, bool) {
			if name == "KEY" {
				return "secret", true
			}
			return "", false
		},
		DefaultConfigPath: func() (string, error) { return configPath, nil },
	})
	if _, err := service.ContextAdd("production", server.URL, "KEY", "workspace-a", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := service.ContextUse("production"); err != nil {
		t.Fatal(err)
	}
	options := CheckOptions{ContextSet: true, Context: "production"}

	plugin, err := service.PluginInstallGitHub(context.Background(), map[string]any{"owner": "a"}, false, true, WaitOptions{Wait: true, PollInterval: time.Millisecond, WaitTimeout: time.Second}, options)
	if err != nil || plugin.Data.(map[string]any)["task_id"] != "42" || plugin.Data.(map[string]any)["step"] != "completed" {
		t.Fatalf("plugin install = %#v, error = %v", plugin, err)
	}
	if pluginWrites.Load() != 1 {
		t.Fatalf("plugin writes = %d", pluginWrites.Load())
	}

	skill, err := service.SkillInstallUpload(context.Background(), packagePath, []string{"one", "two"}, true, options)
	if err != nil || skill.Data.(map[string]any)["business_validation"] != "server_preview" {
		t.Fatalf("skill dry-run = %#v, error = %v", skill, err)
	}
	if skillWrites.Load() != 1 {
		t.Fatalf("skill writes = %d, want preview only", skillWrites.Load())
	}

	mcp, err := service.MCPServerTest(context.Background(), "server-a", map[string]any{}, false, true, WaitOptions{Wait: true, PollInterval: time.Millisecond, WaitTimeout: time.Second}, options)
	if result := resultError(err); result == nil || result.Type != "task_failed" {
		t.Fatalf("MCP task failure = %#v, error = %v", result, err)
	}
	if data, ok := mcp.Data.(map[string]any); !ok || data["task_id"] != "43" || data["submitted"] != true {
		t.Fatalf("MCP failure lost submitted task = %#v", mcp.Data)
	}
	if mcpWrites.Load() != 1 {
		t.Fatalf("MCP writes = %d", mcpWrites.Load())
	}
}

func resultError(err error) *result.Error {
	if err == nil {
		return nil
	}
	return result.AsError(err)
}

func TestAsyncExtensionSubmissionDistinguishesRejectionAndUnknownResult(t *testing.T) {
	for _, test := range []struct {
		name          string
		writeResponse *http.Response
		writeError    error
		wantType      string
		wantSubmitted any
		wantStep      string
	}{
		{
			name:          "rejected",
			writeResponse: taskHTTPResponse(http.StatusBadRequest, `{"code":"invalid_request","data":null}`),
			wantType:      "server",
			wantSubmitted: false,
			wantStep:      "submission_failed",
		},
		{
			name:          "unknown",
			writeError:    errors.New("connection reset after request"),
			wantType:      "result_unknown",
			wantSubmitted: "unknown",
			wantStep:      "submission_unknown",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			service, options := extensionService(t, taskRoundTripFunc(func(request *http.Request) (*http.Response, error) {
				switch request.URL.Path {
				case "/api/v1/system/context":
					return extensionIdentityResponse(), nil
				case "/api/v1/system/capabilities":
					return extensionCapabilitiesResponse("plugin.install.github"), nil
				case "/api/v1/plugins/install/github":
					return test.writeResponse, test.writeError
				default:
					return nil, fmt.Errorf("unexpected path %s", request.URL.Path)
				}
			}))
			value, err := service.PluginInstallGitHub(context.Background(), map[string]any{"owner": "a"}, false, false, WaitOptions{}, options)
			if failure := result.AsError(err); failure.Type != test.wantType {
				t.Fatalf("error = %+v", failure)
			}
			data := value.Data.(map[string]any)
			if data["submitted"] != test.wantSubmitted || data["step"] != test.wantStep {
				t.Fatalf("data = %#v", data)
			}
		})
	}
}

func TestExtensionWaitCapabilityIsCheckedBeforeSubmission(t *testing.T) {
	var writes atomic.Int32
	service, options := extensionService(t, taskRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		switch request.URL.Path {
		case "/api/v1/system/context":
			return extensionIdentityResponse(), nil
		case "/api/v1/system/capabilities":
			return extensionCapabilitiesResponse("plugin.install.github"), nil
		default:
			writes.Add(1)
			return taskResponse(`{"code":0,"data":{"task_id":1}}`), nil
		}
	}))
	_, err := service.PluginInstallGitHub(context.Background(), map[string]any{}, false, true, WaitOptions{Wait: true}, options)
	if failure := result.AsError(err); failure.Kind != "precondition" {
		t.Fatalf("error = %+v", failure)
	}
	if writes.Load() != 0 {
		t.Fatalf("writes = %d", writes.Load())
	}
}

func TestPluginUpgradeAndSavedMCPTestValidateTargetsBeforeSubmission(t *testing.T) {
	var calls []string
	service, options := extensionService(t, taskRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		calls = append(calls, request.Method+" "+request.URL.Path)
		switch request.URL.Path {
		case "/api/v1/system/context":
			return extensionIdentityResponse(), nil
		case "/api/v1/system/capabilities":
			return extensionCapabilitiesResponse("plugin.upgrade", "plugin.get", "mcp_server.test", "mcp_server.get"), nil
		case "/api/v1/plugins/author/name":
			return taskResponse(`{"code":0,"data":{"plugin":{"author":"author","name":"name","version":"1"}}}`), nil
		case "/api/v1/plugins/author/name/upgrade", "/api/v1/mcp/servers/saved/test":
			return taskResponse(`{"code":0,"data":{"task_id":2}}`), nil
		case "/api/v1/mcp/servers/saved":
			return taskResponse(`{"code":0,"data":{"server":{"uuid":"server-id","name":"saved"}}}`), nil
		default:
			return nil, fmt.Errorf("unexpected path %s", request.URL.Path)
		}
	}))
	if _, err := service.PluginUpgrade(context.Background(), "author", "name", false, false, WaitOptions{}, options); err != nil {
		t.Fatalf("PluginUpgrade() error = %v", err)
	}
	if _, err := service.MCPServerTest(context.Background(), "saved", map[string]any{}, false, false, WaitOptions{}, options); err != nil {
		t.Fatalf("MCPServerTest() error = %v", err)
	}
	want := []string{
		"GET /api/v1/system/context", "GET /api/v1/system/capabilities", "GET /api/v1/plugins/author/name", "POST /api/v1/plugins/author/name/upgrade",
		"GET /api/v1/system/context", "GET /api/v1/system/capabilities", "GET /api/v1/mcp/servers/saved", "POST /api/v1/mcp/servers/saved/test",
	}
	if fmt.Sprint(calls) != fmt.Sprint(want) {
		t.Fatalf("calls = %#v", calls)
	}
}

func TestTransientMCPDryRunDoesNotRequireSavedServer(t *testing.T) {
	service, options := extensionService(t, taskRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		switch request.URL.Path {
		case "/api/v1/system/context":
			return extensionIdentityResponse(), nil
		case "/api/v1/system/capabilities":
			return extensionCapabilitiesResponse("mcp_server.test"), nil
		default:
			return nil, fmt.Errorf("unexpected request %s %s", request.Method, request.URL.Path)
		}
	}))
	value, err := service.MCPServerTest(context.Background(), "_", map[string]any{"name": "temporary"}, true, false, WaitOptions{}, options)
	if err != nil {
		t.Fatalf("MCPServerTest() error = %v", err)
	}
	data := value.Data.(map[string]any)
	if data["server_write"] != false || data["business_validation"] != "not_run" {
		t.Fatalf("data = %#v", data)
	}
}

func TestExtensionUploadSizeIsRejectedBeforeNetwork(t *testing.T) {
	filename := filepath.Join(t.TempDir(), "large.lbpkg")
	file, err := os.Create(filename)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(maxExtensionUploadBytes + 1); err != nil {
		file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	service := New(Dependencies{Transport: taskRoundTripFunc(func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		return nil, errors.New("unexpected network request")
	})})
	_, err = service.PluginInstallLocal(context.Background(), filename, false, false, WaitOptions{}, CheckOptions{})
	if failure := result.AsError(err); failure.Kind != "input" {
		t.Fatalf("error = %+v", failure)
	}
	if calls.Load() != 0 {
		t.Fatalf("network calls = %d", calls.Load())
	}
}

func TestSkillDirectoryRejectsInvalidFilesBeforeNetwork(t *testing.T) {
	for _, test := range []struct {
		name  string
		setup func(*testing.T, string) string
	}{
		{"empty", func(t *testing.T, directory string) string { return directory }},
		{"git only", func(t *testing.T, directory string) string {
			if err := os.Mkdir(filepath.Join(directory, ".git"), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(directory, ".git", "config"), []byte("private"), 0o600); err != nil {
				t.Fatal(err)
			}
			return directory
		}},
		{"linked directory", func(t *testing.T, directory string) string {
			link := filepath.Join(t.TempDir(), "linked")
			if err := os.Symlink(directory, link); err != nil {
				t.Skipf("symlinks unavailable: %v", err)
			}
			return link
		}},
		{"linked file", func(t *testing.T, directory string) string {
			target := filepath.Join(t.TempDir(), "outside.md")
			if err := os.WriteFile(target, []byte("outside"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, filepath.Join(directory, "SKILL.md")); err != nil {
				t.Skipf("symlinks unavailable: %v", err)
			}
			return directory
		}},
		{"oversized archive", func(t *testing.T, directory string) string {
			file, err := os.Create(filepath.Join(directory, "random.bin"))
			if err != nil {
				t.Fatal(err)
			}
			defer file.Close()
			if _, err := io.CopyN(file, rand.Reader, maxExtensionUploadBytes+64*1024); err != nil {
				t.Fatal(err)
			}
			if err := file.Close(); err != nil {
				t.Fatal(err)
			}
			return directory
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			directory := test.setup(t, t.TempDir())
			packages := t.TempDir()
			for _, key := range []string{"TMPDIR", "TEMP", "TMP"} {
				t.Setenv(key, packages)
			}
			var calls atomic.Int32
			service := New(Dependencies{Transport: taskRoundTripFunc(func(*http.Request) (*http.Response, error) {
				calls.Add(1)
				return nil, errors.New("unexpected network request")
			})})
			_, err := service.SkillInstallDirectory(context.Background(), directory, nil, false, CheckOptions{})
			if err == nil || result.AsError(err).Kind != "input" {
				t.Fatalf("invalid directory error = %v", err)
			}
			if calls.Load() != 0 {
				t.Fatalf("network calls = %d", calls.Load())
			}
			entries, err := os.ReadDir(packages)
			if err != nil || len(entries) != 0 {
				t.Fatalf("temporary packages leaked: %v, error = %v", entries, err)
			}
		})
	}
}

func extensionService(t *testing.T, transport http.RoundTripper) (*Service, CheckOptions) {
	t.Helper()
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	service := New(Dependencies{
		LookupEnv:         func(name string) (string, bool) { return "secret", name == "KEY" },
		DefaultConfigPath: func() (string, error) { return configPath, nil },
		Transport:         transport,
	})
	if _, err := service.ContextAdd("production", "http://example.test", "KEY", "workspace-a", ""); err != nil {
		t.Fatalf("ContextAdd() error = %v", err)
	}
	return service, CheckOptions{ContextSet: true, Context: "production"}
}

func extensionIdentityResponse() *http.Response {
	return taskResponse(`{"code":0,"data":{"instance_uuid":"instance-a","workspace_uuid":"workspace-a","api_key_id":"key-a","permissions":["resource.manage","resource.view"]}}`)
}

func extensionCapabilitiesResponse(operations ...string) *http.Response {
	values := ""
	for index, operation := range operations {
		if index != 0 {
			values += ","
		}
		values += fmt.Sprintf("%q:{\"supported\":true}", operation)
	}
	return taskResponse(fmt.Sprintf(`{"code":0,"data":{"schema_version":1,"operations":{%s}}}`, values))
}

func TestPluginUpgradeWaitReadsInstalledRuntimeAndGrantsOnlyOnRequest(t *testing.T) {
	const ready = `"version":"2","enabled":true,"status":"initialized","components":[]`
	const pending = `"version":"2","enabled":false,"status":"pending_confirmation","permission_status":{"pending_confirmation":true},"components":[]`
	const mounted = `"version":"2","enabled":true,"status":"mounted","components":[]`
	for _, tc := range []struct {
		name                  string
		states                []string
		confirm, lostAck      bool
		want                  string
		wantReads, wantGrants int
	}{
		{"ready", []string{ready}, false, false, "ready", 2, 0},
		{"already current", []string{`"version":"1","enabled":true,"status":"initialized","components":[]`}, false, false, "ready", 2, 0},
		{"initializing", []string{mounted, ready}, false, false, "ready", 3, 0},
		{"disabled", []string{`"version":"2","status":"initialized","enabled":false,"components":[]`}, false, false, "unknown", 2, 0},
		{"unverified components", []string{`"version":"2","status":"initialized","enabled":true`}, false, false, "unknown", 2, 0},
		{"wrong running version", []string{ready + `,"install_info":{"plugin_version":"3"}`}, false, false, "unknown", 2, 0},
		{"pending", []string{pending}, false, false, "pending_confirmation", 2, 0},
		{"explicit permission grant", []string{pending, ready}, true, false, "ready", 3, 1},
		{"approval response lost", []string{pending, ready}, true, true, "unknown", 3, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reads, upgrades, grants, manifests := 0, 0, 0, 0
			service, options := extensionService(t, taskRoundTripFunc(func(r *http.Request) (*http.Response, error) {
				switch r.URL.Path {
				case "/api/v1/system/context":
					return extensionIdentityResponse(), nil
				case "/api/v1/system/capabilities":
					return extensionCapabilitiesResponse("plugin.upgrade", "plugin.get", "task.get", "operation.invoke", "plugin.permissions.confirm_api_key"), nil
				case "/api/v1/plugins/acme/echo":
					state := `"version":"1"`
					if reads > 0 {
						if reads > len(tc.states) {
							t.Fatalf("unexpected extra runtime read %d", reads)
						}
						state = tc.states[reads-1]
					}
					reads++
					return taskResponse(`{"code":0,"data":{"plugin":{"author":"acme","name":"echo",` + state + `}}}`), nil
				case "/api/v1/plugins/acme/echo/upgrade":
					upgrades++
					return taskResponse(`{"code":0,"data":{"task_id":42}}`), nil
				case "/api/v1/system/tasks/42":
					return taskResponse(`{"code":0,"data":{"id":42,"task_type":"user","kind":"plugin-operation","status":"succeeded","error":null,"result":null,"created_at":1}}`), nil
				case "/api/v1/operations/plugin.permissions.get":
					manifests++
					return taskResponse(`{"code":0,"data":{"permissions":[{"category":"capabilities","id":"example.read"}]}}`), nil
				case "/api/v1/operations/plugin.permissions.update":
					grants++
					payload, _ := io.ReadAll(r.Body)
					if !strings.Contains(string(payload), `"capabilities":["example.read"]`) {
						t.Fatalf("confirmation body %s", payload)
					}
					if tc.lostAck {
						return nil, errors.New("lost acknowledgment")
					}
					return taskResponse(`{"code":0,"data":{"enabled":true,"pending_confirmation":false}}`), nil
				default:
					t.Fatalf("unexpected URL %s", r.URL.Path)
					return nil, nil
				}
			}))
			out, err := service.PluginUpgrade(context.Background(), "acme", "echo", false, !tc.confirm, WaitOptions{ConfirmPermissions: tc.confirm, PollInterval: time.Millisecond, WaitTimeout: time.Second}, options)
			data, ok := out.Data.(map[string]any)
			if !ok || data["readiness"] != tc.want || data["step"] != "completed" || (err == nil) != (tc.want == "ready") || reads != tc.wantReads || upgrades != 1 || grants != tc.wantGrants {
				t.Fatalf("got %#v err %v; reads/upgrades/grants %d/%d/%d", out, err, reads, upgrades, grants)
			}
			if (tc.states[0] == pending) != (manifests == 1) {
				t.Fatalf("manifest reads %d", manifests)
			}
			if tc.want == "ready" && data["version_changed"] != (tc.name != "already current") {
				t.Fatalf("version evidence %#v", data)
			}
		})
	}
}
