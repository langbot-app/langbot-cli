package integration_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestKnowledgeBaseCommandsCoverReadsWritesAndConfirmation(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	baseExists := false
	baseName := "KB"
	fileExists := true
	var writes []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/system/context":
			fmt.Fprint(w, `{"code":0,"data":{"instance_uuid":"instance-a","workspace_uuid":"workspace-a","api_key_id":"key-a","permissions":["resource.manage","resource.view"]}}`)
		case "/api/v1/system/capabilities":
			fmt.Fprint(w, capabilitiesJSON(
				"knowledge_base.list", "knowledge_base.get", "knowledge_base.create", "knowledge_base.update",
				"knowledge_base.delete", "knowledge_base.file.list", "knowledge_base.file.delete", "knowledge_base.retrieve",
			))
		case "/api/v1/knowledge/bases":
			if r.Method == http.MethodPost {
				baseExists = true
				writes = append(writes, r.Method+" "+r.URL.Path)
				fmt.Fprint(w, `{"code":0,"data":{"uuid":"kb-a"}}`)
				return
			}
			bases := "[]"
			if baseExists {
				bases = fmt.Sprintf(`[{"uuid":"kb-a","name":%q,"creation_settings":{"token":"business-secret"}}]`, baseName)
			}
			fmt.Fprintf(w, `{"code":0,"data":{"bases":%s}}`, bases)
		case "/api/v1/knowledge/bases/kb-a":
			if !baseExists && r.Method == http.MethodGet {
				writeCLIError(w, http.StatusNotFound)
				return
			}
			switch r.Method {
			case http.MethodGet:
				fmt.Fprintf(w, `{"code":0,"data":{"base":{"uuid":"kb-a","name":%q,"retrieval_settings":{"token":"business-secret"}}}}`, baseName)
			case http.MethodPut:
				var body map[string]any
				_ = json.NewDecoder(r.Body).Decode(&body)
				if name, ok := body["name"].(string); ok {
					baseName = name
				}
				writes = append(writes, r.Method+" "+r.URL.Path)
				fmt.Fprint(w, `{"code":0,"data":{"uuid":"kb-a"}}`)
			case http.MethodDelete:
				baseExists = false
				writes = append(writes, r.Method+" "+r.URL.Path)
				fmt.Fprint(w, `{"code":0,"data":null}`)
			}
		case "/api/v1/knowledge/bases/kb-a/files":
			files := "[]"
			if fileExists {
				files = `[{"uuid":"file-a","file_name":"doc.txt"}]`
			}
			fmt.Fprintf(w, `{"code":0,"data":{"files":%s}}`, files)
		case "/api/v1/knowledge/bases/kb-a/files/file-a":
			fileExists = false
			writes = append(writes, r.Method+" "+r.URL.Path)
			fmt.Fprint(w, `{"code":0,"data":null}`)
		case "/api/v1/knowledge/bases/kb-a/retrieve":
			fmt.Fprint(w, `{"code":0,"data":{"results":[{"text":"answer"}]}}`)
		default:
			writeCLIError(w, http.StatusNotFound)
		}
	}))
	defer server.Close()

	configureWriteContext(t, configPath, server.URL, "KB_MANAGE_KEY")
	env := map[string]string{"KB_MANAGE_KEY": "connection-secret"}
	createBody := writeBodyFile(t, `{"name":"KB","knowledge_engine_plugin_id":"engine","creation_settings":{"token":"business-secret"}}`)
	updateBody := writeBodyFile(t, `{"name":"Updated"}`)
	retrieveBody := writeBodyFile(t, `{"query":"hello"}`)

	requireSuccess(t, run(t, configPath, env, "", "knowledge-base", "create", "--file", createBody, "--dry-run"))
	if len(writes) != 0 {
		t.Fatalf("dry-run writes = %#v", writes)
	}
	requireSuccess(t, run(t, configPath, env, "", "knowledge-base", "create", "--file", createBody))
	listed := run(t, configPath, env, "", "knowledge-base", "list")
	requireSuccess(t, listed)
	if strings.Contains(listed.out, "business-secret") {
		t.Fatalf("list exposed settings: %s", listed.out)
	}
	requireSuccess(t, run(t, configPath, env, "", "knowledge-base", "update", "kb-a", "--file", updateBody))
	requireSuccess(t, run(t, configPath, env, "", "knowledge-base", "retrieve", "kb-a", "--file", retrieveBody))
	requireSuccess(t, run(t, configPath, env, "", "knowledge-base", "file", "list", "kb-a"))
	blockedFileDelete := run(t, configPath, env, "", "knowledge-base", "file", "delete", "kb-a", "file-a")
	if blockedFileDelete.code != 2 {
		t.Fatalf("file delete without --yes = %s", blockedFileDelete.out)
	}
	requireSuccess(t, run(t, configPath, env, "", "knowledge-base", "file", "delete", "kb-a", "file-a", "--yes"))
	blockedDelete := run(t, configPath, env, "", "knowledge-base", "delete", "kb-a")
	if blockedDelete.code != 2 {
		t.Fatalf("delete without --yes = %s", blockedDelete.out)
	}
	requireSuccess(t, run(t, configPath, env, "", "knowledge-base", "delete", "kb-a", "--yes"))
	if got := strings.Join(writes, "|"); got != "POST /api/v1/knowledge/bases|PUT /api/v1/knowledge/bases/kb-a|DELETE /api/v1/knowledge/bases/kb-a/files/file-a|DELETE /api/v1/knowledge/bases/kb-a" {
		t.Fatalf("writes = %s", got)
	}
}

func TestMCPServerCommandsCoverRenameRuntimeReadsAndDelete(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	serverExists := false
	serverName := "demo"
	var writes []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/system/context":
			fmt.Fprint(w, `{"code":0,"data":{"instance_uuid":"instance-a","workspace_uuid":"workspace-a","api_key_id":"key-a","permissions":["resource.manage","resource.view","audit.view"]}}`)
		case "/api/v1/system/capabilities":
			fmt.Fprint(w, capabilitiesJSON(
				"mcp_server.list", "mcp_server.get", "mcp_server.create", "mcp_server.update", "mcp_server.delete",
				"mcp_server.resources", "mcp_server.resource_templates", "mcp_server.resource_read", "mcp_server.logs",
			))
		case "/api/v1/mcp/servers":
			if r.Method == http.MethodPost {
				serverExists = true
				writes = append(writes, r.Method+" "+r.URL.Path)
				fmt.Fprint(w, `{"code":0,"data":{"uuid":"mcp-a"}}`)
				return
			}
			servers := "[]"
			if serverExists {
				servers = fmt.Sprintf(`[{"uuid":"mcp-a","name":%q,"mode":"remote","extra_args":{"headers":{"Authorization":"business-secret"}}}]`, serverName)
			}
			fmt.Fprintf(w, `{"code":0,"data":{"servers":%s}}`, servers)
		case "/api/v1/mcp/servers/demo":
			if r.Method == http.MethodPut {
				serverName = "renamed"
				writes = append(writes, r.Method+" "+r.URL.Path)
				fmt.Fprint(w, `{"code":0,"data":null}`)
				return
			}
			writeMCPServer(w, serverExists, serverName)
		case "/api/v1/mcp/servers/renamed":
			if r.Method == http.MethodDelete {
				serverExists = false
				writes = append(writes, r.Method+" "+r.URL.Path)
				fmt.Fprint(w, `{"code":0,"data":null}`)
				return
			}
			writeMCPServer(w, serverExists, serverName)
		case "/api/v1/mcp/servers/renamed/resources":
			fmt.Fprint(w, `{"code":0,"data":{"resources":[{"uri":"file://demo"}]}}`)
		case "/api/v1/mcp/servers/renamed/resource-templates":
			fmt.Fprint(w, `{"code":0,"data":{"resource_templates":[]}}`)
		case "/api/v1/mcp/servers/renamed/resources/read":
			fmt.Fprint(w, `{"code":0,"data":{"contents":[{"text":"content"}]}}`)
		case "/api/v1/mcp/servers/renamed/logs":
			if r.URL.Query().Get("limit") != "20" || r.URL.Query().Get("level") != "error" {
				writeCLIError(w, http.StatusBadRequest)
				return
			}
			fmt.Fprint(w, `{"code":0,"data":{"logs":[]}}`)
		default:
			writeCLIError(w, http.StatusNotFound)
		}
	}))
	defer server.Close()

	configureWriteContext(t, configPath, server.URL, "MCP_MANAGE_KEY")
	env := map[string]string{"MCP_MANAGE_KEY": "connection-secret"}
	createBody := writeBodyFile(t, `{"name":"demo","enable":false,"mode":"remote","extra_args":{"url":"http://example.test"}}`)
	updateBody := writeBodyFile(t, `{"name":"renamed","enable":false}`)
	readBody := writeBodyFile(t, `{"uri":"file://demo"}`)

	requireSuccess(t, run(t, configPath, env, "", "mcp-server", "create", "--file", createBody))
	listed := run(t, configPath, env, "", "mcp-server", "list")
	requireSuccess(t, listed)
	if strings.Contains(listed.out, "business-secret") {
		t.Fatalf("list exposed MCP configuration: %s", listed.out)
	}
	detail := run(t, configPath, env, "", "mcp-server", "get", "demo")
	requireSuccess(t, detail)
	if strings.Contains(detail.out, "business-secret") || !strings.Contains(detail.out, `"Authorization": "***"`) {
		t.Fatalf("get did not preserve the redacted MCP configuration: %s", detail.out)
	}
	updated := run(t, configPath, env, "", "mcp-server", "update", "demo", "--file", updateBody)
	requireSuccess(t, updated)
	if updated.data["data"].(map[string]any)["server_name"] != "renamed" {
		t.Fatalf("rename output = %s", updated.out)
	}
	requireSuccess(t, run(t, configPath, env, "", "mcp-server", "resources", "renamed"))
	requireSuccess(t, run(t, configPath, env, "", "mcp-server", "resource-templates", "renamed"))
	requireSuccess(t, run(t, configPath, env, "", "mcp-server", "resource-read", "renamed", "--file", readBody))
	requireSuccess(t, run(t, configPath, env, "", "mcp-server", "logs", "renamed", "--level", "error", "--limit", "20"))
	invalidLimit := run(t, configPath, env, "", "mcp-server", "logs", "renamed", "--limit", "0")
	if invalidLimit.code != 2 {
		t.Fatalf("invalid log limit = %s", invalidLimit.out)
	}
	blocked := run(t, configPath, env, "", "mcp-server", "delete", "renamed")
	if blocked.code != 2 {
		t.Fatalf("delete without --yes = %s", blocked.out)
	}
	requireSuccess(t, run(t, configPath, env, "", "mcp-server", "delete", "renamed", "--yes"))
	if got := strings.Join(writes, "|"); got != "POST /api/v1/mcp/servers|PUT /api/v1/mcp/servers/demo|DELETE /api/v1/mcp/servers/renamed" {
		t.Fatalf("writes = %s", got)
	}
}

func capabilitiesJSON(operations ...string) string {
	values := make(map[string]map[string]bool, len(operations))
	for _, operation := range operations {
		values[operation] = map[string]bool{"supported": true}
	}
	data, _ := json.Marshal(map[string]any{"code": 0, "data": map[string]any{"schema_version": 1, "operations": values}})
	return string(data)
}

func writeBodyFile(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "body.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func writeMCPServer(w http.ResponseWriter, exists bool, name string) {
	if !exists {
		writeCLIError(w, http.StatusNotFound)
		return
	}
	fmt.Fprintf(w, `{"code":0,"data":{"server":{"uuid":"mcp-a","name":%q,"mode":"remote","extra_args":{"headers":{"Authorization":"***"}}}}}`, name)
}

func writeCLIError(w http.ResponseWriter, status int) {
	w.WriteHeader(status)
	fmt.Fprintf(w, `{"code":%d,"data":null}`, status)
}
