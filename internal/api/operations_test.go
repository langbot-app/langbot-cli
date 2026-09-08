package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/langbot-app/langbot-cli/internal/result"
)

func TestResolveOperationRestrictsPathsAndPreservesOperationSemantics(t *testing.T) {
	tests := []struct {
		name       string
		method     string
		path       string
		id         string
		permission string
		allowed    bool
	}{
		{name: "task list query", method: http.MethodGet, path: "/api/v1/system/tasks?kind=extension-operation", id: "task.list", permission: "resource.view", allowed: true},
		{name: "knowledge retrieve", method: http.MethodPost, path: "/api/v1/knowledge/bases/kb-a/retrieve", id: "knowledge_base.retrieve", permission: "resource.view", allowed: true},
		{name: "mcp resource read", method: http.MethodPost, path: "/api/v1/mcp/servers/server-a/resources/read", id: "mcp_server.resource_read", permission: "resource.view", allowed: true},
		{name: "plugin logs", method: http.MethodGet, path: "/api/v1/plugins/author/plugin/logs?limit=20", id: "plugin.logs", permission: "audit.view", allowed: true},
		{name: "skill preview", method: http.MethodGet, path: "/api/v1/skills/demo/preview", id: "skill.preview", permission: "resource.manage", allowed: true},
		{name: "resource create", method: http.MethodPost, path: "/api/v1/knowledge/bases", allowed: false},
		{name: "provider scan", method: http.MethodGet, path: "/api/v1/provider/providers/provider-a/scan-models", allowed: false},
		{name: "model test", method: http.MethodPost, path: "/api/v1/provider/models/llm/model-a/test", allowed: false},
		{name: "unknown path", method: http.MethodGet, path: "/api/v1/unknown", allowed: false},
		{name: "external URL", method: http.MethodGet, path: "https://example.invalid/api/v1/system/info", allowed: false},
		{name: "delete verb", method: http.MethodDelete, path: "/api/v1/bots/bot-a", allowed: false},
		{name: "encoded traversal", method: http.MethodGet, path: "/api/v1/mcp/servers/%2e%2e", allowed: false},
		{name: "encoded slash", method: http.MethodGet, path: "/api/v1/mcp/servers/server%2Fa", allowed: false},
		{name: "encoded backslash", method: http.MethodGet, path: "/api/v1/mcp/servers/server%5Ca", allowed: false},
		{name: "extra segment", method: http.MethodGet, path: "/api/v1/plugins/author/plugin/logs/extra", allowed: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			operation, ok := ResolveOperation(test.method, test.path)
			if ok != test.allowed {
				t.Fatalf("ResolveOperation() allowed = %v, want %v", ok, test.allowed)
			}
			if !ok {
				return
			}
			if operation.ID != test.id || !operation.ReadOnly || operation.Permission != test.permission {
				t.Fatalf("ResolveOperation() = %#v, want id=%q readonly=true permission=%q", operation, test.id, test.permission)
			}
		})
	}
}

func TestAPIRequestPreservesSafeFieldsAndMasksConnectionCredential(t *testing.T) {
	client := Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body: io.NopCloser(strings.NewReader(
				`{"code":0,"data":{"api_key_id":"key-id","token_count":12,"text":"prefix connection-secret suffix"}}`,
			)),
		}, nil
	})}
	operation, ok := ResolveOperation(http.MethodGet, "/api/v1/system/tasks")
	if !ok {
		t.Fatal("task list operation was not registered")
	}
	value, err := client.APIRequest(context.Background(), Target{
		Endpoint: "http://example.test",
		APIKey:   "connection-secret",
	}, operation, nil)
	if err != nil {
		t.Fatalf("APIRequest() error = %v", err)
	}
	data := value.(map[string]any)
	if data["api_key_id"] != "key-id" || data["token_count"].(json.Number).String() != "12" || data["text"] != "prefix *** suffix" {
		t.Fatalf("APIRequest() data = %#v", data)
	}
}

func TestAPIRequestRejectsUnknownOperationWithoutRequest(t *testing.T) {
	var calls atomic.Int32
	client := Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		return nil, errors.New("unexpected request")
	})}
	_, err := client.APIRequest(context.Background(), Target{Endpoint: "http://example.test"}, Operation{
		ID: "unknown", Method: http.MethodGet, Path: "/api/v1/unknown", ReadOnly: true,
	}, nil)
	if result.AsError(err).Kind != "incompatible" || calls.Load() != 0 {
		t.Fatalf("unknown operation error = %+v, calls = %d", result.AsError(err), calls.Load())
	}
}

func TestReadOnlyPostFailuresAreNotClassifiedAsUnknownWrites(t *testing.T) {
	operation, ok := ResolveOperation(http.MethodPost, "/api/v1/knowledge/bases/kb-a/retrieve")
	if !ok {
		t.Fatal("knowledge retrieve operation was not registered")
	}
	tests := []struct {
		name      string
		transport http.RoundTripper
	}{
		{
			name: "network",
			transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				return nil, errors.New("connection failed")
			}),
		},
		{
			name: "server",
			transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: http.StatusInternalServerError,
					Header:     make(http.Header),
					Body:       io.NopCloser(strings.NewReader(`{"code":500,"msg":"failed"}`)),
				}, nil
			}),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client := Client{Transport: test.transport}
			_, err := client.APIRequest(context.Background(), Target{Endpoint: "http://example.test"}, operation, map[string]any{"query": "hello"})
			if failure := result.AsError(err); failure.Type == "result_unknown" {
				t.Fatalf("read-only POST was classified as unknown write: %+v", failure)
			}
		})
	}
}
