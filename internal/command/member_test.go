package command

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/langbot-app/langbot-cli/internal/config"
)

func memberCLI(t *testing.T, path, input string, environment map[string]string, arguments ...string) (int, string) {
	t.Helper()
	var output bytes.Buffer
	code := executeChinese(context.Background(), append([]string{"--config", path, "-o", "json"}, arguments...), Dependencies{
		In: strings.NewReader(input), Out: &output, Err: &output, Version: "test",
		LookupEnv: func(name string) (string, bool) { value, ok := environment[name]; return value, ok },
	})
	return code, output.String()
}

func memberConfig(t *testing.T, endpoint, token string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	err := (config.Store{Path: path}).Update(context.Background(), func(file *config.File) error {
		file.CurrentContext = "test"
		file.Contexts["test"] = config.Context{Endpoint: endpoint, UserToken: token, ExpectedWorkspaceUUID: "workspace", Credential: &config.Credential{Type: "env", Name: "TEST_KEY"}}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return path
}

func TestMemberCredentialSelection(t *testing.T) {
	for _, test := range []struct {
		name, saved, env string
		args             []string
		member           bool
		status           int
	}{
		{"api-default", "saved-token", "env-token", []string{"agent", "list"}, false, 200},
		{"explicit", "saved-token", "", []string{"agent", "list", "--as-member"}, true, 200},
		{"member-auto", "saved-token", "", []string{"workspace", "member", "list"}, true, 200},
		{"env-priority", "saved-token", "env-token", []string{"assistant", "list"}, true, 200},
		{"generic-member", "saved-token", "", []string{"op", "call", "provider.codex.status", "--file", "-"}, true, 200},
		{"expired", "saved-token", "", []string{"assistant", "list"}, true, 401},
		{"forbidden", "saved-token", "", []string{"assistant", "list"}, true, 403},
	} {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				calls++
				if !strings.HasPrefix(request.Header.Get("User-Agent"), "lbctl/") || request.Header.Get("X-LangBot-Client-Command") == "" {
					t.Error("missing identity headers")
				}
				if test.member {
					token := test.saved
					if test.env != "" {
						token = test.env
					}
					if request.Header.Get("Authorization") != "Bearer "+token || request.Header.Get("X-API-Key") != "" {
						t.Error("incorrect member headers")
					}
				} else if request.Header.Get("X-API-Key") != "api-secret" || request.Header.Get("Authorization") != "" {
					t.Error("API key default changed")
				}
				if operationPreflight(writer, request, true) {
					return
				}
				if request.Method == "GET" && strings.HasPrefix(request.URL.Path, "/api/v1/operations/") {
					fmt.Fprintf(writer, `{"id":%q,"risk":"read","permission":"resource.view"}`, strings.TrimPrefix(request.URL.Path, "/api/v1/operations/"))
					return
				}
				writer.WriteHeader(test.status)
				fmt.Fprint(writer, `{"code":0,"data":{"value":"ok"}}`)
			}))
			defer server.Close()
			path := memberConfig(t, server.URL, test.saved)
			environment := map[string]string{"TEST_KEY": "api-secret"}
			if test.env != "" {
				environment["LANGBOT_USER_TOKEN"] = test.env
			}
			code, output := memberCLI(t, path, `{"provider":"openai"}`, environment, test.args...)
			if test.status != 200 {
				if code != 3 || !strings.Contains(output, "requires_member_session") {
					t.Fatalf("%d %s", code, output)
				}
			} else if code != 0 {
				t.Fatalf("%d %s", code, output)
			}
			if calls == 0 || strings.Contains(output, "saved-token") || strings.Contains(output, "env-token") || strings.Contains(output, "api-secret") {
				t.Fatalf("calls=%d output=%s", calls, output)
			}
		})
	}
}

func TestMemberRequiredBeforeRequest(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("member-only command sent a request without token") }))
	defer server.Close()
	path := memberConfig(t, server.URL, "")
	for _, args := range [][]string{{"workspace", "member", "list"}, {"workspace", "invitation", "list"}, {"provider", "codex", "status", "openai"}, {"assistant", "list"}, {"pipeline", "migration", "preview"}, {"knowledge-base", "migration", "preview"}, {"op", "call", "assistant.list"}, {"agent", "list", "--as-member"}} {
		code, output := memberCLI(t, path, "", map[string]string{"TEST_KEY": "api-secret"}, args...)
		if code != 3 || !strings.Contains(output, "requires_member_session") {
			t.Fatalf("%v: %d %s", args, code, output)
		}
	}
}

func TestMemberLoginPersistenceAndLogout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/api/v1/user/auth" || request.Header.Get("Authorization") != "" || request.Header.Get("X-API-Key") != "" {
			t.Error("login request")
		}
		fmt.Fprint(writer, `{"code":0,"data":{"token":"private-session"}}`)
	}))
	defer server.Close()
	path := memberConfig(t, server.URL, "")
	code, output := memberCLI(t, path, "password\n", nil, "auth", "login", "--email", "local@example.test", "--password-stdin")
	if code != 0 || strings.Contains(output, "private-session") {
		t.Fatalf("%d %s", code, output)
	}
	file, err := (config.Store{Path: path}).Load()
	if err != nil || file.Contexts["test"].UserToken != "private-session" {
		t.Fatal("session not saved")
	}
	info, err := os.Stat(path)
	if err != nil || runtime.GOOS != "windows" && info.Mode().Perm() != 0600 {
		t.Fatal("credential mode must be 0600")
	}
	code, output = memberCLI(t, path, "", nil, "context", "show")
	if code != 0 || strings.Contains(output, "private-session") {
		t.Fatalf("%d %s", code, output)
	}
	code, output = memberCLI(t, path, "manual-session\n", nil, "auth", "set-token", "--stdin")
	if code != 0 || strings.Contains(output, "manual-session") {
		t.Fatalf("%d %s", code, output)
	}
	code, output = memberCLI(t, path, "", nil, "auth", "logout")
	if code != 0 {
		t.Fatalf("%d %s", code, output)
	}
	file, err = (config.Store{Path: path}).Load()
	if err != nil || file.Contexts["test"].UserToken != "" {
		t.Fatal("session not removed")
	}
}

func TestImageDownloadAndOperationDecode(t *testing.T) {
	image := []byte{137, 80, 78, 71, 13, 10, 26, 10, 0, 255}
	encoded := base64.StdEncoding.EncodeToString(image)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/api/v1/files/image/v1/workspace/sample.png" {
			writer.Write(image)
			return
		}
		if operationPreflight(writer, request, true) {
			return
		}
		if request.Method == "GET" {
			fmt.Fprint(writer, `{"id":"file.image.get","risk":"read","output_schema":{"properties":{"content":{"type":"string","contentEncoding":"base64"}}}}`)
			return
		}
		fmt.Fprintf(writer, `{"code":0,"data":{"content":%q,"media_type":"image/png"}}`, encoded)
	}))
	defer server.Close()
	path := memberConfig(t, server.URL, "")
	environment := map[string]string{"TEST_KEY": "api-secret"}
	destination := filepath.Join(t.TempDir(), "image.png")
	code, output := memberCLI(t, path, "", environment, "file", "image", "get", "v1/workspace/sample.png", "-O", destination)
	if code != 0 || output != "" {
		t.Fatalf("%d %s", code, output)
	}
	data, err := os.ReadFile(destination)
	if err != nil || !bytes.Equal(data, image) {
		t.Fatal("download changed bytes")
	}
	code, output = memberCLI(t, path, "", environment, "file", "image", "get", "v1/workspace/sample.png")
	if code != 0 || !bytes.Equal([]byte(output), image) {
		t.Fatal("stdout changed bytes")
	}
	code, output = memberCLI(t, path, `{"file_key":"sample"}`, environment, "op", "call", "file.image.get", "--file", "-")
	if code != 0 || !strings.Contains(output, encoded) {
		t.Fatalf("base64 JSON changed: %d %s", code, output)
	}
	code, output = memberCLI(t, path, `{"file_key":"sample"}`, environment, "op", "call", "file.image.get", "--file", "-", "--decode-binary", "content", "-O", destination)
	if code != 0 {
		t.Fatalf("%d %s", code, output)
	}
	var envelope struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal([]byte(output), &envelope); err != nil {
		t.Fatalf("invalid decoded output %q: %v", output, err)
	}
	if !reflect.DeepEqual(envelope.Data, map[string]any{"file": destination, "bytes": float64(len(image))}) {
		t.Fatalf("generic decoded output changed: %#v", envelope.Data)
	}
	data, err = os.ReadFile(destination)
	if err != nil || !bytes.Equal(data, image) {
		t.Fatal("decode changed bytes")
	}
}
