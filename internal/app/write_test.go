package app

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/langbot-app/langbot-cli/internal/result"
)

func TestWritePreflightRequiresSavedBoundContextAndRejectsTemporaryEndpoint(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	service := New(Dependencies{DefaultConfigPath: func() (string, error) { return path, nil }})
	if _, err := service.WritePreflight(context.Background(), "bot.create", CheckOptions{}); result.AsError(err).Kind != "precondition" {
		t.Fatalf("missing selected context error = %+v", result.AsError(err))
	}
	if _, err := service.WritePreflight(context.Background(), "bot.create", CheckOptions{ContextSet: true, Context: "missing"}); result.AsError(err).Kind != "precondition" {
		t.Fatalf("missing context error = %+v", result.AsError(err))
	}
	if _, err := service.ContextAdd("production", "http://example.test", "KEY", "workspace-a", ""); err != nil {
		t.Fatalf("ContextAdd() error = %v", err)
	}
	if _, err := service.WritePreflight(context.Background(), "bot.create", CheckOptions{ContextSet: true, Context: "production", EndpointSet: true, Endpoint: "http://other.test"}); result.AsError(err).Kind != "precondition" {
		t.Fatalf("temporary endpoint error = %+v", result.AsError(err))
	}
}

func TestWritePreflightAcceptsCurrentAndEnvironmentSelectedSavedContext(t *testing.T) {
	for _, test := range []struct {
		name       string
		useCurrent bool
		useEnv     bool
	}{
		{name: "current context", useCurrent: true},
		{name: "environment context", useEnv: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.yaml")
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/v1/system/context":
					fmt.Fprint(w, `{"code":0,"data":{"instance_uuid":"instance-a","workspace_uuid":"workspace-a","api_key_id":"key-a","permissions":["resource.manage","resource.view"]}}`)
				case "/api/v1/system/capabilities":
					fmt.Fprint(w, `{"code":0,"data":{"schema_version":1,"operations":{"bot.create":{"supported":true},"bot.get":{"supported":true}}}}`)
				default:
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer server.Close()
			lookup := func(name string) (string, bool) {
				switch name {
				case "KEY":
					return "secret", true
				case "LANGBOT_CONTEXT":
					return "production", test.useEnv
				default:
					return "", false
				}
			}
			service := New(Dependencies{LookupEnv: lookup, DefaultConfigPath: func() (string, error) { return path, nil }})
			if _, err := service.ContextAdd("production", server.URL, "KEY", "workspace-a", ""); err != nil {
				t.Fatalf("ContextAdd() error = %v", err)
			}
			if test.useCurrent {
				if _, err := service.ContextUse("production"); err != nil {
					t.Fatalf("ContextUse() error = %v", err)
				}
			}
			preflight, err := service.WritePreflight(context.Background(), "bot.create", CheckOptions{})
			if err != nil {
				t.Fatalf("WritePreflight() error = %v", err)
			}
			if preflight.Connection.Context != "production" || preflight.Connection.Temporary {
				t.Fatalf("preflight connection = %#v", preflight.Connection)
			}
		})
	}
}

func TestWritePreflightChecksIdentityPermissionAndCapabilityWithoutWrite(t *testing.T) {
	for _, test := range []struct {
		name        string
		permissions []string
		workspace   string
		operations  string
		wantKind    string
	}{
		{name: "workspace mismatch", permissions: []string{"resource.manage", "resource.view"}, workspace: "other", operations: `{"bot.create":{"supported":true},"bot.get":{"supported":true}}`, wantKind: "precondition"},
		{name: "permission", permissions: nil, workspace: "workspace-a", operations: `{"bot.create":{"supported":true}}`, wantKind: "permission"},
		{name: "readback permission", permissions: []string{"resource.manage"}, workspace: "workspace-a", operations: `{"bot.create":{"supported":true},"bot.get":{"supported":true}}`, wantKind: "permission"},
		{name: "capability unknown", permissions: []string{"resource.manage", "resource.view"}, workspace: "workspace-a", operations: `{}`, wantKind: "precondition"},
		{name: "capability false", permissions: []string{"resource.manage", "resource.view"}, workspace: "workspace-a", operations: `{"bot.create":{"supported":false},"bot.get":{"supported":true}}`, wantKind: "precondition"},
		{name: "readback capability", permissions: []string{"resource.manage", "resource.view"}, workspace: "workspace-a", operations: `{"bot.create":{"supported":true}}`, wantKind: "precondition"},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.yaml")
			var writeCalls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet {
					writeCalls.Add(1)
				}
				switch r.URL.Path {
				case "/api/v1/system/context":
					fmt.Fprintf(w, `{"code":0,"data":{"instance_uuid":"instance-a","workspace_uuid":%q,"api_key_id":"key-a","permissions":%s}}`, test.workspace, jsonArray(test.permissions))
				case "/api/v1/system/capabilities":
					fmt.Fprintf(w, `{"code":0,"data":{"schema_version":1,"operations":%s}}`, test.operations)
				default:
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer server.Close()
			lookup := func(name string) (string, bool) {
				if name == "KEY" {
					return "secret", true
				}
				return "", false
			}
			service := New(Dependencies{LookupEnv: lookup, DefaultConfigPath: func() (string, error) { return path, nil }})
			if _, err := service.ContextAdd("production", server.URL, "KEY", "workspace-a", ""); err != nil {
				t.Fatalf("ContextAdd() error = %v", err)
			}
			preflight, err := service.WritePreflight(context.Background(), "bot.create", CheckOptions{ContextSet: true, Context: "production"})
			if result.AsError(err).Kind != test.wantKind {
				t.Fatalf("WritePreflight() error = %+v, want %q", result.AsError(err), test.wantKind)
			}
			if writeCalls.Load() != 0 {
				t.Fatalf("preflight sent a write request: %d", writeCalls.Load())
			}
			if test.name == "permission" && (preflight.Identity.InstanceUUID != "instance-a" || preflight.Meta()["workspace_uuid"] != "workspace-a") {
				t.Fatalf("permission failure lost safe identity: %#v", preflight)
			}
		})
	}
}

func jsonArray(values []string) string {
	if len(values) == 0 {
		return "[]"
	}
	return `["` + strings.Join(values, `","`) + `"]`
}

func TestWritePreflightUsesSameConnectionSnapshotForSuccess(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.Method+" "+r.URL.Path)
		switch r.URL.Path {
		case "/api/v1/system/context":
			fmt.Fprint(w, `{"code":0,"data":{"instance_uuid":"instance-a","workspace_uuid":"workspace-a","api_key_id":"key-a","permissions":["resource.manage","resource.view"]}}`)
		case "/api/v1/system/capabilities":
			fmt.Fprint(w, `{"code":0,"data":{"schema_version":1,"operations":{"bot.create":{"supported":true},"bot.get":{"supported":true}}}}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	lookup := func(name string) (string, bool) {
		if name == "KEY" {
			return "secret", true
		}
		return "", false
	}
	service := New(Dependencies{LookupEnv: lookup, DefaultConfigPath: func() (string, error) { return path, nil }})
	if _, err := service.ContextAdd("production", server.URL, "KEY", "workspace-a", ""); err != nil {
		t.Fatalf("ContextAdd() error = %v", err)
	}
	preflight, err := service.WritePreflight(context.Background(), "bot.create", CheckOptions{ContextSet: true, Context: "production"})
	if err != nil {
		t.Fatalf("WritePreflight() error = %v", err)
	}
	if preflight.Identity.WorkspaceUUID != "workspace-a" || !preflight.Capabilities.Operations["bot.create"] {
		t.Fatalf("preflight = %#v", preflight)
	}
	if strings.Join(paths, "|") != "GET /api/v1/system/context|GET /api/v1/system/capabilities" {
		t.Fatalf("preflight requests = %#v", paths)
	}
}
