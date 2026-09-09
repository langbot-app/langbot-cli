package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func TestKnowledgeBaseWritesUsePreflightConfirmationAndReadback(t *testing.T) {
	baseExists := false
	files := []string{"file-a"}
	var writes []string
	service, options, closeServer := managedResourceService(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/knowledge/bases":
			if r.Method == http.MethodPost {
				baseExists = true
				writes = append(writes, r.Method+" "+r.URL.Path)
				fmt.Fprint(w, `{"code":0,"data":{"uuid":"kb-a"}}`)
				return
			}
		case "/api/v1/knowledge/bases/kb-a":
			switch r.Method {
			case http.MethodGet:
				if !baseExists {
					writeNotFound(w)
					return
				}
				fmt.Fprint(w, `{"code":0,"data":{"base":{"uuid":"kb-a","name":"KB","retrieval_settings":{"token":"hidden"}}}}`)
				return
			case http.MethodPut:
				writes = append(writes, r.Method+" "+r.URL.Path)
				fmt.Fprint(w, `{"code":0,"data":{"uuid":"kb-a"}}`)
				return
			case http.MethodDelete:
				writes = append(writes, r.Method+" "+r.URL.Path)
				baseExists = false
				fmt.Fprint(w, `{"code":0,"data":null}`)
				return
			}
		case "/api/v1/knowledge/bases/kb-a/files":
			items := make([]map[string]any, 0, len(files))
			for _, id := range files {
				items = append(items, map[string]any{"uuid": id, "file_name": "doc.txt"})
			}
			writeData(w, map[string]any{"files": items})
			return
		case "/api/v1/knowledge/bases/kb-a/files/file-a":
			writes = append(writes, r.Method+" "+r.URL.Path)
			files = nil
			fmt.Fprint(w, `{"code":0,"data":null}`)
			return
		}
		writeNotFound(w)
	})
	defer closeServer()

	request := map[string]any{"name": "KB", "knowledge_engine_plugin_id": "engine"}
	dryRun, err := service.KnowledgeBaseCreate(context.Background(), request, true, options)
	if err != nil || dryRun.Data.(map[string]any)["server_write"] != false || len(writes) != 0 {
		t.Fatalf("dry-run = %#v, error = %v, writes = %#v", dryRun, err, writes)
	}
	created, err := service.KnowledgeBaseCreate(context.Background(), request, false, options)
	if err != nil || created.Data.(map[string]any)["verified"] != true {
		t.Fatalf("create = %#v, error = %v", created, err)
	}
	if _, err := service.KnowledgeBaseUpdate(context.Background(), "kb-a", map[string]any{"description": "updated"}, false, options); err != nil {
		t.Fatalf("update error = %v", err)
	}
	if _, err := service.KnowledgeBaseFileDelete(context.Background(), "kb-a", "file-a", false, false, options); resultError(err).Kind != "input" {
		t.Fatalf("delete without confirmation error = %+v", resultError(err))
	}
	if _, err := service.KnowledgeBaseFileDelete(context.Background(), "kb-a", "file-a", true, false, options); err != nil {
		t.Fatalf("file delete error = %v", err)
	}
	if _, err := service.KnowledgeBaseDelete(context.Background(), "kb-a", true, false, options); err != nil {
		t.Fatalf("delete error = %v", err)
	}
	if got := strings.Join(writes, "|"); got != "POST /api/v1/knowledge/bases|PUT /api/v1/knowledge/bases/kb-a|DELETE /api/v1/knowledge/bases/kb-a/files/file-a|DELETE /api/v1/knowledge/bases/kb-a" {
		t.Fatalf("writes = %s", got)
	}
}

func TestMCPServerUpdateSupportsRenameAndVerifiesUUID(t *testing.T) {
	serverName := "old"
	const serverUUID = "mcp-a"
	var writes []string
	service, options, closeServer := managedResourceService(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/mcp/servers/old" && r.Method == http.MethodPut {
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			serverName = body["name"].(string)
			writes = append(writes, r.Method+" "+r.URL.Path)
			fmt.Fprint(w, `{"code":0,"data":null}`)
			return
		}
		if r.Method == http.MethodGet && r.URL.Path == "/api/v1/mcp/servers/"+serverName {
			fmt.Fprintf(w, `{"code":0,"data":{"server":{"uuid":%q,"name":%q,"mode":"remote","config":{"token":"***"}}}}`, serverUUID, serverName)
			return
		}
		writeNotFound(w)
	})
	defer closeServer()

	updated, err := service.MCPServerUpdate(context.Background(), "old", map[string]any{"name": "new", "extra_args": map[string]any{"token": "***"}}, false, options)
	if err != nil {
		t.Fatalf("update error = %v", err)
	}
	data := updated.Data.(map[string]any)
	if data["uuid"] != serverUUID || data["server_name"] != "new" || data["verified"] != true {
		t.Fatalf("update = %#v", updated)
	}
	if strings.Join(writes, "|") != "PUT /api/v1/mcp/servers/old" {
		t.Fatalf("writes = %#v", writes)
	}
	if _, err := service.MCPServerLogs(context.Background(), "new", "", 0, options); resultError(err).Kind != "input" {
		t.Fatalf("invalid log limit error = %+v", resultError(err))
	}
}

func managedResourceService(t *testing.T, handler http.HandlerFunc) (*Service, CheckOptions, func()) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/system/context":
			fmt.Fprint(w, `{"code":0,"data":{"instance_uuid":"instance-a","workspace_uuid":"workspace-a","api_key_id":"key-a","permissions":["resource.manage","resource.view","audit.view"]}}`)
		case "/api/v1/system/capabilities":
			operations := map[string]map[string]bool{}
			for _, operation := range []string{
				"knowledge_base.get", "knowledge_base.create", "knowledge_base.update", "knowledge_base.delete", "knowledge_base.file.list", "knowledge_base.file.delete",
				"mcp_server.get", "mcp_server.create", "mcp_server.update", "mcp_server.delete", "mcp_server.logs",
			} {
				operations[operation] = map[string]bool{"supported": true}
			}
			writeData(w, map[string]any{"schema_version": 1, "operations": operations})
		default:
			handler(w, r)
		}
	}))
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	service := New(Dependencies{
		LookupEnv:         func(name string) (string, bool) { return "secret", name == "KEY" },
		DefaultConfigPath: func() (string, error) { return configPath, nil },
	})
	if _, err := service.ContextAdd("production", server.URL, "KEY", "workspace-a", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := service.ContextUse("production"); err != nil {
		t.Fatal(err)
	}
	return service, CheckOptions{ContextSet: true, Context: "production"}, server.Close
}

func writeData(w http.ResponseWriter, data any) {
	_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": data})
}

func writeNotFound(w http.ResponseWriter) {
	w.WriteHeader(http.StatusNotFound)
	fmt.Fprint(w, `{"code":404,"data":null}`)
}
