package app

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/langbot-app/langbot-cli/internal/result"
)

func TestAPIRequestChecksPermissionAndCapabilityBeforeOperation(t *testing.T) {
	tests := []struct {
		name        string
		permissions string
		operations  string
		wantKind    string
		wantCalls   int32
	}{
		{
			name:        "missing audit permission",
			permissions: `["resource.view"]`,
			operations:  `{"plugin.logs":{"supported":true}}`,
			wantKind:    "permission",
		},
		{
			name:        "missing capability",
			permissions: `["audit.view"]`,
			operations:  `{}`,
			wantKind:    "precondition",
		},
		{
			name:        "audit operation without resource view",
			permissions: `["audit.view"]`,
			operations:  `{"plugin.logs":{"supported":true}}`,
			wantCalls:   1,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var operationCalls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
				switch request.URL.Path {
				case "/api/v1/system/context":
					fmt.Fprintf(w, `{"code":0,"data":{"instance_uuid":"instance-a","workspace_uuid":"workspace-a","api_key_id":"key-a","permissions":%s}}`, test.permissions)
				case "/api/v1/system/capabilities":
					fmt.Fprintf(w, `{"code":0,"data":{"schema_version":1,"operations":%s}}`, test.operations)
				case "/api/v1/plugins/author/plugin/logs":
					operationCalls.Add(1)
					fmt.Fprint(w, `{"code":0,"data":{"logs":[]}}`)
				default:
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer server.Close()

			configPath := filepath.Join(t.TempDir(), "config.yaml")
			service := New(Dependencies{
				LookupEnv: func(name string) (string, bool) {
					if name == "KEY" {
						return "connection-secret", true
					}
					return "", false
				},
				DefaultConfigPath: func() (string, error) { return configPath, nil },
			})
			if _, err := service.ContextAdd("production", server.URL, "KEY", "workspace-a", ""); err != nil {
				t.Fatalf("ContextAdd() error = %v", err)
			}
			_, err := service.APIRequest(
				context.Background(),
				http.MethodGet,
				"/api/v1/plugins/author/plugin/logs",
				nil,
				CheckOptions{ContextSet: true, Context: "production"},
			)
			if test.wantKind == "" {
				if err != nil {
					t.Fatalf("APIRequest() error = %v", err)
				}
			} else if failure := result.AsError(err); failure.Kind != test.wantKind {
				t.Fatalf("APIRequest() error = %+v, want kind %q", failure, test.wantKind)
			}
			if operationCalls.Load() != test.wantCalls {
				t.Fatalf("operation calls = %d, want %d", operationCalls.Load(), test.wantCalls)
			}
		})
	}
}
