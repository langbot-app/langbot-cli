package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/langbot-app/langbot-cli/internal/result"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}

type blockingBody struct {
	ctx context.Context
}

func (body blockingBody) Read(_ []byte) (int, error) {
	<-body.ctx.Done()
	return 0, body.ctx.Err()
}

func (blockingBody) Close() error { return nil }

func TestInfoKeepsTargetsAndCredentialsSeparate(t *testing.T) {
	type request struct {
		path string
		key  string
	}
	var mu sync.Mutex
	seen := make([]request, 0, 2)
	newServer := func(expectedKey, version string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			seen = append(seen, request{path: r.URL.Path, key: r.Header.Get("X-API-Key")})
			mu.Unlock()
			if r.Header.Get("X-API-Key") != expectedKey {
				t.Error("请求携带了错误的 API Key")
			}
			_, _ = fmt.Fprintf(w, `{"code":0,"msg":"ok","data":{"version":%q,"edition":"community"}}`, version)
		}))
	}
	first := newServer("key-a", "a")
	second := newServer("key-b", "b")
	defer first.Close()
	defer second.Close()

	client := Client{}
	var wg sync.WaitGroup
	results := make(chan Info, 2)
	for _, target := range []Target{{Endpoint: first.URL, APIKey: "key-a"}, {Endpoint: second.URL, APIKey: "key-b"}} {
		wg.Add(1)
		go func(target Target) {
			defer wg.Done()
			info, err := client.Info(context.Background(), target)
			if err != nil {
				t.Errorf("Info() error = %v", err)
				return
			}
			results <- info
		}(target)
	}
	wg.Wait()
	close(results)
	got := map[string]bool{}
	for info := range results {
		got[info.Version] = true
	}
	if !got["a"] || !got["b"] {
		t.Fatalf("未收到两个目标的独立响应: %#v", got)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(seen) != 2 {
		t.Fatalf("请求数 = %d, want 2", len(seen))
	}
	for _, item := range seen {
		if item.path != infoPath {
			t.Errorf("请求路径 = %q, want %q", item.path, infoPath)
		}
	}
}

func TestInfoPreservesReverseProxyPrefix(t *testing.T) {
	var gotPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_, _ = w.Write([]byte(`{"code":0,"msg":"ok","data":{"version":"v","edition":"e"}}`))
	}))
	defer server.Close()

	_, err := (Client{}).Info(context.Background(), Target{Endpoint: server.URL + "/langbot/", APIKey: "key"})
	if err != nil {
		t.Fatalf("Info() error = %v", err)
	}
	if gotPath != "/langbot/api/v1/system/info" {
		t.Fatalf("请求路径 = %q, want proxy prefix", gotPath)
	}
}

func TestInfoSupportsConfiguredTLSTransport(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-API-Key") != "secret" {
			t.Error("TLS 请求没有使用配置的 API Key")
		}
		_, _ = w.Write([]byte(`{"code":0,"data":{"version":"v","edition":"e"}}`))
	}))
	defer server.Close()

	info, err := (Client{Transport: server.Client().Transport}).Info(
		context.Background(), Target{Endpoint: server.URL, APIKey: "secret"},
	)
	if err != nil {
		t.Fatalf("TLS Info() error = %v", err)
	}
	if info.Version != "v" || info.Edition != "e" {
		t.Fatalf("TLS Info() = %#v", info)
	}
}

func TestInfoHidesTLSVerificationDetails(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"code":0,"data":{"version":"v","edition":"e"}}`))
	}))
	defer server.Close()

	_, err := (Client{}).Info(context.Background(), Target{Endpoint: server.URL, APIKey: "secret"})
	got := result.AsError(err)
	if got.Kind != "network" || got.Type != "tls_error" || got.Message != "TLS 证书验证失败" {
		t.Fatalf("TLS error = %+v, want fixed tls_error", got)
	}
	if strings.Contains(got.Message, "x509") || strings.Contains(got.Message, "secret") {
		t.Fatalf("TLS error contains implementation detail: %q", got.Message)
	}
}

func TestInfoHTTPAndBusinessErrors(t *testing.T) {
	tests := []struct {
		name       string
		status     int
		body       string
		wantKind   string
		wantStatus int
	}{
		{name: "business int", status: http.StatusOK, body: `{"code":400,"msg":"bad"}`, wantKind: "server", wantStatus: http.StatusOK},
		{name: "business string", status: http.StatusOK, body: `{"code":"denied","msg":"bad"}`, wantKind: "server", wantStatus: http.StatusOK},
		{name: "unauthorized", status: http.StatusUnauthorized, body: `not json`, wantKind: "auth", wantStatus: http.StatusUnauthorized},
		{name: "forbidden", status: http.StatusForbidden, body: `{"code":"nope"}`, wantKind: "permission", wantStatus: http.StatusForbidden},
		{name: "not found", status: http.StatusNotFound, body: `{"code":"missing"}`, wantKind: "not_found", wantStatus: http.StatusNotFound},
		{name: "non json success", status: http.StatusOK, body: `<html>secret</html>`, wantKind: "incompatible", wantStatus: http.StatusOK},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte(tt.body))
			}))
			defer server.Close()
			_, err := (Client{}).Info(context.Background(), Target{Endpoint: server.URL, APIKey: "secret"})
			if err == nil {
				t.Fatal("Info() error = nil")
			}
			got := result.AsError(err)
			if got.Kind != tt.wantKind || got.HTTPStatus != tt.wantStatus {
				t.Fatalf("error = %+v, want kind=%q status=%d", got, tt.wantKind, tt.wantStatus)
			}
		})
	}
}

func TestInfoRejectsNonStrictOrIncompletePayload(t *testing.T) {
	for _, body := range []string{
		`{"code":0,"data":{}}`,
		`{"code":0,"data":null}`,
		`{"code":0,"data":{"version":123}}`,
		`{"code":0,"data":{"version":"v"}} trailing`,
	} {
		t.Run(body, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write([]byte(body))
			}))
			defer server.Close()
			_, err := (Client{}).Info(context.Background(), Target{Endpoint: server.URL})
			if result.AsError(err).Kind != "incompatible" {
				t.Fatalf("Info() error = %+v, want incompatible", result.AsError(err))
			}
		})
	}
}

func TestInfoTimeoutAndCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(200 * time.Millisecond):
			_, _ = w.Write([]byte(`{"code":0,"data":{}}`))
		case <-r.Context().Done():
		}
	}))
	defer server.Close()

	_, err := (Client{}).Info(context.Background(), Target{Endpoint: server.URL, Timeout: 10 * time.Millisecond})
	if result.AsError(err).Kind != "network" {
		t.Fatalf("timeout error = %+v, want network", result.AsError(err))
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = (Client{}).Info(ctx, Target{Endpoint: server.URL, Timeout: time.Second})
	if result.AsError(err).Kind != "network" {
		t.Fatalf("cancel error = %+v, want network", result.AsError(err))
	}
}

func TestInfoBodyReadTimeoutIsNetworkError(t *testing.T) {
	client := Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(blockingBody{ctx: request.Context()}),
			Request:    request,
		}, nil
	})}
	_, err := client.Info(context.Background(), Target{Endpoint: "http://example.test", Timeout: 10 * time.Millisecond})
	got := result.AsError(err)
	if got.Kind != "network" || got.HTTPStatus != http.StatusOK || got.Message != "请求超时" {
		t.Fatalf("body read timeout error = %+v, want network/200/请求超时", got)
	}
}

func TestRawBlocksUnknownAndWriteOperations(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_, _ = w.Write([]byte(`{"code":0,"data":{}}`))
	}))
	defer server.Close()

	client := Client{}
	for _, tc := range []struct{ method, path string }{
		{http.MethodPost, infoPath},
		{http.MethodGet, "/api/v1/unknown"},
		{http.MethodGet, infoPath + "?unsafe=true"},
	} {
		if _, err := client.Raw(context.Background(), Target{Endpoint: server.URL, APIKey: "secret"}, tc.method, tc.path); result.AsError(err).Kind != "incompatible" {
			t.Errorf("Raw(%q, %q) error = %+v, want incompatible", tc.method, tc.path, result.AsError(err))
		}
	}
	if calls.Load() != 0 {
		t.Fatalf("非法操作触发了 %d 个请求", calls.Load())
	}
}

func TestContextUsesAPIKeyAndNormalizesPermissions(t *testing.T) {
	var gotPath string
	var gotKey string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotKey = r.Header.Get("X-API-Key")
		_, _ = w.Write([]byte(`{"code":0,"msg":"ok","data":{"instance_uuid":"instance-a","workspace_uuid":"workspace-a","api_key_id":"key-id-a","permissions":["write","read","write"]}}`))
	}))
	defer server.Close()

	identity, err := (Client{}).Context(context.Background(), Target{
		Endpoint: server.URL + "/proxy/", APIKey: "secret-key",
	})
	if err != nil {
		t.Fatalf("Context() error = %v", err)
	}
	if gotPath != "/proxy/api/v1/system/context" || gotKey != "secret-key" {
		t.Fatalf("request = path %q key %q", gotPath, gotKey)
	}
	if identity.InstanceUUID != "instance-a" || identity.WorkspaceUUID != "workspace-a" || identity.APIKeyID != "key-id-a" {
		t.Fatalf("identity = %#v", identity)
	}
	if got := strings.Join(identity.Permissions, ","); got != "read,write" {
		t.Fatalf("permissions = %q, want sorted unique values", got)
	}
}

func TestContextRejectsIncompleteOrWronglyTypedPayloads(t *testing.T) {
	bodies := []string{
		`{"code":0,"data":{}}`,
		`{"code":0,"data":{"instance_uuid":"i","workspace_uuid":"w","api_key_id":"k","permissions":"read"}}`,
		`{"code":0,"data":{"instance_uuid":"i","workspace_uuid":"w","api_key_id":"k","permissions":["read",1]}}`,
		`{"code":0,"data":{"instance_uuid":"","workspace_uuid":"w","api_key_id":"k","permissions":[]}}`,
	}
	for _, body := range bodies {
		t.Run(body, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(body))
			}))
			defer server.Close()
			_, err := (Client{}).Context(context.Background(), Target{Endpoint: server.URL})
			if result.AsError(err).Kind != "incompatible" {
				t.Fatalf("Context() error = %+v, want incompatible", result.AsError(err))
			}
		})
	}
}

func TestCapabilitiesUsesPrefixAndKeepsMissingOperationsUnknown(t *testing.T) {
	secret := "capability-key"
	var gotPath, gotKey string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotKey = r.URL.Path, r.Header.Get("X-API-Key")
		_, _ = fmt.Fprintf(w, `{"code":0,"msg":%q,"data":{"schema_version":1,"operations":{"bot.list":{"supported":true},"pipeline.list":{"supported":false},"ignored-secret-operation":{"supported":true,"secret":%q}}}}`, secret, secret)
	}))
	defer server.Close()

	capabilities, err := (Client{}).Capabilities(context.Background(), Target{Endpoint: server.URL + "/proxy", APIKey: secret})
	if err != nil {
		t.Fatalf("Capabilities() error = %v", err)
	}
	if gotPath != "/proxy/api/v1/system/capabilities" || gotKey != secret {
		t.Fatalf("request = path %q key %q", gotPath, gotKey)
	}
	if capabilities.SchemaVersion != 1 || !capabilities.Operations["bot.list"] || capabilities.Operations["pipeline.list"] {
		t.Fatalf("capabilities = %#v", capabilities)
	}
	if _, ok := capabilities.Operations["bot.get"]; ok {
		t.Fatal("missing operation was incorrectly inferred")
	}
	if _, ok := capabilities.Operations["ignored-secret-operation"]; ok {
		t.Fatal("unknown operation was exposed")
	}
}

func TestTaskAndKnowledgeBaseClientMethods(t *testing.T) {
	var paths []string
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		paths = append(paths, request.URL.Path)
		var body string
		switch request.URL.Path {
		case "/api/v1/system/tasks/17":
			body = `{"code":0,"data":{"id":17,"task_type":"user","kind":"knowledge_base.store","status":"running","error":null,"result":null,"created_at":1.0}}`
		case "/api/v1/knowledge/bases/kb-a":
			body = `{"code":0,"data":{"base":{"uuid":"kb-a","name":"KB","engine_plugin_id":"engine"}}}`
		case "/api/v1/knowledge/bases/kb-a/files":
			body = `{"code":0,"data":{"task_id":18}}`
		case "/api/v1/files/documents":
			if !strings.HasPrefix(request.Header.Get("Content-Type"), "multipart/form-data;") {
				t.Fatalf("upload content type = %q", request.Header.Get("Content-Type"))
			}
			body = `{"code":0,"data":{"file_id":"file-a"}}`
		default:
			return nil, fmt.Errorf("unexpected path %s", request.URL.Path)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(body)),
		}, nil
	})
	client := Client{Transport: transport}
	target := Target{Endpoint: "http://example.test", APIKey: "key"}
	task, err := client.Task(context.Background(), target, "17")
	if err != nil || task.ID.String() != "17" || task.Status != "running" || task.CreatedAt.String() != "1.0" {
		t.Fatalf("Task() = %#v, error = %v", task, err)
	}
	base, err := client.KnowledgeBase(context.Background(), target, "kb-a")
	if err != nil || base["uuid"] != "kb-a" {
		t.Fatalf("KnowledgeBase() = %#v, error = %v", base, err)
	}
	taskID, err := client.KnowledgeBaseStoreFile(context.Background(), target, "kb-a", "file-a", "")
	if err != nil || taskID != "18" {
		t.Fatalf("KnowledgeBaseStoreFile() = %q, error = %v", taskID, err)
	}
	fileID, err := client.UploadDocument(context.Background(), target, "sample.txt", strings.NewReader("hello"))
	if err != nil || fileID != "file-a" {
		t.Fatalf("UploadDocument() = %q, error = %v", fileID, err)
	}
	want := []string{
		"/api/v1/system/tasks/17",
		"/api/v1/knowledge/bases/kb-a",
		"/api/v1/knowledge/bases/kb-a/files",
		"/api/v1/files/documents",
	}
	if !reflect.DeepEqual(paths, want) {
		t.Fatalf("paths = %#v, want %#v", paths, want)
	}
}

func TestExtensionClientMethodsKeepRoutesAndMultipartFields(t *testing.T) {
	var paths []string
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		paths = append(paths, request.Method+" "+request.URL.EscapedPath())
		var body string
		switch request.URL.Path {
		case "/api/v1/plugins/install/github", "/api/v1/plugins/install/marketplace", "/api/v1/plugins/install/local", "/api/v1/plugins/a/name/upgrade", "/api/v1/mcp/servers/server-a/test", "/api/v1/mcp/servers/server/name/test":
			body = `{"code":0,"data":{"task_id":19}}`
		case "/api/v1/plugins/a/name":
			body = `{"code":0,"data":{"plugin":{"uuid":"plugin-a","author":"a","name":"name","version":"1.0","config":{"token":"secret"}}}}`
		case "/api/v1/skills/install/github", "/api/v1/skills/install/github/preview":
			body = `{"code":0,"data":{"skills":[{"name":"demo","instructions":"safe"}]}}`
		case "/api/v1/skills/install/upload", "/api/v1/skills/install/upload/preview":
			if err := request.ParseMultipartForm(1 << 20); err != nil {
				return nil, fmt.Errorf("parse multipart: %w", err)
			}
			if got := request.MultipartForm.Value["source_paths"]; !reflect.DeepEqual(got, []string{"one", "two"}) {
				return nil, fmt.Errorf("source_paths = %#v", got)
			}
			body = `{"code":0,"data":{"skills":[{"name":"demo"}]}}`
		case "/api/v1/mcp/servers/server-a":
			body = `{"code":0,"data":{"server":{"uuid":"server-a-id","name":"server-a","config":{"token":"secret"}}}}`
		default:
			return nil, fmt.Errorf("unexpected path %s", request.URL.Path)
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
	})
	client := Client{Transport: transport}
	target := Target{Endpoint: "http://example.test", APIKey: "key"}
	if id, err := client.PluginInstallGitHub(context.Background(), target, map[string]any{"owner": "a"}); err != nil || id != "19" {
		t.Fatalf("PluginInstallGitHub() = %q, %v", id, err)
	}
	if id, err := client.PluginInstallMarketplace(context.Background(), target, map[string]any{}); err != nil || id != "19" {
		t.Fatalf("PluginInstallMarketplace() = %q, %v", id, err)
	}
	if id, err := client.PluginInstallLocal(context.Background(), target, "plugin.lbpkg", strings.NewReader("package")); err != nil || id != "19" {
		t.Fatalf("PluginInstallLocal() = %q, %v", id, err)
	}
	if _, err := client.PluginGet(context.Background(), target, "a", "name"); err != nil {
		t.Fatalf("PluginGet() error = %v", err)
	}
	if id, err := client.PluginUpgrade(context.Background(), target, "a", "name"); err != nil || id != "19" {
		t.Fatalf("PluginUpgrade() = %q, %v", id, err)
	}
	if _, err := client.SkillPreviewGitHub(context.Background(), target, map[string]any{}); err != nil {
		t.Fatalf("SkillPreviewGitHub() error = %v", err)
	}
	if _, err := client.SkillInstallGitHub(context.Background(), target, map[string]any{}); err != nil {
		t.Fatalf("SkillInstallGitHub() error = %v", err)
	}
	if _, err := client.SkillInstallUpload(context.Background(), target, "skill.zip", strings.NewReader("zip"), []string{"one", "two"}); err != nil {
		t.Fatalf("SkillInstallUpload() error = %v", err)
	}
	if _, err := client.SkillPreviewUpload(context.Background(), target, "skill.zip", strings.NewReader("zip"), []string{"one", "two"}); err != nil {
		t.Fatalf("SkillPreviewUpload() error = %v", err)
	}
	if _, err := client.MCPServerGet(context.Background(), target, "server-a"); err != nil {
		t.Fatalf("MCPServerGet() error = %v", err)
	}
	if id, err := client.MCPServerTest(context.Background(), target, "server-a", map[string]any{}); err != nil || id != "19" {
		t.Fatalf("MCPServerTest() = %q, %v", id, err)
	}
	if id, err := client.MCPServerTest(context.Background(), target, "server/name", map[string]any{}); err != nil || id != "19" {
		t.Fatalf("MCPServerTest(encoded name) = %q, %v", id, err)
	}
	if len(paths) != 12 || paths[len(paths)-1] != "POST /api/v1/mcp/servers/server%2Fname/test" {
		t.Fatalf("paths = %#v", paths)
	}
}

func TestSkillPreviewFailureIsNotClassifiedAsUnknownWrite(t *testing.T) {
	client := Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("connection reset")
	})}
	target := Target{Endpoint: "http://example.test", APIKey: "key"}
	_, previewErr := client.SkillPreviewGitHub(context.Background(), target, map[string]any{})
	if failure := result.AsError(previewErr); failure.Type == "result_unknown" || failure.Kind != "network" {
		t.Fatalf("preview error = %+v", failure)
	}
	_, installErr := client.SkillInstallGitHub(context.Background(), target, map[string]any{})
	if failure := result.AsError(installErr); failure.Type != "result_unknown" {
		t.Fatalf("install error = %+v", failure)
	}
}

func TestConfirmedWriteRejectionIsNotClassifiedAsUnknown(t *testing.T) {
	client := Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusTooManyRequests,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"code":"rate_limited","data":null}`)),
		}, nil
	})}
	_, err := client.PluginInstallMarketplace(context.Background(), Target{Endpoint: "http://example.test"}, map[string]any{})
	if failure := result.AsError(err); failure.Type == "result_unknown" || failure.HTTPStatus != http.StatusTooManyRequests {
		t.Fatalf("error = %+v", failure)
	}
}

func TestExtensionClientRejectsPathInjection(t *testing.T) {
	client := Client{}
	target := Target{Endpoint: "http://example.test"}
	for _, test := range []struct {
		name string
		call func() error
	}{
		{name: "plugin author", call: func() error {
			_, err := client.PluginUpgrade(context.Background(), target, "../owner", "plugin")
			return err
		}},
		{name: "plugin name", call: func() error {
			_, err := client.PluginUpgrade(context.Background(), target, "owner", "../plugin")
			return err
		}},
		{name: "mcp name", call: func() error {
			_, err := client.MCPServerTest(context.Background(), target, "server/../name", nil)
			return err
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if result.AsError(test.call()).Kind != "input" {
				t.Fatalf("path injection was not rejected")
			}
		})
	}
}

func TestTaskRejectsUnstableStatusAndIdentifier(t *testing.T) {
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body: io.NopCloser(strings.NewReader(
				`{"code":0,"data":{"id":17,"status":"queued"}}`,
			)),
		}, nil
	})
	_, err := (Client{Transport: transport}).Task(context.Background(), Target{Endpoint: "http://example.test"}, "17")
	if result.AsError(err).Kind != "incompatible" {
		t.Fatalf("Task() error = %+v, want incompatible", result.AsError(err))
	}
	for _, identifier := range []string{"../17", "-1", "+1", "01", "task"} {
		if _, err := (Client{}).Task(context.Background(), Target{Endpoint: "http://example.test"}, identifier); result.AsError(err).Kind != "input" {
			t.Fatalf("task ID %q error = %+v, want input", identifier, result.AsError(err))
		}
	}
}

func TestTaskRejectsInconsistentTerminalData(t *testing.T) {
	for _, body := range []string{
		`{"code":0,"data":{"id":17,"task_type":"user","kind":"knowledge-operation","status":"failed","error":null,"result":null,"created_at":1}}`,
		`{"code":0,"data":{"id":17,"task_type":"user","kind":"knowledge-operation","status":"running","error":{"type":"task_failed","message":"failed"},"result":null,"created_at":1}}`,
		`{"code":0,"data":{"id":17,"task_type":"user","kind":"knowledge-operation","status":"cancelled","error":{"type":"task_cancelled","message":"cancelled"},"result":{"unexpected":true},"created_at":1}}`,
	} {
		transport := roundTripFunc(func(*http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(body)),
			}, nil
		})
		_, err := (Client{Transport: transport}).Task(context.Background(), Target{Endpoint: "http://example.test"}, "17")
		if result.AsError(err).Kind != "incompatible" {
			t.Fatalf("Task() error = %+v, want incompatible for %s", result.AsError(err), body)
		}
	}
}

func TestCapabilitiesIgnoresUnknownOperationShapes(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"code":0,"data":{"schema_version":1,"operations":{"bot.get":{"supported":true},"future.operation":"new-shape"}}}`)
	}))
	defer server.Close()
	capabilities, err := (Client{}).Capabilities(context.Background(), Target{Endpoint: server.URL})
	if err != nil || !capabilities.Operations["bot.get"] {
		t.Fatalf("Capabilities() = %#v, error = %v", capabilities, err)
	}
}

func TestCapabilitiesRejectsUnsupportedSchemaAndMalformedOperations(t *testing.T) {
	for _, body := range []string{
		`{"code":0,"data":{"schema_version":2,"operations":{}}}`,
		`{"code":0,"data":{"schema_version":1,"operations":[]}}`,
		`{"code":0,"data":{"schema_version":1,"operations":{"bot.list":{"supported":"yes"}}}}`,
		`{"code":0,"data":{"schema_version":1,"operations":{"bot.list":{}}}}`,
	} {
		t.Run(body, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(body))
			}))
			defer server.Close()
			_, err := (Client{}).Capabilities(context.Background(), Target{Endpoint: server.URL, APIKey: "capability-secret"})
			if result.AsError(err).Kind != "incompatible" {
				t.Fatalf("Capabilities() error = %+v, want incompatible", result.AsError(err))
			}
		})
	}
}

func TestCapabilitiesRejectsOversizedResponse(t *testing.T) {
	client := Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(strings.Repeat("x", maxResponseBytes+1))),
			Request:    request,
		}, nil
	})}
	_, err := client.Capabilities(context.Background(), Target{Endpoint: "http://example.test", APIKey: "capability-secret"})
	if result.AsError(err).Kind != "incompatible" {
		t.Fatalf("oversized capabilities error = %+v, want incompatible", result.AsError(err))
	}
}

func TestContextAuthenticationErrorsAndSecretRedaction(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			secret := "context-secret-value"
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("X-API-Key") != secret {
					t.Fatalf("API Key header = %q", r.Header.Get("X-API-Key"))
				}
				w.Header().Set("X-Request-Id", secret+"-request")
				w.WriteHeader(status)
				_, _ = fmt.Fprintf(w, `{"code":%d,"msg":%q}`, status, secret)
			}))
			defer server.Close()
			_, err := (Client{}).Context(context.Background(), Target{Endpoint: server.URL, APIKey: secret})
			got := result.AsError(err)
			wantKind := "auth"
			if status == http.StatusForbidden {
				wantKind = "permission"
			}
			if got.Kind != wantKind || got.HTTPStatus != status {
				t.Fatalf("error = %+v, want kind=%q status=%d", got, wantKind, status)
			}
			if strings.Contains(fmt.Sprintf("%+v", got), secret) {
				t.Fatalf("error exposed API Key: %+v", got)
			}
		})
	}
}

func TestResourceReadsKeepPrefixAndProjectSensitiveFields(t *testing.T) {
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		switch r.URL.Path {
		case "/proxy/api/v1/platform/bots":
			_, _ = w.Write([]byte(`{"code":0,"data":{"bots":[{"uuid":"bot-a","name":"resource-key","description":"desc","adapter":"http_bot","enable":true,"adapter_config":{"token":"bot-secret"},"created_at":"2026-01-01T00:00:00","unknown":{"secret":"hidden"}}]}}`))
		case "/proxy/api/v1/platform/bots/bot-a":
			_, _ = w.Write([]byte(`{"code":0,"data":{"bot":{"uuid":"bot-a","name":"Bot A","adapter":"http_bot","config":{"api_key":"bot-secret"}}}}`))
		case "/proxy/api/v1/pipelines":
			_, _ = w.Write([]byte(`{"code":0,"data":{"pipelines":[{"uuid":"pipeline-a","name":"Pipeline A","description":"desc","is_default":false,"config":{"provider":{"secret":"provider-secret","token":"provider-token","key":"provider-key","url":"https://provider.example/key"}},"created_at":"2026-01-01T00:00:00"}]}}`))
		case "/proxy/api/v1/pipelines/pipeline-a":
			_, _ = w.Write([]byte(`{"code":0,"data":{"pipeline":{"uuid":"pipeline-a","name":"Pipeline A","config":{"provider":{"secret":"pipeline-secret","token":"pipeline-token","key":"pipeline-key","url":"https://pipeline.example/key"}}}}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	target := Target{Endpoint: server.URL + "/proxy", APIKey: "resource-key"}

	bots, err := (Client{}).Bots(context.Background(), target)
	if err != nil || len(bots) != 1 || bots[0]["uuid"] != "bot-a" {
		t.Fatalf("Bots() = %#v, err=%v", bots, err)
	}
	if _, ok := bots[0]["adapter_config"]; ok {
		t.Fatal("Bot projection exposed adapter_config")
	}
	if _, ok := bots[0]["name"]; ok {
		t.Fatal("Bot projection exposed a safe field containing the API Key")
	}
	bot, err := (Client{}).Bot(context.Background(), target, "bot-a")
	if err != nil || bot["uuid"] != "bot-a" {
		t.Fatalf("Bot() = %#v, err=%v", bot, err)
	}
	if _, ok := bot["config"]; ok {
		t.Fatal("Bot projection exposed config")
	}
	pipelines, err := (Client{}).Pipelines(context.Background(), target)
	if err != nil || len(pipelines) != 1 || pipelines[0]["uuid"] != "pipeline-a" {
		t.Fatalf("Pipelines() = %#v, err=%v", pipelines, err)
	}
	if _, ok := pipelines[0]["config"]; ok {
		t.Fatal("Pipeline projection exposed config")
	}
	for _, secret := range []string{"provider-secret", "provider-token", "provider-key", "https://provider.example/key"} {
		if strings.Contains(fmt.Sprint(pipelines), secret) {
			t.Fatalf("Pipeline projection exposed %q", secret)
		}
	}
	pipeline, err := (Client{}).Pipeline(context.Background(), target, "pipeline-a")
	if err != nil || pipeline["uuid"] != "pipeline-a" {
		t.Fatalf("Pipeline() = %#v, err=%v", pipeline, err)
	}
	if _, ok := pipeline["config"]; ok {
		t.Fatal("Pipeline projection exposed config")
	}
	for _, secret := range []string{"pipeline-secret", "pipeline-token", "pipeline-key", "https://pipeline.example/key"} {
		if strings.Contains(fmt.Sprint(pipeline), secret) {
			t.Fatalf("Pipeline projection exposed %q", secret)
		}
	}
	wantPaths := []string{
		"/proxy/api/v1/platform/bots",
		"/proxy/api/v1/platform/bots/bot-a",
		"/proxy/api/v1/pipelines",
		"/proxy/api/v1/pipelines/pipeline-a",
	}
	if strings.Join(paths, "|") != strings.Join(wantPaths, "|") {
		t.Fatalf("resource paths = %#v, want %#v", paths, wantPaths)
	}
}

func TestResourceResponsesRequireUUIDAndMatchingRequestedID(t *testing.T) {
	secret := "resource-secret"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/platform/bots":
			_, _ = fmt.Fprintf(w, `{"code":0,"msg":%q,"data":{"bots":[{"name":"Bot"}]}}`, secret)
		case "/api/v1/platform/bots/requested":
			_, _ = fmt.Fprintf(w, `{"code":0,"msg":%q,"data":{"bot":{"uuid":"other","name":"Bot","config":{"token":%q}}}}`, secret, secret)
		case "/api/v1/pipelines/requested":
			_, _ = fmt.Fprintf(w, `{"code":0,"msg":%q,"data":{"pipeline":{"uuid":"other","name":"Pipeline","config":{"token":%q}}}}`, secret, secret)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	target := Target{Endpoint: server.URL, APIKey: secret}
	if _, err := (Client{}).Bots(context.Background(), target); result.AsError(err).Kind != "incompatible" {
		t.Fatalf("Bot list without UUID error = %+v, want incompatible", result.AsError(err))
	} else if strings.Contains(err.Error(), secret) {
		t.Fatalf("Bot list protocol error exposed API Key: %v", err)
	}
	for _, get := range []struct {
		name string
		call func() (map[string]any, error)
	}{
		{name: "bot", call: func() (map[string]any, error) {
			return (Client{}).Bot(context.Background(), target, "requested")
		}},
		{name: "pipeline", call: func() (map[string]any, error) {
			return (Client{}).Pipeline(context.Background(), target, "requested")
		}},
	} {
		t.Run(get.name, func(t *testing.T) {
			_, err := get.call()
			if result.AsError(err).Kind != "incompatible" {
				t.Fatalf("%s UUID mismatch error = %+v, want incompatible", get.name, result.AsError(err))
			}
			if strings.Contains(err.Error(), secret) {
				t.Fatalf("%s protocol error exposed API Key: %v", get.name, err)
			}
		})
	}
}

func TestResourceIDMustBeOneSafePathSegment(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
	}))
	defer server.Close()
	for _, identifier := range []string{"", ".", "..", "a/b", "a\\b", "a?b", "a#b"} {
		_, err := (Client{}).Bot(context.Background(), Target{Endpoint: server.URL}, identifier)
		if result.AsError(err).Kind != "input" {
			t.Errorf("Bot(%q) error = %+v, want input", identifier, result.AsError(err))
		}
	}
	if calls.Load() != 0 {
		t.Fatalf("unsafe resource IDs triggered %d requests", calls.Load())
	}
}

func TestResourceWritesUseFixedMethodsPathsAndReturnUUIDs(t *testing.T) {
	secret := "write-key"
	var requests []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-API-Key") != secret {
			t.Errorf("write headers = %#v", r.Header)
		}
		if (r.Method == http.MethodDelete || strings.HasSuffix(r.URL.Path, "/copy")) && r.Header.Get("Content-Type") != "" {
			t.Errorf("bodyless write sent Content-Type: %#v", r.Header)
		}
		if r.Method != http.MethodDelete && !strings.HasSuffix(r.URL.Path, "/copy") && r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("write with body omitted Content-Type: %#v", r.Header)
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read request body: %v", err)
		}
		requests = append(requests, r.Method+" "+r.URL.Path+" "+string(body))
		if r.Method == http.MethodPost && (r.URL.Path == "/proxy/api/v1/platform/bots" || r.URL.Path == "/proxy/api/v1/pipelines") {
			_, _ = w.Write([]byte(`{"code":0,"data":{"uuid":"created-resource"}}`))
			return
		}
		if r.URL.Path == "/proxy/api/v1/pipelines/pipeline-a/copy" {
			_, _ = w.Write([]byte(`{"code":0,"data":{"uuid":"copied-pipeline"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"code":0,"data":null}`))
	}))
	defer server.Close()
	target := Target{Endpoint: server.URL + "/proxy", APIKey: secret}
	if got, err := (Client{}).BotCreate(context.Background(), target, map[string]any{"name": "Bot A"}); err != nil || got.UUID != "created-resource" {
		t.Fatalf("BotCreate() = %#v, error = %v", got, err)
	}
	if _, err := (Client{}).BotUpdate(context.Background(), target, "bot-a", map[string]any{"name": "Bot B"}); err != nil {
		t.Fatalf("BotUpdate() error = %v", err)
	}
	if _, err := (Client{}).BotDelete(context.Background(), target, "bot-a"); err != nil {
		t.Fatalf("BotDelete() error = %v", err)
	}
	if got, err := (Client{}).PipelineCreate(context.Background(), target, map[string]any{"name": "Pipeline A"}); err != nil || got.UUID != "created-resource" {
		t.Fatalf("PipelineCreate() = %#v, error = %v", got, err)
	}
	if _, err := (Client{}).PipelineUpdate(context.Background(), target, "pipeline-a", map[string]any{"name": "Pipeline B"}); err != nil {
		t.Fatalf("PipelineUpdate() error = %v", err)
	}
	if _, err := (Client{}).PipelineDelete(context.Background(), target, "pipeline-a"); err != nil {
		t.Fatalf("PipelineDelete() error = %v", err)
	}
	if got, err := (Client{}).PipelineCopy(context.Background(), target, "pipeline-a"); err != nil || got.UUID != "copied-pipeline" {
		t.Fatalf("PipelineCopy() = %#v, error = %v", got, err)
	}
	want := []string{
		"POST /proxy/api/v1/platform/bots {\"name\":\"Bot A\"}",
		"PUT /proxy/api/v1/platform/bots/bot-a {\"name\":\"Bot B\"}",
		"DELETE /proxy/api/v1/platform/bots/bot-a ",
		"POST /proxy/api/v1/pipelines {\"name\":\"Pipeline A\"}",
		"PUT /proxy/api/v1/pipelines/pipeline-a {\"name\":\"Pipeline B\"}",
		"DELETE /proxy/api/v1/pipelines/pipeline-a ",
		"POST /proxy/api/v1/pipelines/pipeline-a/copy ",
	}
	if strings.Join(requests, "|") != strings.Join(want, "|") {
		t.Fatalf("write requests = %#v, want %#v", requests, want)
	}
}

func TestResourceWritesClassifyUnknownOutcomeWithoutRetry(t *testing.T) {
	secret := "write-secret"
	var calls atomic.Int32
	client := Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		calls.Add(1)
		return nil, errors.New("connection reset: " + secret)
	})}
	_, err := client.BotCreate(context.Background(), Target{Endpoint: "http://example.test", APIKey: secret}, map[string]any{"token": secret})
	got := result.AsError(err)
	if got.Kind != "network" || got.Type != "result_unknown" || got.Message != "写操作结果未知，请检查服务端状态" {
		t.Fatalf("write transport error = %+v", got)
	}
	if calls.Load() != 1 || strings.Contains(err.Error(), secret) {
		t.Fatalf("write retried or exposed secret: calls=%d error=%v", calls.Load(), err)
	}
}

func TestResourceWriteServerFailureIsUnknownAndKeepsRequestID(t *testing.T) {
	var calls atomic.Int32
	client := Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		calls.Add(1)
		return &http.Response{
			StatusCode: http.StatusInternalServerError,
			Header:     http.Header{"X-Request-Id": []string{"request-safe"}},
			Body:       io.NopCloser(strings.NewReader(`{"code":-1,"data":null}`)),
			Request:    request,
		}, nil
	})}
	_, err := client.BotUpdate(context.Background(), Target{Endpoint: "http://example.test", APIKey: "write-secret"}, "bot-a", map[string]any{"name": "Bot"})
	failure := result.AsError(err)
	if failure.Type != "result_unknown" || failure.HTTPStatus != http.StatusInternalServerError || failure.RequestID != "request-safe" {
		t.Fatalf("server failure = %+v", failure)
	}
	if calls.Load() != 1 {
		t.Fatalf("write retried: calls=%d", calls.Load())
	}
}

func TestResourceWritesRejectMissingUUIDAndUnsafeIDs(t *testing.T) {
	secret := "write-secret"
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		_, _ = fmt.Fprintf(w, `{"code":0,"msg":%q,"data":{}}`, secret)
	}))
	defer server.Close()
	_, err := (Client{}).BotCreate(context.Background(), Target{Endpoint: server.URL, APIKey: secret}, map[string]any{"name": "safe"})
	got := result.AsError(err)
	if got.Kind != "network" || got.Type != "result_unknown" || strings.Contains(err.Error(), secret) {
		t.Fatalf("missing UUID error = %+v", got)
	}
	_, err = (Client{}).BotDelete(context.Background(), Target{Endpoint: server.URL, APIKey: secret}, "bad/id")
	if result.AsError(err).Kind != "input" || calls.Load() != 1 {
		t.Fatalf("unsafe ID error = %+v, calls=%d", result.AsError(err), calls.Load())
	}
	if _, err = (Client{}).write(context.Background(), Target{Endpoint: server.URL}, http.MethodPost, "/api/v1/unknown", nil); result.AsError(err).Kind != "incompatible" {
		t.Fatalf("unknown write path error = %+v", result.AsError(err))
	}
	if _, err = (Client{}).BotCreate(context.Background(), Target{Endpoint: server.URL}, nil); result.AsError(err).Kind != "input" {
		t.Fatalf("nil create body error = %+v", result.AsError(err))
	}
	if _, err = (Client{}).PipelineUpdate(context.Background(), Target{Endpoint: server.URL}, "pipeline-a", nil); result.AsError(err).Kind != "input" {
		t.Fatalf("nil update body error = %+v", result.AsError(err))
	}
}

func TestResourceWriteRejectsUnsafeReturnedUUID(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"code":0,"data":{"uuid":"unsafe/id"}}`)
	}))
	defer server.Close()
	_, err := (Client{}).PipelineCreate(context.Background(), Target{Endpoint: server.URL}, map[string]any{"name": "Pipeline"})
	if failure := result.AsError(err); failure.Type != "result_unknown" {
		t.Fatalf("unsafe returned UUID error = %+v", failure)
	}
}

func TestRawReturnsOnlyTheSafeSystemInfoProjection(t *testing.T) {
	secret := "secret"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprintf(w, `{"code":"0","msg":"server message","data":{"version":"v","edition":"e","debug":true,"wizard_progress":{"token":"%s"},"cloud_service_url":"https://example.test","unknown":"value"}}`, secret)
	}))
	defer server.Close()

	value, err := (Client{}).Raw(context.Background(), Target{Endpoint: server.URL, APIKey: secret}, http.MethodGet, infoPath)
	if err != nil {
		t.Fatalf("Raw() error = %v", err)
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("Marshal(Raw()) error = %v", err)
	}
	text := string(encoded)
	for _, forbidden := range []string{"wizard_progress", "cloud_service_url", "unknown", secret, "server message"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("Raw() 返回了未允许字段 %q: %s", forbidden, text)
		}
	}
	if !strings.Contains(text, `"code":0`) || !strings.Contains(text, `"msg":"ok"`) || !strings.Contains(text, `"version":"v"`) {
		t.Fatalf("Raw() 未保留安全包络: %s", text)
	}
}

func TestRedirectIsNotFollowedAndDoesNotForwardKey(t *testing.T) {
	var destinationCalls atomic.Int32
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		destinationCalls.Add(1)
		if r.Header.Get("X-API-Key") != "" {
			t.Error("重定向目标收到了 API Key")
		}
		_, _ = w.Write([]byte(`{"code":0,"data":{}}`))
	}))
	defer destination.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, destination.URL+infoPath, http.StatusTemporaryRedirect)
	}))
	defer redirect.Close()

	_, err := (Client{}).Info(context.Background(), Target{Endpoint: redirect.URL, APIKey: "secret"})
	if result.AsError(err).Kind != "incompatible" {
		t.Fatalf("redirect error = %+v, want incompatible", result.AsError(err))
	}
	if destinationCalls.Load() != 0 {
		t.Fatal("客户端跟随了重定向")
	}
}

func TestMaliciousResponseDoesNotLeakKey(t *testing.T) {
	secret := "key-secret-123"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Request-Id", secret)
		_, _ = fmt.Fprintf(w, `{"code":%q,"msg":%q,"request_id":%q,"data":{"version":%q,"edition":"community","wizard_status":%q}}`, secret, secret, secret, secret, secret)
	}))
	defer server.Close()

	_, err := (Client{}).Info(context.Background(), Target{Endpoint: server.URL, APIKey: secret})
	if err == nil {
		t.Fatal("恶意业务响应应当失败")
	}
	encoded, _ := json.Marshal(result.AsError(err))
	if strings.Contains(string(encoded), secret) || strings.Contains(err.Error(), secret) {
		t.Fatalf("错误泄露 API Key: %s", encoded)
	}

	server2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprintf(w, `{"code":0,"msg":"ok","data":{"version":%q,"edition":"community"}}`, secret)
	}))
	defer server2.Close()
	_, err = (Client{}).Info(context.Background(), Target{Endpoint: server2.URL, APIKey: secret})
	if err == nil {
		t.Fatal("包含 API Key 的版本字段应当被拒绝")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("协议错误泄露 API Key: %v", err)
	}
}
