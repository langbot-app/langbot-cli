package command

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/langbot-app/langbot-cli/internal/api"
	"github.com/langbot-app/langbot-cli/internal/app"
	"github.com/spf13/cobra"
)

func TestExecuteIdentityAcrossReadPreflight(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests++
		if request.Header.Get("X-LangBot-Client-Command") != "knowledge-base.list" || request.UserAgent() != fmt.Sprintf("lbctl/0.4.1 (%s/%s)", runtime.GOOS, runtime.GOARCH) {
			t.Errorf("identity lost in %s", request.URL.Path)
		}
		switch request.URL.Path {
		case "/api/v1/system/context":
			fmt.Fprint(writer, `{"code":0,"data":{"instance_uuid":"instance","workspace_uuid":"workspace","api_key_id":"key","permissions":["resource.view"]}}`)
		case "/api/v1/system/capabilities":
			fmt.Fprint(writer, `{"code":0,"data":{"schema_version":1,"operations":{"knowledge_base.list":{"supported":true}}}}`)
		case "/api/v1/knowledge/bases":
			fmt.Fprint(writer, `{"code":0,"data":{"bases":[]}}`)
		default:
			t.Errorf("unexpected request %s", request.URL.Path)
			writer.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	var output bytes.Buffer
	code := executeChinese(context.Background(), []string{"--endpoint", server.URL, "kb", "list"}, Dependencies{
		Version: "0.4.1", Out: &output, Err: &output,
		DefaultConfigPath: func() (string, error) { return filepath.Join(t.TempDir(), "config.yaml"), nil },
		LookupEnv: func(key string) (string, bool) {
			if key == "LANGBOT_API_KEY" {
				return "test-key", true
			}
			return "", false
		},
	})
	if code != 0 || requests != 3 {
		t.Fatalf("code=%d requests=%d output=%s", code, requests, output.String())
	}
}

func TestCanonicalCommandIdentity(t *testing.T) {
	for _, test := range []struct {
		args []string
		want string
	}{
		{[]string{"kb", "list"}, "knowledge-base.list"},
		{[]string{"kb", "file", "list", "private-kb-name"}, "knowledge-base.file.list"},
		{[]string{"knowledge-base", "ingest", "private-kb-name", "--file", "private.pdf"}, "knowledge-base.ingest"},
		{[]string{"pipeline", "apply", "--file", "private.yaml"}, "pipeline.apply"},
		{[]string{"plugin", "install", "local", "--file", "private.lbpkg"}, "plugin.install.local"},
	} {
		t.Run(test.want, func(t *testing.T) {
			requests := 0
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				requests++
				if request.Header.Get("X-LangBot-Client-Command") != test.want || request.UserAgent() != fmt.Sprintf("lbctl/0.4.1 (%s/%s)", runtime.GOOS, runtime.GOARCH) {
					t.Errorf("wrong identity: %v", request.Header)
				}
				fmt.Fprint(writer, `{"code":0,"data":{"version":"v4.10.10"}}`)
			}))
			defer server.Close()
			deps := normalizeDependencies(Dependencies{Version: "0.4.1", Out: io.Discard, Err: io.Discard})
			root := newRoot(deps, &globalFlags{}, app.New(app.Dependencies{}))
			selected, _, err := root.Find(test.args)
			if err != nil {
				t.Fatal(err)
			}
			selected.RunE = func(cmd *cobra.Command, _ []string) error {
				for count := 0; count < 2; count++ {
					if _, err := (api.Client{}).Info(cmd.Context(), api.Target{Endpoint: server.URL}); err != nil {
						return err
					}
				}
				return nil
			}
			root.SetArgs(test.args)
			if err := root.ExecuteContext(context.Background()); err != nil {
				t.Fatal(err)
			}
			if requests != 2 {
				t.Fatalf("requests = %d, want 2", requests)
			}
		})
	}
}
