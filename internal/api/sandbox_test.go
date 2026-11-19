package api

import (
	"context"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/langbot-app/langbot-cli/internal/result"
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

func TestSandboxResetRequiresExplicitBooleanAcknowledgement(t *testing.T) {
	for _, test := range []struct {
		name, action, payload string
		wantSuccess           bool
	}{
		{name: "acknowledged", payload: `{"reset":true,"api_key":"private-key"}`, wantSuccess: true},
		{name: "missing", payload: `{}`},
		{name: "false", payload: `{"reset":false}`},
		{name: "string", payload: `{"reset":"true"}`},
		{name: "number", payload: `{"reset":1}`},
		{name: "null", payload: `null`},
		{name: "array", payload: `[]`},
		{name: "update cannot accept reset acknowledgement", action: "update", payload: `{"reset":true}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			action := test.action
			if action == "" {
				action = "reset"
			}
			calls := 0
			client := Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
				calls++
				method := http.MethodDelete
				if action == "update" {
					method = http.MethodPut
				}
				if request.Method != method || request.URL.Path != workspaceSandboxPath || request.URL.RawQuery != "" {
					t.Fatalf("unexpected sandbox write %s %s", request.Method, request.URL)
				}
				return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"X-Request-Id": []string{"sandbox-reset-request"}}, Body: io.NopCloser(strings.NewReader(`{"code":0,"data":` + test.payload + `}`))}, nil
			})}
			value, err := client.WorkspaceSandbox(context.Background(), Target{Endpoint: "http://example.test", APIKey: "private-key"}, action, false, nil)
			if calls != 1 {
				t.Fatalf("sandbox writes = %d, want 1", calls)
			}
			if test.wantSuccess {
				if err != nil || !reflect.DeepEqual(value, map[string]any{"reset": true}) {
					t.Fatalf("reset acknowledgement = %#v, error=%v", value, err)
				}
				return
			}
			failure := result.AsError(err)
			if failure == nil || failure.Type != "result_unknown" || failure.HTTPStatus != http.StatusOK {
				t.Fatalf("invalid acknowledgement result = %#v, error=%#v", value, failure)
			}
			if action == "reset" && failure.RequestID != "sandbox-reset-request" {
				t.Fatalf("reset acknowledgement lost request ID: %#v", failure)
			}
		})
	}
}
