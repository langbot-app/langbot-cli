package integration_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func TestPluginSkillManagementCommandsEnforceSafety(t *testing.T) {
	var configWrites atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/system/context":
			fmt.Fprint(w, `{"code":0,"data":{"instance_uuid":"instance-a","workspace_uuid":"workspace-a","api_key_id":"key-a","permissions":["resource.manage","resource.view","audit.view"]}}`)
		case "/api/v1/system/capabilities":
			fmt.Fprint(w, `{"code":0,"data":{"schema_version":1,"operations":{"plugin.get":{"supported":true},"plugin.config.get":{"supported":true},"plugin.config.update":{"supported":true},"plugin.delete":{"supported":true},"skill.get":{"supported":true},"skill.update":{"supported":true}}}}`)
		case "/api/v1/plugins/author/plugin":
			fmt.Fprint(w, `{"code":0,"data":{"plugin":{"manifest":{"manifest":{"metadata":{"author":"author","name":"plugin"}}}}}}`)
		case "/api/v1/plugins/author/plugin/config":
			if r.Method == http.MethodPut {
				configWrites.Add(1)
				fmt.Fprint(w, `{"code":0,"data":{}}`)
				return
			}
			fmt.Fprint(w, `{"code":0,"data":{"config":{"token":"***","mode":"safe"}}}`)
		case "/api/v1/skills/demo":
			fmt.Fprint(w, `{"code":0,"data":{"skill":{"name":"demo"}}}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	env := map[string]string{"KEY": "connection-secret"}
	requireSuccess(t, run(t, configPath, env, "", "context", "add", "managed", "--endpoint", server.URL, "--api-key-env", "KEY", "--expect-workspace", "workspace-a"))
	requireSuccess(t, run(t, configPath, env, "", "context", "use", "managed"))

	dryRun := run(t, configPath, env, `{"token":"***","mode":"safe"}`, "plugin", "config", "update", "author", "plugin", "--file", "-", "--dry-run")
	requireSuccess(t, dryRun)
	if configWrites.Load() != 0 || strings.Contains(dryRun.out, "connection-secret") {
		t.Fatalf("config dry-run wrote or leaked credential: %s", dryRun.out)
	}

	blocked := run(t, configPath, env, "", "plugin", "delete", "author", "plugin")
	if blocked.code == 0 || blocked.data["ok"] != false {
		t.Fatalf("plugin delete without --yes was accepted: %s", blocked.out)
	}

	rename := run(t, configPath, env, `{"name":"other"}`, "skill", "update", "demo", "--file", "-")
	if rename.code == 0 || rename.data["ok"] != false {
		t.Fatalf("skill rename was accepted: %s", rename.out)
	}
}
