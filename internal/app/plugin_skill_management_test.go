package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/langbot-app/langbot-cli/internal/result"
)

const managementCapabilities = `{"schema_version":1,"operations":{
"plugin.get":{"supported":true},"plugin.config.get":{"supported":true},"plugin.config.update":{"supported":true},"plugin.delete":{"supported":true},"task.get":{"supported":true},
"skill.list":{"supported":true},"skill.get":{"supported":true},"skill.create":{"supported":true},"skill.update":{"supported":true},"skill.delete":{"supported":true},"skill.files.list":{"supported":true},"skill.files.read":{"supported":true},"skill.files.write":{"supported":true}}}`

type managementServer struct {
	pluginPresent atomic.Bool
	pluginConfig  atomic.Value
	skillPresent  atomic.Bool
	skillContent  atomic.Value
	skillDesc     atomic.Value
	deleteData    atomic.Bool
	deleteCalls   atomic.Int32
}

func newManagementService(t *testing.T, state *managementServer) (*Service, func()) {
	t.Helper()
	state.pluginPresent.Store(true)
	state.skillPresent.Store(true)
	state.pluginConfig.Store(map[string]any{"token": "***", "mode": "safe"})
	state.skillContent.Store("old content")
	state.skillDesc.Store("Demo")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/v1/system/context" {
			fmt.Fprint(w, `{"code":0,"data":{"instance_uuid":"instance-a","workspace_uuid":"workspace-a","api_key_id":"key-a","permissions":["resource.manage","resource.view","audit.view"]}}`)
			return
		}
		if r.URL.Path == "/api/v1/system/capabilities" {
			fmt.Fprintf(w, `{"code":0,"data":%s}`, managementCapabilities)
			return
		}
		switch r.URL.Path {
		case "/api/v1/plugins/author/plugin":
			if r.Method == http.MethodDelete {
				state.deleteCalls.Add(1)
				state.deleteData.Store(r.URL.Query().Get("delete_data") == "true")
				state.pluginPresent.Store(false)
				fmt.Fprint(w, `{"code":0,"data":{"task_id":17}}`)
				return
			}
			if !state.pluginPresent.Load() {
				w.WriteHeader(http.StatusNotFound)
				fmt.Fprint(w, `{"code":404,"msg":"not found"}`)
				return
			}
			fmt.Fprint(w, `{"code":0,"data":{"plugin":{"manifest":{"manifest":{"metadata":{"author":"author","name":"plugin"}}}}}}`)
		case "/api/v1/plugins/author/plugin/config":
			if r.Method == http.MethodGet {
				config := state.pluginConfig.Load().(map[string]any)
				payload, _ := json.Marshal(config)
				fmt.Fprintf(w, `{"code":0,"data":{"config":%s}}`, payload)
				return
			}
			if r.Method == http.MethodPut {
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Errorf("decode plugin config: %v", err)
				}
				state.pluginConfig.Store(body)
				fmt.Fprint(w, `{"code":0,"data":{}}`)
				return
			}
		case "/api/v1/plugins/author/plugin/logs":
			fmt.Fprint(w, `{"code":0,"data":{"logs":[]}}`)
		case "/api/v1/skills":
			if r.Method == http.MethodGet {
				if state.skillPresent.Load() {
					fmt.Fprintf(w, `{"code":0,"data":{"skills":[{"name":"demo","description":%q}]}}`, state.skillDesc.Load().(string))
				} else {
					fmt.Fprint(w, `{"code":0,"data":{"skills":[]}}`)
				}
				return
			}
			if r.Method == http.MethodPost {
				fmt.Fprint(w, `{"code":0,"data":{"skill":{"name":"demo","description":"Demo"}}}`)
				return
			}
		case "/api/v1/skills/demo":
			if !state.skillPresent.Load() {
				w.WriteHeader(http.StatusNotFound)
				fmt.Fprint(w, `{"code":404,"msg":"not found"}`)
				return
			}
			switch r.Method {
			case http.MethodGet:
				fmt.Fprintf(w, `{"code":0,"data":{"skill":{"name":"demo","description":%q,"instructions":"Use it"}}}`, state.skillDesc.Load().(string))
			case http.MethodPut:
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Errorf("decode skill update: %v", err)
				}
				if description, ok := body["description"].(string); ok {
					state.skillDesc.Store(description)
				}
				fmt.Fprint(w, `{"code":0,"data":{"skill":{"name":"demo","description":"Updated"}}}`)
			case http.MethodDelete:
				state.skillPresent.Store(false)
				fmt.Fprint(w, `{"code":0,"data":{}}`)
			}
		case "/api/v1/skills/demo/files":
			fmt.Fprint(w, `{"code":0,"data":{"entries":[{"path":"README.md"}]}}`)
		case "/api/v1/skills/demo/files/README.md":
			if r.Method == http.MethodGet {
				fmt.Fprintf(w, `{"code":0,"data":{"path":"README.md","content":%q}}`, state.skillContent.Load().(string))
				return
			}
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decode skill file: %v", err)
			}
			state.skillContent.Store(body["content"].(string))
			fmt.Fprint(w, `{"code":0,"data":{"path":"README.md"}}`)
		case "/api/v1/system/tasks/17":
			fmt.Fprint(w, `{"code":0,"data":{"id":17,"task_type":"user","kind":"plugin-operation","status":"succeeded","error":null,"result":null,"created_at":1.0}}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	service := New(Dependencies{
		Transport:         server.Client().Transport,
		LookupEnv:         func(name string) (string, bool) { return "connection-secret", name == "KEY" },
		DefaultConfigPath: func() (string, error) { return configPath, nil },
	})
	if _, err := service.ContextAdd("managed", server.URL, "KEY", "workspace-a", ""); err != nil {
		t.Fatal(err)
	}
	return service, server.Close
}

func TestPluginConfigUpdateDryRunAndReadbackMasksPlaceholder(t *testing.T) {
	state := &managementServer{}
	service, closeServer := newManagementService(t, state)
	defer closeServer()
	options := CheckOptions{ContextSet: true, Context: "managed"}
	body := map[string]any{"token": "***", "mode": "safe"}
	plan, err := service.PluginConfigUpdate(context.Background(), "author", "plugin", body, true, options)
	if err != nil {
		t.Fatalf("dry-run error = %v", err)
	}
	if plan.Data.(map[string]any)["server_write"] != false {
		t.Fatalf("dry-run data = %#v", plan.Data)
	}
	actual, err := service.PluginConfigUpdate(context.Background(), "author", "plugin", body, false, options)
	if err != nil {
		t.Fatalf("update error = %v", err)
	}
	data := actual.Data.(map[string]any)
	if data["verified"] != true || strings.Contains(fmt.Sprint(data), "connection-secret") {
		t.Fatalf("unsafe update result = %#v", data)
	}
}

func TestPluginDeleteRequiresConfirmationAndVerifiesAfterWait(t *testing.T) {
	state := &managementServer{}
	service, closeServer := newManagementService(t, state)
	defer closeServer()
	options := CheckOptions{ContextSet: true, Context: "managed"}
	if _, err := service.PluginDelete(context.Background(), "author", "plugin", true, false, false, false, WaitOptions{}, options); result.AsError(err).Kind != "input" {
		t.Fatalf("delete without confirmation error = %v", err)
	}
	plan, err := service.PluginDelete(context.Background(), "author", "plugin", true, false, true, false, WaitOptions{}, options)
	if err != nil || plan.Data.(map[string]any)["server_write"] != false {
		t.Fatalf("delete dry-run = %#v, error = %v", plan.Data, err)
	}
	value, err := service.PluginDelete(context.Background(), "author", "plugin", true, true, false, true, WaitOptions{PollInterval: time.Millisecond, WaitTimeout: time.Second}, options)
	if err != nil {
		t.Fatalf("delete wait error = %v", err)
	}
	if value.Data.(map[string]any)["verified"] != true || state.deleteCalls.Load() != 1 || !state.deleteData.Load() {
		t.Fatalf("delete result = %#v", value.Data)
	}
}

func TestSkillManagementReadsWritesAndVerifiesFileContent(t *testing.T) {
	state := &managementServer{}
	service, closeServer := newManagementService(t, state)
	defer closeServer()
	options := CheckOptions{ContextSet: true, Context: "managed"}
	if value, err := service.SkillList(context.Background(), options); err != nil || len(value.Data.(map[string]any)["skills"].([]map[string]any)) != 1 {
		t.Fatalf("list = %#v, error = %v", value.Data, err)
	}
	if _, err := service.SkillGet(context.Background(), "demo", options); err != nil {
		t.Fatalf("get error = %v", err)
	}
	if _, err := service.SkillCreate(context.Background(), map[string]any{"name": "demo"}, true, options); err != nil {
		t.Fatalf("create dry-run error = %v", err)
	}
	if _, err := service.SkillCreate(context.Background(), map[string]any{"name": "demo"}, false, options); err != nil {
		t.Fatalf("create error = %v", err)
	}
	if _, err := service.SkillUpdate(context.Background(), "demo", map[string]any{"name": "other"}, true, options); result.AsError(err).Kind != "input" {
		t.Fatalf("rename update error = %v", err)
	}
	if _, err := service.SkillUpdate(context.Background(), "demo", map[string]any{"name": "demo", "description": "Updated"}, false, options); err != nil {
		t.Fatalf("update error = %v", err)
	}
	if _, err := service.SkillFiles(context.Background(), "demo", ".", false, options); err != nil {
		t.Fatalf("file list error = %v", err)
	}
	if _, err := service.SkillFileRead(context.Background(), "demo", "README.md", options); err != nil {
		t.Fatalf("file read error = %v", err)
	}
	if value, err := service.SkillFileWrite(context.Background(), "demo", "README.md", "new content", false, options); err != nil || value.Data.(map[string]any)["verified"] != true {
		t.Fatalf("file write = %#v, error = %v", value.Data, err)
	}
	if _, err := service.SkillDelete(context.Background(), "demo", true, false, options); err != nil {
		t.Fatalf("delete error = %v", err)
	}
}
