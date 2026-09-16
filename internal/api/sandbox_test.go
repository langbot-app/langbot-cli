package api

import (
	"net/http"
	"testing"
)

func TestSandboxDiagnosticsExcludeInstanceAndWriteOperations(t *testing.T) {
	for _, path := range []string{"/api/v1/box/runtime-status", "/api/v1/box/status?workspace=other"} {
		if _, ok := ResolveOperation(http.MethodGet, path); ok {
			t.Errorf("accepted %s", path)
		}
	}
	if _, ok := ResolveOperation(http.MethodPost, "/api/v1/box/sessions"); ok {
		t.Fatal("diagnostics allowed execution")
	}
}
