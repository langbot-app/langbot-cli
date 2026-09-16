package integration_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
)

func TestPipelineExtensionReplacementAndReadback(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	permissions := []string{"resource.view", "resource.manage"}
	supported, mismatch := true, false
	body := `{"bound_plugins":[],"bound_mcp_servers":[],"bound_skills":[],"bound_mcp_resources":[],"enable_all_plugins":false,"enable_all_mcp_servers":false,"enable_all_skills":false,"mcp_resource_agent_read_enabled":false}`
	writes, reads := 0, 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/system/context":
			p, _ := json.Marshal(permissions)
			fmt.Fprintf(w, `{"code":0,"data":{"instance_uuid":"instance-a","workspace_uuid":"workspace-a","api_key_id":"key-a","permissions":%s}}`, p)
		case "/api/v1/system/capabilities":
			if supported {
				fmt.Fprint(w, capabilitiesJSON("pipeline.extensions.get", "pipeline.extensions.update"))
			} else {
				fmt.Fprint(w, capabilitiesJSON("pipeline.extensions.update"))
			}
		case "/api/v1/pipelines/p/extensions":
			if r.Method == http.MethodPut {
				var value map[string]any
				_ = json.NewDecoder(r.Body).Decode(&value)
				encoded, _ := json.Marshal(value)
				body = string(encoded)
				writes++
			}
			if r.Method == http.MethodGet {
				reads++
				if mismatch {
					fmt.Fprint(w, `{"code":0,"data":{}}`)
					return
				}
			}
			fmt.Fprintf(w, `{"code":0,"data":%s}`, body)
		default:
			writeCLIError(w, http.StatusNotFound)
		}
	}))
	defer server.Close()
	configureWriteContext(t, configPath, server.URL, "EXT_KEY")
	env := map[string]string{"EXT_KEY": "connection-secret"}
	requireSuccess(t, run(t, configPath, env, "", "pipeline", "extensions", "get", "p"))
	incomplete := writeBodyFile(t, `{"bound_plugins":[]}`)
	if got := run(t, configPath, env, "", "pipeline", "extensions", "update", "p", "--file", incomplete); got.code != 2 || writes != 0 {
		t.Fatalf("incomplete replacement: %s", got.out)
	}
	complete := writeBodyFile(t, body)
	requireSuccess(t, run(t, configPath, env, "", "pipeline", "extensions", "update", "p", "--file", complete, "--dry-run"))
	if writes != 0 {
		t.Fatal("dry-run mutated")
	}
	requireSuccess(t, run(t, configPath, env, "", "pipeline", "extensions", "update", "p", "--file", complete))
	if writes != 1 || reads != 2 {
		t.Fatalf("missing write/readback: writes=%d reads=%d", writes, reads)
	}
	mismatch = true
	if got := run(t, configPath, env, "", "pipeline", "extensions", "update", "p", "--file", complete); got.code == 0 {
		t.Fatal("accepted mismatched readback")
	}
	permissions = []string{"resource.view"}
	before := writes
	if got := run(t, configPath, env, "", "pipeline", "extensions", "update", "p", "--file", complete); got.code != 4 || writes != before {
		t.Fatalf("permission: %s", got.out)
	}
	permissions = []string{"resource.view", "resource.manage"}
	supported = false
	if got := run(t, configPath, env, "", "pipeline", "extensions", "update", "p", "--file", complete); got.code != 6 || writes != before {
		t.Fatalf("missing readback capability: %s", got.out)
	}
}
