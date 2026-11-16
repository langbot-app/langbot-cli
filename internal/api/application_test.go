package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/langbot-app/langbot-cli/internal/result"
)

func TestOperationStreamFramingAndFailures(t *testing.T) {
	for _, test := range []struct {
		name, body, failure string
		status              int
		values              []any
	}{
		{"events", ": heartbeat\r\n\r\ndata: {\"value\":\r\ndata: 42}\r\n\r\nevent: done\r\ndata: {}\r\n\r\n", "", 200, []any{map[string]any{"value": json.Number("42")}}},
		{"error", "event: error\ndata: {\"code\":\"permission_denied\",\"msg\":\"denied\"}\n\n", "server", 200, nil},
		{"disconnect", "data: 1\n\n", "network", 200, []any{json.Number("1")}},
		{"bad json", "data: not-json\n\n", "incompatible", 200, nil},
		{"denied", `{"code":"permission_denied","data":null}`, "permission", 403, nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				if request.Method != "POST" || request.URL.Path != "/api/v1/operations/agent.debug.stream/stream" {
					t.Error(request.URL)
				}
				writer.Header().Set("Content-Type", "text/event-stream")
				writer.WriteHeader(test.status)
				fmt.Fprint(writer, test.body)
			}))
			defer server.Close()
			var values []any
			err := (Client{}).OperationStream(context.Background(), Target{Endpoint: server.URL}, "agent.debug.stream", nil, func(value any) error { values = append(values, value); return nil })
			if test.failure == "" && err != nil || test.failure != "" && (err == nil || result.AsError(err).Type != test.failure) {
				t.Fatalf("error=%v want %s", err, test.failure)
			}
			if !reflect.DeepEqual(values, test.values) {
				t.Fatalf("values=%#v", values)
			}
		})
	}
}

func TestOperationCallPreservesJSONTypes(t *testing.T) {
	for _, payload := range []string{`null`, `[1,"two"]`, `42`, `"hello"`, `{"count":9007199254740993}`} {
		t.Run(payload, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				fmt.Fprintf(writer, `{"code":0,"data":%s}`, payload)
			}))
			defer server.Close()
			value, err := (Client{}).OperationCall(context.Background(), Target{Endpoint: server.URL}, "stats.basic", nil)
			encoded, _ := json.Marshal(value)
			if err != nil || string(encoded) != payload {
				t.Fatalf("value=%s err=%v", encoded, err)
			}
		})
	}
}

func TestOperationRejectsRedirectsAndInvalidIDs(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests++
		http.Redirect(writer, request, "/another", http.StatusTemporaryRedirect)
	}))
	defer server.Close()
	client := Client{}
	if _, err := client.OperationCall(context.Background(), Target{Endpoint: server.URL}, "../secrets", nil); err == nil || requests != 0 {
		t.Fatal("invalid operation reached transport")
	}
	if _, err := client.OperationCall(context.Background(), Target{Endpoint: server.URL}, "agent.list", nil); err == nil || requests != 1 {
		t.Fatal("redirect followed")
	}
}
