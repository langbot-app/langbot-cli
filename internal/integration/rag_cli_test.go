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

func TestRAGDiscoveryCommands(t *testing.T) {
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
				fmt.Fprint(w, capabilitiesJSON("knowledge_engine.list", "knowledge_engine.creation_schema", "knowledge_engine.retrieval_schema", "knowledge_parser.list"))
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
	}{{[]string{"knowledge-engine", "list"}, "/api/v1/knowledge/engines"},
		{[]string{"knowledge-engine", "creation-schema", "author/engine"}, "/api/v1/knowledge/engines/author/engine/creation-schema"},
		{[]string{"knowledge-engine", "retrieval-schema", "author/engine"}, "/api/v1/knowledge/engines/author/engine/retrieval-schema"},
		{[]string{"knowledge-parser", "list", "--mime-type", "text/plain"}, "/api/v1/knowledge/parsers?mime_type=text%2Fplain"}} {
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
	if got := run(t, configPath, env, "", "knowledge-engine", "list"); got.code != 5 {
		t.Fatalf("not found: %s", got.out)
	}
	missing = false
	permissions = []string{}
	before := len(requests)
	if got := run(t, configPath, env, "", "knowledge-engine", "list"); got.code != 4 || len(requests) != before {
		t.Fatalf("permission: %s", got.out)
	}
	permissions = []string{"resource.view"}
	supported = false
	if got := run(t, configPath, env, "", "knowledge-engine", "list"); got.code != 6 || len(requests) != before {
		t.Fatalf("capability: %s", got.out)
	}

}
