package integration_test

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func TestTaskListPassesFiltersAndRendersPublicTasks(t *testing.T) {
	var query string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/api/v1/system/context":
			fmt.Fprint(w, `{"code":0,"data":{"instance_uuid":"instance-a","workspace_uuid":"workspace-a","api_key_id":"key-a","permissions":["resource.view"]}}`)
		case "/api/v1/system/capabilities":
			fmt.Fprint(w, `{"code":0,"data":{"schema_version":1,"operations":{"task.list":{"supported":true}}}}`)
		case "/api/v1/system/tasks":
			query = request.URL.RawQuery
			fmt.Fprint(w, `{"code":0,"data":{"tasks":[{"id":7,"task_type":"user","kind":"extension-operation","status":"succeeded","error":null,"result":null,"created_at":1.0}]}}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	configPath := filepath.Join(t.TempDir(), "config.yaml")
	requireSuccess(t, run(t, configPath, nil, "", "context", "add", "production", "--endpoint", server.URL, "--api-key-env", "KEY", "--expect-workspace", "workspace-a"))
	result := run(t, configPath, map[string]string{"KEY": "connection-secret"}, "", "--context", "production", "task", "list", "--type", "user", "--kind", "extension-operation")
	requireSuccess(t, result)
	if query != "kind=extension-operation&type=user" || !strings.Contains(result.out, `"kind": "extension-operation"`) {
		t.Fatalf("task list query=%q output=%s", query, result.out)
	}
}

func TestControlledAPIPostUsesReadSemantics(t *testing.T) {
	var body string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/api/v1/system/context":
			fmt.Fprint(w, `{"code":0,"data":{"instance_uuid":"instance-a","workspace_uuid":"workspace-a","api_key_id":"key-a","permissions":["resource.view"]}}`)
		case "/api/v1/system/capabilities":
			fmt.Fprint(w, `{"code":0,"data":{"schema_version":1,"operations":{"knowledge_base.retrieve":{"supported":true}}}}`)
		case "/api/v1/knowledge/bases/kb-a/retrieve":
			data, _ := io.ReadAll(request.Body)
			body = string(data)
			fmt.Fprint(w, `{"code":0,"data":{"results":[],"token_count":12}}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	configPath := filepath.Join(t.TempDir(), "config.yaml")
	requireSuccess(t, run(t, configPath, nil, "", "context", "add", "production", "--endpoint", server.URL, "--api-key-env", "KEY", "--expect-workspace", "workspace-a"))
	result := run(
		t,
		configPath,
		map[string]string{"KEY": "connection-secret"},
		`{"query":"hello"}`,
		"--context", "production", "api", "post", "/api/v1/knowledge/bases/kb-a/retrieve", "--file", "-",
	)
	requireSuccess(t, result)
	if !strings.Contains(body, `"query":"hello"`) || !strings.Contains(result.out, `"token_count": 12`) {
		t.Fatalf("api post body=%q output=%s", body, result.out)
	}
}

func TestControlledAPIRejectsUnknownPostBeforeReadingBody(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "missing.yaml")
	result := runLookup(
		t,
		configPath,
		nil,
		func(string) (string, bool) { return "", false },
		panicReader{},
		"api", "post", "/api/v1/unknown", "--file", "-",
	)
	if result.code == 0 || result.data["ok"] != false {
		t.Fatalf("unknown API path was accepted: %s", result.out)
	}
}
