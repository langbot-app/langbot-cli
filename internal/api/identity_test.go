package api

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"

	"github.com/langbot-app/langbot-cli/internal/clientidentity"
)

func TestClientIdentityHeaders(t *testing.T) {
	for _, test := range []struct{ version, command, wantCommand string }{
		{"0.4.1", "knowledge-base.file.ingest", "knowledge-base.file.ingest"},
		{"", "pipeline.apply", "pipeline.apply"},
		{"0.4.1", "plugin.install.local", "plugin.install.local"},
		{"0.4.1", "", ""},
		{"0.4.1", "kb list --file secret.pdf", ""},
		{"0.4.1", "KB.list", ""},
		{"0.4.1", "kb..list", ""},
		{"0.4.1", "a.b.c.d.e.f.g", ""},
		{"0.4.1", strings.Repeat("a", 65), ""},
		{"0.4.1", "kb.list\r\nSecret: value", ""},
	} {
		t.Run(test.command, func(t *testing.T) {
			requests := 0
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				requests++
				wantUA := fmt.Sprintf("lbctl/%s (%s/%s)", clientidentity.Version(test.version), runtime.GOOS, runtime.GOARCH)
				if request.UserAgent() != wantUA || request.Header.Get("X-LangBot-Client-Command") != test.wantCommand {
					t.Errorf("identity headers = %q, %q", request.UserAgent(), request.Header.Get("X-LangBot-Client-Command"))
				}
				if test.wantCommand == "" && request.Header.Values("X-LangBot-Client-Command") != nil {
					t.Error("invalid command header must be absent")
				}
				fmt.Fprint(writer, `{"code":0,"data":{"version":"v4.10.10","edition":"community"}}`)
			}))
			defer server.Close()
			ctx := clientidentity.WithContext(context.Background(), test.version, test.command)
			for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete} {
				_, err := (Client{}).doWithContentType(ctx, Target{Endpoint: server.URL}, method, "/api/v1/test", strings.NewReader("payload"), false, "multipart/form-data; boundary=test")
				if err != nil {
					t.Fatal(err)
				}
			}
			if requests != 5 {
				t.Fatalf("requests = %d, want 5", requests)
			}
		})
	}
}
