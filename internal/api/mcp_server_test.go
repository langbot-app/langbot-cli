package api

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestMCPReadMethodsKeepConfigurationOutOfNormalProjection(t *testing.T) {
	client := Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		var body string
		switch request.URL.Path {
		case mcpServersPath:
			body = `{"code":0,"data":{"servers":[{"uuid":"mcp-a","name":"demo","enable":true,"mode":"sse","config":{"token":"secret"}}]}}`
		case mcpServersPath + "/demo":
			body = `{"code":0,"data":{"server":{"uuid":"mcp-a","name":"demo","enable":true,"mode":"sse","extra_args":{"token":"***","note":"secret"}}}}`
		case mcpServersPath + "/demo/resources":
			body = `{"code":0,"data":{"resources":[{"uri":"x","description":"secret"}]}}`
		case mcpServersPath + "/demo/logs":
			body = `{"code":0,"data":{"logs":[{"message":"secret"}]}}`
		default:
			return nil, nil
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
	})}
	target := Target{Endpoint: "http://example.test", APIKey: "secret"}
	servers, err := client.MCPServers(context.Background(), target)
	if err != nil || len(servers) != 1 || servers[0]["name"] != "demo" {
		t.Fatalf("MCPServers() = %#v, error = %v", servers, err)
	}
	if _, leaked := servers[0]["config"]; leaked {
		t.Fatal("MCPServers() exposed config")
	}
	detail, err := client.MCPServerGet(context.Background(), target, "demo")
	if err != nil || detail["extra_args"].(map[string]any)["token"] != "***" || detail["extra_args"].(map[string]any)["note"] != "***" {
		t.Fatalf("MCPServerGet() = %#v, error = %v", detail, err)
	}
	resources, err := client.MCPServerResources(context.Background(), target, "demo")
	if err != nil || resources.(map[string]any)["resources"].([]any)[0].(map[string]any)["description"] != "***" {
		t.Fatalf("MCPServerResources() = %#v, error = %v", resources, err)
	}
	if _, err := client.MCPServerLogs(context.Background(), target, "demo", "error", 20); err != nil {
		t.Fatalf("MCPServerLogs() error = %v path=%s", err, mcpServersPath+"/demo/logs?level=error&limit=20")
	}
}

func TestMCPWriteMethodsSupportRenamesAndEscapedNames(t *testing.T) {
	var requests []string
	client := Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		requests = append(requests, request.Method+" "+request.URL.RequestURI())
		body := `{"code":0,"data":null}`
		if request.Method == http.MethodPost {
			body = `{"code":0,"data":{"uuid":"mcp-a"}}`
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
	})}
	target := Target{Endpoint: "http://example.test", APIKey: "secret"}

	if result, err := client.MCPServerCreate(context.Background(), target, map[string]any{"name": "team/demo"}); err != nil || result.UUID != "mcp-a" {
		t.Fatalf("MCPServerCreate() = %#v, error = %v", result, err)
	}
	if err := client.MCPServerUpdate(context.Background(), target, "team/demo", map[string]any{"name": "team/new"}); err != nil {
		t.Fatalf("MCPServerUpdate() error = %v", err)
	}
	if err := client.MCPServerDelete(context.Background(), target, "team/new"); err != nil {
		t.Fatalf("MCPServerDelete() error = %v", err)
	}

	want := []string{
		"POST /api/v1/mcp/servers",
		"PUT /api/v1/mcp/servers/team%2Fdemo",
		"DELETE /api/v1/mcp/servers/team%2Fnew",
	}
	if strings.Join(requests, "|") != strings.Join(want, "|") {
		t.Fatalf("requests = %#v", requests)
	}
}

func TestMCPServerGetRejectsMismatchedIdentity(t *testing.T) {
	client := Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"code":0,"data":{"server":{"uuid":"mcp-a","name":"other"}}}`)),
		}, nil
	})}
	_, err := client.MCPServerGet(context.Background(), Target{Endpoint: "http://example.test"}, "expected")
	if err == nil {
		t.Fatal("MCPServerGet() accepted a mismatched server identity")
	}
}
