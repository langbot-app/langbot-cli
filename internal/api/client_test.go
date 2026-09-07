package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
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
