package integration_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func TestMonitoringCommands(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	permissions := []string{"resource.view"}
	supported, missing := true, false
	var requests []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("unexpected mutation %s", r.Method)
			w.WriteHeader(405)
			return
		}
		switch r.URL.Path {
		case "/api/v1/system/context":
			p, _ := json.Marshal(permissions)
			fmt.Fprintf(w, `{"code":0,"data":{"instance_uuid":"instance-a","workspace_uuid":"workspace-a","api_key_id":"key-a","permissions":%s}}`, p)
		case "/api/v1/system/capabilities":
			if supported {
				fmt.Fprint(w, capabilitiesJSON("monitoring.messages", "monitoring.llm_calls", "monitoring.tool_calls", "monitoring.embedding_calls", "monitoring.sessions", "monitoring.errors", "monitoring.message_details", "monitoring.session_analysis"))
			} else {
				fmt.Fprint(w, capabilitiesJSON())
			}
		default:
			requests = append(requests, r.URL.RequestURI())
			if missing {
				writeCLIError(w, http.StatusNotFound)
				return
			}
			fmt.Fprint(w, `{"code":0,"data":{"items":[],"text":"connection-secret"}}`)
		}
	}))
	defer server.Close()
	configureWriteContext(t, configPath, server.URL, "FEATURE_KEY")
	env := map[string]string{"FEATURE_KEY": "connection-secret"}
	for _, test := range []struct {
		args []string
		path string
	}{{[]string{"monitoring", "messages", "--pipeline", "p", "--session", "s", "--limit", "2"}, "/api/v1/monitoring/messages?limit=2&offset=0&pipelineId=p&sessionId=s"},
		{[]string{"monitoring", "llm-calls"}, "/api/v1/monitoring/llm-calls?limit=100&offset=0"},
		{[]string{"monitoring", "tool-calls", "--session", "s"}, "/api/v1/monitoring/tool-calls?limit=100&offset=0&sessionId=s"},
		{[]string{"monitoring", "embedding-calls", "--knowledge-base", "kb"}, "/api/v1/monitoring/embedding-calls?knowledgeBaseId=kb&limit=100&offset=0"},
		{[]string{"monitoring", "sessions", "--active", "true"}, "/api/v1/monitoring/sessions?isActive=true&limit=100&offset=0"},
		{[]string{"monitoring", "errors"}, "/api/v1/monitoring/errors?limit=100&offset=0"},
		{[]string{"monitoring", "message", "m"}, "/api/v1/monitoring/messages/m/details"},
		{[]string{"monitoring", "session", "s", "--bot", "b"}, "/api/v1/monitoring/sessions/s/analysis?botId=b"}} {
		got := run(t, configPath, env, "", test.args...)
		requireSuccess(t, got)
		if strings.Contains(got.out, "connection-secret") {
			t.Fatalf("secret leaked: %s", got.out)
		}
		if requests[len(requests)-1] != test.path {
			t.Fatalf("request=%s want=%s", requests[len(requests)-1], test.path)
		}
	}
	missing = true
	if got := run(t, configPath, env, "", "monitoring", "messages", "--pipeline", "p", "--session", "s", "--limit", "2"); got.code != 5 {
		t.Fatalf("not found: %s", got.out)
	}
	missing = false
	permissions = []string{}
	before := len(requests)
	if got := run(t, configPath, env, "", "monitoring", "messages", "--pipeline", "p", "--session", "s", "--limit", "2"); got.code != 4 || len(requests) != before {
		t.Fatalf("permission: %s", got.out)
	}
	permissions = []string{"resource.view"}
	supported = false
	if got := run(t, configPath, env, "", "monitoring", "messages", "--pipeline", "p", "--session", "s", "--limit", "2"); got.code != 6 || len(requests) != before {
		t.Fatalf("capability: %s", got.out)
	}
	if got := run(t, configPath, env, "", "monitoring", "messages", "--limit", "501"); got.code != 2 {
		t.Fatalf("invalid limit: %s", got.out)
	}
}
