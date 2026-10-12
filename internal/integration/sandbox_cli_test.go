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

func TestSandboxCommands(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	permissions := []string{"resource.view", "audit.view"}
	supported, missing, managedDenied := true, false, false
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
				fmt.Fprint(w, capabilitiesJSON("sandbox.status", "sandbox.sessions", "sandbox.errors"))
			} else {
				fmt.Fprint(w, capabilitiesJSON())
			}
		default:
			requests = append(requests, r.URL.RequestURI())
			if missing {
				writeCLIError(w, http.StatusNotFound)
				return
			}
			if managedDenied {
				w.WriteHeader(403)
				fmt.Fprint(w, `{"code":"managed_sandbox_unavailable","msg":"restricted"}`)
				return
			}
			if r.URL.Path == "/api/v1/box/sessions" || r.URL.Path == "/api/v1/box/errors" {
				fmt.Fprint(w, `{"code":0,"data":[]}`)
				return
			}
			fmt.Fprint(w, `{"code":0,"data":{"items":[],"enabled":false,"available":false,"reason":"runtime offline","text":"connection-secret"}}`)
		}
	}))
	defer server.Close()
	configureWriteContext(t, configPath, server.URL, "FEATURE_KEY")
	env := map[string]string{"FEATURE_KEY": "connection-secret"}
	for _, test := range []struct {
		args []string
		path string
	}{{[]string{"sandbox", "status"}, "/api/v1/box/status"},
		{[]string{"sandbox", "sessions"}, "/api/v1/box/sessions"},
		{[]string{"sandbox", "errors"}, "/api/v1/box/errors"}} {
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
	if got := run(t, configPath, env, "", "sandbox", "status"); got.code != 5 {
		t.Fatalf("not found: %s", got.out)
	}
	missing = false
	permissions = []string{}
	before := len(requests)
	if got := run(t, configPath, env, "", "sandbox", "status"); got.code != 4 || len(requests) != before {
		t.Fatalf("permission: %s", got.out)
	}
	permissions = []string{"resource.view", "audit.view"}
	supported = false
	if got := run(t, configPath, env, "", "sandbox", "status"); got.code != 6 || len(requests) != before {
		t.Fatalf("capability: %s", got.out)
	}
	supported = true
	permissions = []string{"resource.view"}
	if got := run(t, configPath, env, "", "sandbox", "errors"); got.code != 4 {
		t.Fatalf("audit permission: %s", got.out)
	}
	permissions = []string{"resource.view", "audit.view"}
	managedDenied = true
	if got := run(t, configPath, env, "", "sandbox", "status"); got.code != 4 || !strings.Contains(got.out, "managed_sandbox_unavailable") {
		t.Fatalf("managed admission: %s", got.out)
	}
}
