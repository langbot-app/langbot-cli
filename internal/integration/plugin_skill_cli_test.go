package integration_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func TestPluginPermissionReviewFlow(t *testing.T) {
	for _, mode := range []string{"pending", "approved", "old-core", "denied", "malformed"} {
		t.Run(mode, func(t *testing.T) {
			var reads atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet {
					t.Errorf("permission review attempted a write: %s", r.Method)
				}
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case "/api/v1/system/context":
					permission := "resource.view"
					if mode == "denied" {
						permission = "workspace.view"
					}
					fmt.Fprintf(w, `{"code":0,"data":{"instance_uuid":"instance-a","workspace_uuid":"workspace-a","api_key_id":"key-a","permissions":[%q]}}`, permission)
				case "/api/v1/system/capabilities":
					fmt.Fprintf(w, `{"code":0,"data":{"schema_version":1,"operations":{"plugin.permissions.get":{"supported":%t}}}}`, mode != "old-core")
				case "/api/v1/plugins/author/plugin/permissions":
					reads.Add(1)
					if mode == "malformed" {
						fmt.Fprint(w, `{"code":0,"data":{}}`)
						return
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{
						"can_manage": true, "can_confirm": false, "pending_confirmation": mode != "approved", "enabled": mode == "approved", "compatibility_mode": true,
						"private_key": "fixture-private-payload", "permissions": []any{map[string]any{"id": "sandbox", "category": "capabilities", "risk": "dangerous", "granted": mode == "approved", "grantable": false, "api_key": "fixture-private-payload"}},
					}})
				default:
					w.WriteHeader(404)
				}
			}))
			defer server.Close()
			path := filepath.Join(t.TempDir(), "config.yaml")
			env := map[string]string{"KEY": "connection-secret"}
			requireSuccess(t, run(t, path, env, "", "context", "add", "managed", "--endpoint", server.URL, "--api-key-env", "KEY", "--expect-workspace", "workspace-a"))
			requireSuccess(t, run(t, path, env, "", "context", "use", "managed"))
			got := run(t, path, env, "", "plugin", "permissions", "get", "author", "plugin")
			want := map[string]int{"pending": 0, "approved": 0, "old-core": 6, "denied": 4, "malformed": 8}[mode]
			if got.code != want || strings.Contains(got.out, "fixture-private-payload") {
				t.Fatalf("unexpected result: %s", got.out)
			}
			if (mode == "denied" || mode == "old-core") && reads.Load() != 0 {
				t.Fatal("permission read bypassed preflight")
			}
			data, _ := got.data["data"].(map[string]any)
			if mode == "pending" {
				approval, _ := data["approval"].(map[string]any)
				if approval["url"] != server.URL+"/home/extensions" || approval["workspace_uuid"] != "workspace-a" || approval["required_principal"] != "account" {
					t.Fatalf("approval handoff missing: %s", got.out)
				}
				code, human := runText(t, path, env, strings.NewReader(""), "plugin", "permissions", "get", "author", "plugin")
				if code != 0 || !strings.Contains(human, "sandbox") || !strings.Contains(human, "dangerous") || !strings.Contains(human, "/home/extensions") {
					t.Fatalf("human review omits permissions or handoff: %s", human)
				}
			}
			if mode == "approved" && data["approval"] != nil {
				t.Fatal("approved plugin still requires review")
			}
		})
	}
}

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
