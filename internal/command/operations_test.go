package command

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"go.yaml.in/yaml/v3"
)

type operationTransport func(*http.Request) (*http.Response, error)

func (transport operationTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	return transport(request)
}

func operationCLI(t *testing.T, server *httptest.Server, input string, arguments ...string) (int, string) {
	return operationCLITransport(t, server, input, nil, arguments...)
}

func operationCLITransport(t *testing.T, server *httptest.Server, input string, transport http.RoundTripper, arguments ...string) (int, string) {
	t.Helper()
	return operationCLIContext(t, context.Background(), server, input, transport, arguments...)
}

func operationCLIContext(t *testing.T, ctx context.Context, server *httptest.Server, input string, transport http.RoundTripper, arguments ...string) (int, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	configuration := fmt.Sprintf("version: 1\ncurrent_context: test\ncontexts:\n  test:\n    endpoint: %s\n    expected_workspace_uuid: workspace\n    credential:\n      type: env\n      name: TEST_KEY\n", server.URL)
	if err := os.WriteFile(path, []byte(configuration), 0600); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	code := executeChinese(ctx, append([]string{"--config", path, "-o", "json"}, arguments...), Dependencies{
		In: strings.NewReader(input), Out: &output, Err: &output, Version: "test",
		Transport: transport,
		LookupEnv: func(name string) (string, bool) {
			return "test-secret", name == "TEST_KEY" || name == "LANGBOT_USER_TOKEN"
		},
	})
	return code, output.String()
}

func decodeOperationStream(t *testing.T, output, format string) []map[string]any {
	t.Helper()
	var decoder interface{ Decode(any) error } = json.NewDecoder(strings.NewReader(output))
	if format == "yaml" {
		decoder = yaml.NewDecoder(strings.NewReader(output))
	}
	envelopes := []map[string]any{}
	for {
		var envelope map[string]any
		err := decoder.Decode(&envelope)
		if err == io.EOF {
			return envelopes
		}
		if err != nil {
			t.Fatalf("invalid %s stream output %q: %v", format, output, err)
		}
		envelopes = append(envelopes, envelope)
	}
}

func TestAgentDebugMessageAndFileInputsUseExistingStream(t *testing.T) {
	message := " 你好，Agent🙂\n第二行\n "
	for _, test := range []struct{ name, source, format string }{
		{"Unicode multiline JSON", "message", "json"},
		{"Unicode multiline YAML", "message", "yaml"},
		{"message with credential stdin", "message-key-stdin", "json"},
		{"complex event JSON stdin", "file", "json"},
		{"complex event YAML file", "yaml-file", "yaml"},
		{"generic stream complex event", "generic", "json"},
	} {
		t.Run(test.name, func(t *testing.T) {
			want := map[string]any{"agent_uuid": "agent", "payload": map[string]any{"text": message}}
			args := []string{"agent", "debug", "agent", "--yes", "-o", test.format}
			input := ""
			switch test.source {
			case "message":
				args = append(args, "--message", message)
			case "message-key-stdin":
				args = append(args, "--message", message, "--api-key-stdin")
				input = "test-secret\n"
			default:
				want["payload"] = map[string]any{"event_type": "custom.event", "event_data": map[string]any{"text": "复杂事件", "count": float64(2)}, "event_context": map[string]any{"user_id": "user-a"}}
				encoded, err := json.Marshal(want)
				if err != nil {
					t.Fatal(err)
				}
				input = string(encoded)
				filename := "-"
				if test.source == "yaml-file" {
					encoded, err := yaml.Marshal(want)
					if err != nil {
						t.Fatal(err)
					}
					filename = filepath.Join(t.TempDir(), "event.yaml")
					if err := os.WriteFile(filename, encoded, 0o600); err != nil {
						t.Fatal(err)
					}
					input = ""
				}
				if test.source == "generic" {
					args = []string{"op", "stream", "agent.debug.stream", "--yes", "-o", test.format}
				}
				args = append(args, "--file", filename)
			}
			var streamCalls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				if request.Header.Get("X-API-Key") != "test-secret" || request.Header.Get("Authorization") != "" {
					t.Error("Agent debug lost the API credential")
				}
				if operationPreflight(writer, request, true) {
					return
				}
				if request.Method == http.MethodGet {
					fmt.Fprint(writer, `{"id":"agent.debug.stream","risk":"write","permission":"runtime.operate","streaming":true}`)
					return
				}
				if request.Method != http.MethodPost || request.URL.Path != "/api/v1/operations/agent.debug.stream/stream" {
					t.Errorf("unexpected Agent debug request %s %s", request.Method, request.URL.Path)
					writer.WriteHeader(http.StatusNotFound)
					return
				}
				streamCalls.Add(1)
				var body map[string]any
				if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				if !reflect.DeepEqual(body, want) {
					t.Errorf("Agent debug body = %#v, want %#v", body, want)
				}
				writer.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(writer, "data: {\"kind\":\"result\",\"data\":{\"text\":\"回应🙂\"}}\n\nevent: done\ndata: {}\n\n")
			}))
			defer server.Close()
			code, output := operationCLI(t, server, input, args...)
			envelopes := decodeOperationStream(t, output, test.format)
			if code != 0 || streamCalls.Load() != 1 || len(envelopes) != 2 {
				t.Fatalf("code=%d stream calls=%d output=%s", code, streamCalls.Load(), output)
			}
			first, last := envelopes[0], envelopes[1]
			if first["ok"] != true || first["data"].(map[string]any)["data"].(map[string]any)["text"] != "回应🙂" || last["ok"] != true || last["data"].(map[string]any)["completed"] != true {
				t.Fatalf("Agent debug stream envelopes = %#v", envelopes)
			}
		})
	}
}

func TestAgentDebugValidatesSourcesAndUsesExistingAuthorization(t *testing.T) {
	for _, test := range []struct {
		name, input      string
		flags            []string
		code, requests   int
		permissionDenied bool
	}{
		{name: "missing source", flags: []string{"--yes"}, code: 2},
		{name: "empty message", flags: []string{"--message", "", "--yes"}, code: 2},
		{name: "blank message", flags: []string{"--message", " \n\u2003", "--yes"}, code: 2},
		{name: "empty file", flags: []string{"--file", "", "--yes"}, code: 2},
		{name: "both sources", flags: []string{"--message", "hello", "--file", "-", "--yes"}, input: "invalid JSON", code: 2},
		{name: "explicit empty message with file", flags: []string{"--message", "", "--file", "-", "--yes"}, input: `{}`, code: 2},
		{name: "stdin conflict", flags: []string{"--file", "-", "--api-key-stdin", "--yes"}, input: `{}`, code: 2},
		{name: "message confirmation", flags: []string{"--message", "hello"}, code: 6, requests: 2},
		{name: "file confirmation", flags: []string{"--file", "-"}, input: `{"payload":{"text":"hello"}}`, code: 6, requests: 2},
		{name: "runtime permission", flags: []string{"--message", "hello", "--yes"}, code: 4, requests: 2, permissionDenied: true},
		{name: "file identity mismatch", flags: []string{"--file", "-", "--yes"}, input: `{"agent_uuid":"other","payload":{"text":"hello"}}`, code: 2},
	} {
		t.Run(test.name, func(t *testing.T) {
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				requests.Add(1)
				if test.permissionDenied && request.URL.Path == "/api/v1/system/context" {
					fmt.Fprint(writer, `{"code":0,"data":{"instance_uuid":"instance","workspace_uuid":"workspace","api_key_id":"key","permissions":["resource.view"]}}`)
					return
				}
				if operationPreflight(writer, request, true) {
					return
				}
				t.Errorf("invalid Agent debug request reached Core: %s %s", request.Method, request.URL.Path)
				writer.WriteHeader(http.StatusInternalServerError)
			}))
			defer server.Close()
			args := append([]string{"agent", "debug", "agent"}, test.flags...)
			code, output := operationCLI(t, server, test.input, args...)
			envelopes := decodeOperationStream(t, output, "json")
			if code != test.code || int(requests.Load()) != test.requests || len(envelopes) != 1 || envelopes[0]["ok"] != false {
				t.Fatalf("code=%d requests=%d output=%s", code, requests.Load(), output)
			}
		})
	}
}

type cancelAfterResponseRead struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (body cancelAfterResponseRead) Read(data []byte) (int, error) {
	n, err := body.ReadCloser.Read(data)
	if n > 0 {
		body.cancel()
	}
	return n, err
}

func TestAgentDebugCancellationAndTimeoutDoNotRetry(t *testing.T) {
	for _, mode := range []string{"cancel", "timeout"} {
		t.Run(mode, func(t *testing.T) {
			var streamCalls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				if operationPreflight(writer, request, true) {
					return
				}
				if request.Method != http.MethodPost || request.URL.Path != "/api/v1/operations/agent.debug.stream/stream" {
					t.Errorf("unexpected Agent debug request %s %s", request.Method, request.URL.Path)
					writer.WriteHeader(http.StatusNotFound)
					return
				}
				streamCalls.Add(1)
				writer.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(writer, "data: {\"kind\":\"result\",\"data\":{\"text\":\"partial\"}}\n\n")
				writer.(http.Flusher).Flush()
				<-request.Context().Done()
			}))
			defer server.Close()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var transport http.RoundTripper
			args := []string{"agent", "debug", "agent", "--message", "hello", "--yes"}
			if mode == "cancel" {
				transport = operationTransport(func(request *http.Request) (*http.Response, error) {
					response, err := http.DefaultTransport.RoundTrip(request)
					if err == nil && strings.HasSuffix(request.URL.Path, "/stream") {
						response.Body = cancelAfterResponseRead{ReadCloser: response.Body, cancel: cancel}
					}
					return response, err
				})
			} else {
				args = append(args, "--timeout", "200ms")
			}
			code, output := operationCLIContext(t, ctx, server, "", transport, args...)
			envelopes := decodeOperationStream(t, output, "json")
			if code != 7 || streamCalls.Load() != 1 || len(envelopes) != 2 {
				t.Fatalf("code=%d stream calls=%d output=%s", code, streamCalls.Load(), output)
			}
			last := envelopes[len(envelopes)-1]
			if last["ok"] != false || last["data"].(map[string]any)["completed"] != false || last["error"].(map[string]any)["type"] != "network" {
				t.Fatalf("interrupted Agent debug output = %#v", last)
			}
		})
	}
}

func TestSkillPackageDownloadWritesArchiveAndRetainsPublicationMetadata(t *testing.T) {
	var archive bytes.Buffer
	zipWriter := zip.NewWriter(&archive)
	file, err := zipWriter.Create("SKILL.md")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(file, "# Demo\n\nInstructions.\n"); err != nil {
		t.Fatal(err)
	}
	if err := zipWriter.Close(); err != nil {
		t.Fatal(err)
	}
	encoded := base64.StdEncoding.EncodeToString(archive.Bytes())
	checksum := fmt.Sprintf("%x", sha256.Sum256(archive.Bytes()))
	for _, test := range []struct{ outputFlag, format string }{{"", "json"}, {"-O", "json"}, {"--out-file", "json"}, {"-O", "yaml"}} {
		t.Run("flag="+test.outputFlag+"/format="+test.format, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				if operationPreflight(writer, request, true) {
					return
				}
				if request.Method != http.MethodPost || request.URL.Path != "/api/v1/operations/skill.package.get" {
					t.Errorf("unexpected request %s %s", request.Method, request.URL.Path)
					writer.WriteHeader(http.StatusNotFound)
					return
				}
				calls++
				var body map[string]any
				if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				if !reflect.DeepEqual(body, map[string]any{"skill_name": "demo", "revision": "sha256:r1"}) {
					t.Errorf("package request body = %#v", body)
				}
				fmt.Fprintf(writer, `{"code":0,"data":{"content":%q,"revision":"sha256:r1","sha256":%q,"filename":"demo.zip","manifest":{"name":"demo"}}}`, encoded, checksum)
			}))
			defer server.Close()
			destination := filepath.Join(t.TempDir(), "demo.zip")
			args := []string{"skill", "package", "get", "demo", "--file", "-", "-o", test.format}
			if test.outputFlag != "" {
				if err := os.WriteFile(destination, []byte("previous package"), 0o600); err != nil {
					t.Fatal(err)
				}
				args = append(args, test.outputFlag, destination)
			}
			code, output := operationCLI(t, server, `{"revision":"sha256:r1"}`, args...)
			var envelope struct {
				OK   bool           `json:"ok" yaml:"ok"`
				Data map[string]any `json:"data" yaml:"data"`
			}
			unmarshal := json.Unmarshal
			if test.format == "yaml" {
				unmarshal = yaml.Unmarshal
			}
			if err := unmarshal([]byte(output), &envelope); err != nil {
				t.Fatalf("invalid structured output %q: %v", output, err)
			}
			if code != 0 || !envelope.OK || calls != 1 {
				t.Fatalf("code=%d calls=%d output=%s", code, calls, output)
			}
			data := envelope.Data
			if data["revision"] != "sha256:r1" || data["sha256"] != checksum || data["filename"] != "demo.zip" || data["manifest"].(map[string]any)["name"] != "demo" {
				t.Fatalf("publication metadata = %#v", data)
			}
			if test.outputFlag == "" {
				if data["content"] != encoded || data["file"] != nil || data["bytes"] != nil {
					t.Fatalf("default package response changed: %#v", data)
				}
				return
			}
			if data["file"] != destination || fmt.Sprint(data["bytes"]) != fmt.Sprint(archive.Len()) || data["content"] != nil {
				t.Fatalf("saved package output = %#v", data)
			}
			saved, err := os.ReadFile(destination)
			if err != nil || !bytes.Equal(saved, archive.Bytes()) {
				t.Fatalf("package bytes changed: %v", err)
			}
			entries, err := os.ReadDir(filepath.Dir(destination))
			if err != nil || len(entries) != 1 || entries[0].Name() != "demo.zip" {
				t.Fatalf("package output left temporary files: %#v, error=%v", entries, err)
			}
		})
	}
}

func TestSkillPackageDownloadFailurePreservesDestination(t *testing.T) {
	for _, test := range []struct {
		name        string
		content     any
		destination string
		wantCode    int
		wantType    string
	}{
		{name: "invalid base64", content: "YWJj!!", wantCode: 8, wantType: "incompatible"},
		{name: "missing content", wantCode: 2, wantType: "input"},
		{name: "wrong content type", content: 123, wantCode: 2, wantType: "input"},
		{name: "missing parent directory", content: "YWJj", destination: "missing", wantCode: 2, wantType: "input"},
		{name: "destination is directory", content: "YWJj", destination: "directory", wantCode: 2, wantType: "input"},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				if operationPreflight(writer, request, true) {
					return
				}
				payload := map[string]any{"revision": "sha256:r1"}
				if test.content != nil {
					payload["content"] = test.content
				}
				json.NewEncoder(writer).Encode(map[string]any{"code": 0, "data": payload})
			}))
			defer server.Close()
			directory := t.TempDir()
			original := filepath.Join(directory, "demo.zip")
			if err := os.WriteFile(original, []byte("previous package"), 0o600); err != nil {
				t.Fatal(err)
			}
			destination := original
			switch test.destination {
			case "missing":
				destination = filepath.Join(directory, "missing", "demo.zip")
			case "directory":
				destination = directory
			}
			code, output := operationCLI(t, server, "", "skill", "package", "get", "demo", "-O", destination)
			var envelope struct {
				OK    bool `json:"ok"`
				Error struct {
					Type string `json:"type"`
				} `json:"error"`
			}
			if err := json.Unmarshal([]byte(output), &envelope); err != nil {
				t.Fatalf("invalid structured output %q: %v", output, err)
			}
			if code != test.wantCode || envelope.OK || envelope.Error.Type != test.wantType {
				t.Fatalf("code=%d output=%s", code, output)
			}
			saved, err := os.ReadFile(original)
			if err != nil || string(saved) != "previous package" {
				t.Fatalf("failed download changed previous package: %q, error=%v", saved, err)
			}
			entries, err := os.ReadDir(directory)
			if err != nil || len(entries) != 1 || entries[0].Name() != "demo.zip" {
				t.Fatalf("failed download left files: %#v, error=%v", entries, err)
			}
		})
	}
}

type cancelOnResponseClose struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (body cancelOnResponseClose) Close() error {
	err := body.ReadCloser.Close()
	body.cancel()
	return err
}

func TestSkillPackageDownloadCancellationPreservesDestination(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if operationPreflight(writer, request, true) {
			return
		}
		fmt.Fprint(writer, `{"code":0,"data":{"content":"YWJj","revision":"sha256:r1"}}`)
	}))
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	transport := operationTransport(func(request *http.Request) (*http.Response, error) {
		response, err := http.DefaultTransport.RoundTrip(request)
		if err == nil && request.URL.Path == "/api/v1/operations/skill.package.get" {
			response.Body = cancelOnResponseClose{ReadCloser: response.Body, cancel: cancel}
		}
		return response, err
	})
	directory := t.TempDir()
	destination := filepath.Join(directory, "demo.zip")
	if err := os.WriteFile(destination, []byte("previous package"), 0o600); err != nil {
		t.Fatal(err)
	}
	code, output := operationCLIContext(t, ctx, server, "", transport, "skill", "package", "get", "demo", "-O", destination)
	var envelope struct {
		OK    bool `json:"ok"`
		Error struct {
			Type string `json:"type"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(output), &envelope); err != nil {
		t.Fatalf("invalid structured output %q: %v", output, err)
	}
	if code != 7 || envelope.OK || envelope.Error.Type != "network" {
		t.Fatalf("cancelled download code=%d output=%s", code, output)
	}
	saved, err := os.ReadFile(destination)
	if err != nil || string(saved) != "previous package" {
		t.Fatalf("cancelled download changed previous package: %q, error=%v", saved, err)
	}
	entries, err := os.ReadDir(directory)
	if err != nil || len(entries) != 1 || entries[0].Name() != "demo.zip" {
		t.Fatalf("cancelled download left files: %#v, error=%v", entries, err)
	}
}

func operationPreflight(writer http.ResponseWriter, request *http.Request, supported bool) bool {
	switch request.URL.Path {
	case "/api/v1/workspaces/current":
		fmt.Fprint(writer, `{"code":0,"data":{"workspace":{"uuid":"workspace"},"permissions":["resource.view","resource.manage","runtime.operate","provider_secret.manage","workspace.view","workspace.update","api_key.manage","audit.view","data.export","member.view","member.invite","member.update_role","member.remove"]}}`)
	case "/api/v1/system/context":
		fmt.Fprint(writer, `{"code":0,"data":{"instance_uuid":"instance","workspace_uuid":"workspace","api_key_id":"key","permissions":["resource.view","resource.manage","runtime.operate","provider_secret.manage","workspace.view","workspace.update","api_key.manage","audit.view","data.export","member.view","member.invite","member.update_role","member.remove"]}}`)
	case "/api/v1/system/capabilities":
		fmt.Fprintf(writer, `{"code":0,"data":{"schema_version":1,"operations":{"operation.invoke":{"supported":%t},"operation.stream":{"supported":%t},"plugin.permissions.confirm_api_key":{"supported":%t},"plugin.install.local":{"supported":true},"plugin.install.github":{"supported":true},"plugin.install.marketplace":{"supported":true},"task.get":{"supported":true}}}}`, supported, supported, supported)
	default:
		return false
	}
	return true
}

func TestEveryFriendlyOperationMapping(t *testing.T) {
	for _, spec := range operationCommands() {
		t.Run(spec.path, func(t *testing.T) {
			calls := 0
			want := map[string]any{"filter": "sample", "limit": float64(3)}
			arguments := strings.Fields(spec.path)
			for _, field := range strings.Fields(spec.arguments) {
				arguments = append(arguments, "value-"+field)
				want[field] = "value-" + field
			}
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				if got := request.Header.Get("X-LangBot-Client-Command"); got != strings.ReplaceAll(spec.path, " ", ".") {
					t.Errorf("command identity = %q", got)
				}
				if memberOperation(spec.id) {
					if request.Header.Get("Authorization") != "Bearer test-secret" || request.Header.Get("X-API-Key") != "" {
						t.Error("missing member credential")
					}
				} else if request.Header.Get("X-API-Key") != "test-secret" {
					t.Error("missing credential")
				}
				if operationPreflight(writer, request, true) {
					return
				}
				path := "/api/v1/operations/" + spec.id
				if request.Method == http.MethodGet && request.URL.Path == path {
					json.NewEncoder(writer).Encode(map[string]any{"id": spec.id, "risk": "read", "streaming": spec.stream})
					return
				}
				if spec.stream {
					path += "/stream"
				}
				if request.Method != http.MethodPost || request.URL.Path != path {
					t.Errorf("unexpected %s %s", request.Method, request.URL.Path)
					writer.WriteHeader(404)
					return
				}
				calls++
				var body map[string]any
				if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				if !reflect.DeepEqual(body, want) {
					t.Errorf("body = %#v, want %#v", body, want)
				}
				if spec.stream {
					writer.Header().Set("Content-Type", "text/event-stream")
					fmt.Fprint(writer, "data: {\"message\":\"hello\"}\n\nevent: done\ndata: {}\n\n")
				} else {
					fmt.Fprint(writer, `{"code":0,"data":[{"value":"ok"}]}`)
				}
			}))
			defer server.Close()
			code, output := operationCLI(t, server, `{"filter":"sample","limit":3}`, append(arguments, "--file", "-", "--yes")...)
			if code != 0 || calls != 1 {
				t.Fatalf("code=%d calls=%d output=%s", code, calls, output)
			}
		})
	}
}

func TestFriendlyOperationContracts(t *testing.T) {
	seen := map[string]bool{}
	for _, spec := range operationCommands() {
		if seen[spec.path] {
			t.Errorf("duplicate command %s", spec.path)
		}
		seen[spec.path] = true
		details, exists := friendlyOperationDetails[spec.id]
		if !exists || details.permission == "" || details.risk == "" || details.streaming != spec.stream {
			t.Errorf("missing/mismatched contract %s", spec.id)
		}
		for _, field := range strings.Fields(spec.arguments) {
			if !strings.Contains(" "+details.fields+" ", " "+field+" ") {
				t.Errorf("%s has unknown input %s", spec.id, field)
			}
		}
	}
}

func TestGenericStreamUsesKnownContractWhenCatalogHidesIt(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if operationPreflight(writer, request, true) {
			return
		}
		if request.Method == http.MethodGet {
			writer.WriteHeader(404)
			fmt.Fprint(writer, `{"code":"unknown_operation","data":null}`)
			return
		}
		calls++
		writer.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(writer, "event: done\ndata: {}\n\n")
	}))
	defer server.Close()
	code, output := operationCLI(t, server, `{"agent_uuid":"agent","payload":{}}`, "op", "stream", "agent.debug.stream", "--file", "-", "--yes")
	if code != 0 || calls != 1 {
		t.Fatalf("code=%d calls=%d %s", code, calls, output)
	}
	code, output = operationCLI(t, server, `{}`, "op", "stream", "future.hidden", "--file", "-", "--yes")
	if code != 5 || calls != 1 {
		t.Fatalf("unknown hidden operation accepted: %d %s", code, output)
	}
}

func TestGenericOperationsModesAndConfirmation(t *testing.T) {
	for _, test := range []struct {
		name, risk  string
		args        []string
		input       string
		supported   bool
		code, calls int
	}{
		{"list", "read", []string{"op", "list", "--domain", "agent", "--search", "test"}, "", true, 0, 0},
		{"describe", "read", []string{"op", "describe", "agent.list"}, "", true, 0, 0},
		{"read", "read", []string{"op", "call", "agent.list"}, "", true, 0, 1},
		{"write denied", "write", []string{"op", "call", "agent.create"}, "", true, 6, 0},
		{"high denied", "high", []string{"op", "call", "agent.delete"}, "", true, 6, 0},
		{"dangerous denied", "dangerous", []string{"op", "call", "agent.delete"}, "", true, 6, 0},
		{"write yes", "write", []string{"op", "call", "agent.create", "--yes", "--file", "-"}, `{"agent_data":{"name":"test"}}`, true, 0, 1},
		{"old Core", "read", []string{"op", "call", "agent.list"}, "", false, 8, 0},
		{"invalid id", "read", []string{"op", "call", "../system"}, "", true, 2, 0},
		{"invalid body", "read", []string{"op", "call", "agent.list", "--file", "-"}, `[]`, true, 2, 0},
		{"stdin conflict", "read", []string{"op", "call", "agent.list", "--file", "-", "--api-key-stdin"}, `{}`, true, 2, 0},
		{"mode conflict", "read", []string{"op", "call", "agent.debug.stream"}, "", true, 2, 0},
		{"stream", "write", []string{"op", "stream", "agent.debug.stream", "--file", "-", "--yes"}, `{}`, true, 0, 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				if request.Header.Get("X-LangBot-Client-Command") != test.args[0]+"."+test.args[1] {
					t.Error("generic command identity missing")
				}
				if operationPreflight(writer, request, test.supported) {
					return
				}
				if !test.supported {
					t.Error("old Core received operation request")
				}
				if request.Method == http.MethodGet {
					if request.URL.Path == "/api/v1/operations" {
						if request.URL.Query().Get("domain") != "agent" || request.URL.Query().Get("q") != "test" {
							t.Error("missing filters")
						}
						fmt.Fprint(writer, `{"operations":[]}`)
					} else {
						json.NewEncoder(writer).Encode(map[string]any{"id": strings.TrimPrefix(request.URL.Path, "/api/v1/operations/"), "risk": test.risk, "streaming": strings.HasSuffix(request.URL.Path, ".stream")})
					}
					return
				}
				calls++
				if strings.HasSuffix(request.URL.Path, "/stream") {
					writer.Header().Set("Content-Type", "text/event-stream")
					fmt.Fprint(writer, "data: {\"value\":1}\n\nevent: done\n\n")
				} else {
					fmt.Fprint(writer, `{"code":0,"data":null}`)
				}
			}))
			defer server.Close()
			code, output := operationCLI(t, server, test.input, test.args...)
			if code != test.code || calls != test.calls {
				t.Fatalf("code=%d calls=%d output=%s", code, calls, output)
			}
		})
	}
}

func TestPermissionConfirmation(t *testing.T) {
	for _, compat := range []bool{false, true} {
		for _, explicit := range []bool{false, true} {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				if operationPreflight(writer, request, true) {
					return
				}
				switch request.URL.Path {
				case "/api/v1/operations/plugin.permissions.get":
					fmt.Fprintf(writer, `{"code":0,"data":{"compatibility_mode":%t,"permissions":[{"id":"storage.plugin","category":"capabilities"},{"id":"agent.list","category":"workspace_operations"}]}}`, compat)
				case "/api/v1/operations/plugin.permissions.update":
					calls++
					var body map[string]any
					json.NewDecoder(request.Body).Decode(&body)
					want := map[string]any{"author": "author", "plugin_name": "name", "confirm": true, "capabilities": []any{"storage.plugin"}, "workspace_operations": []any{"agent.list"}}
					if explicit && !compat {
						want["workspace_operations"] = []any{}
					}
					if !reflect.DeepEqual(body, want) {
						t.Errorf("confirmation = %#v, want %#v", body, want)
					}
					fmt.Fprint(writer, `{"code":0,"data":{"enabled":true,"pending_confirmation":false}}`)
				default:
					writer.WriteHeader(404)
				}
			}))
			args := []string{"plugin", "permissions", "confirm", "author", "name", "--yes"}
			if explicit {
				args = append(args, "--capability", "storage.plugin")
			}
			code, output := operationCLI(t, server, "", args...)
			server.Close()
			if code != 0 || calls != 1 {
				t.Fatalf("code=%d calls=%d %s", code, calls, output)
			}
		}
	}
}

func TestInstallLocalConfirmPermissions(t *testing.T) {
	filename := filepath.Join(t.TempDir(), "plugin.lbpkg")
	var archive bytes.Buffer
	writer := zip.NewWriter(&archive)
	manifest, _ := writer.Create("manifest.yaml")
	io.WriteString(manifest, "metadata:\n  author: author\n  name: name\n")
	writer.Close()
	os.WriteFile(filename, archive.Bytes(), 0600)
	steps := []string{}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if operationPreflight(writer, request, true) {
			return
		}
		steps = append(steps, request.URL.Path)
		switch request.URL.Path {
		case "/api/v1/plugins/install/local":
			file, header, err := request.FormFile("file")
			if err != nil {
				t.Error(err)
			} else {
				file.Close()
				if header.Filename != "plugin.lbpkg" {
					t.Error(header.Filename)
				}
			}
			fmt.Fprint(writer, `{"code":0,"data":{"task_id":1}}`)
		case "/api/v1/system/tasks/1":
			fmt.Fprint(writer, `{"code":0,"data":{"id":1,"task_type":"user","kind":"plugin-operation","status":"succeeded","error":null,"result":null,"created_at":1}}`)
		case "/api/v1/operations/plugin.permissions.get":
			fmt.Fprint(writer, `{"code":0,"data":{"compatibility_mode":false,"permissions":[]}}`)
		case "/api/v1/operations/plugin.permissions.update":
			var body map[string]any
			json.NewDecoder(request.Body).Decode(&body)
			if body["author"] != "author" || body["plugin_name"] != "name" || body["confirm"] != true {
				t.Error(body)
			}
			fmt.Fprint(writer, `{"code":0,"data":{"enabled":true,"pending_confirmation":false}}`)
		default:
			writer.WriteHeader(404)
		}
	}))
	defer server.Close()
	code, output := operationCLI(t, server, "", "plugin", "install", "local", "--file", filename, "--confirm-permissions", "--poll-interval", "1ms")
	if code != 0 || len(steps) != 4 || !strings.Contains(output, `"enabled": true`) {
		t.Fatalf("code=%d steps=%v %s", code, steps, output)
	}
}

func TestUploadOperations(t *testing.T) {
	for path, identifier := range uploadOperations {
		t.Run(path, func(t *testing.T) {
			filename := filepath.Join(t.TempDir(), "sample.zip")
			os.WriteFile(filename, []byte("payload"), 0600)
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				if operationPreflight(writer, request, true) {
					return
				}
				want := map[string]string{"plugin.config_file.upload": "/api/v1/plugins/config-files", "file.image.upload": "/api/v1/files/images", "skill.zip.preview": "/api/v1/skills/install/upload/preview"}[identifier]
				if request.URL.Path != want || request.Method != "POST" {
					t.Errorf("unexpected %s %s", request.Method, request.URL.Path)
					writer.WriteHeader(404)
					return
				}
				file, _, err := request.FormFile("file")
				if err != nil {
					t.Error(err)
				} else {
					content, _ := io.ReadAll(file)
					file.Close()
					if string(content) != "payload" {
						t.Error(string(content))
					}
				}
				calls++
				fmt.Fprint(writer, `{"code":0,"data":{}}`)
			}))
			defer server.Close()
			code, output := operationCLI(t, server, "", append(strings.Fields(path), "--file", filename, "--yes")...)
			if code != 0 || calls != 1 {
				t.Fatalf("code=%d calls=%d %s", code, calls, output)
			}
		})
	}
}

func TestAudioModelRoutesAreProbed(t *testing.T) {
	for _, modelType := range []string{"asr", "tts"} {
		for _, available := range []bool{false, true} {
			requests := 0
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				if operationPreflight(writer, request, false) {
					return
				}
				if request.URL.Path != "/api/v1/provider/models/"+modelType {
					t.Error(request.URL.Path)
					writer.WriteHeader(404)
					return
				}
				requests++
				if !available {
					writer.WriteHeader(404)
					fmt.Fprint(writer, `{"code":404,"data":null}`)
					return
				}
				fmt.Fprint(writer, `{"code":0,"data":{"models":[]}}`)
			}))
			code, output := operationCLI(t, server, "", "model", "list", "--type", modelType)
			server.Close()
			want := 8
			if available {
				want = 0
			}
			if code != want || requests == 0 {
				t.Fatalf("%s available=%t code=%d %s", modelType, available, code, output)
			}
		}
	}
}

func TestMarketplacePositionalInstall(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if operationPreflight(writer, request, true) {
			return
		}
		if request.URL.Path != "/api/v1/operations/plugin.install.marketplace" {
			t.Error(request.URL.Path)
			writer.WriteHeader(404)
			return
		}
		var body map[string]any
		json.NewDecoder(request.Body).Decode(&body)
		if !reflect.DeepEqual(body, map[string]any{"plugin_author": "author", "plugin_name": "name", "plugin_version": "1.2.3"}) {
			t.Error(body)
		}
		calls++
		fmt.Fprint(writer, `{"code":0,"data":{"task_id":7}}`)
	}))
	defer server.Close()
	code, output := operationCLI(t, server, "", "plugin", "install", "marketplace", "author", "name", "--version", "1.2.3")
	if code != 0 || calls != 1 {
		t.Fatalf("code=%d calls=%d %s", code, calls, output)
	}
}

func TestOldCoreInstallConfirmationDoesNotUpload(t *testing.T) {
	filename := filepath.Join(t.TempDir(), "plugin.lbpkg")
	os.WriteFile(filename, []byte("archive"), 0600)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if !operationPreflight(writer, request, false) {
			t.Error("unexpected upload " + request.URL.Path)
			writer.WriteHeader(404)
		}
	}))
	defer server.Close()
	code, output := operationCLI(t, server, "", "plugin", "install", "local", "--file", filename, "--confirm-permissions")
	if code != 8 {
		t.Fatalf("code=%d %s", code, output)
	}
}

func TestRemoteInstallConfirmationAndFailure(t *testing.T) {
	for _, source := range []string{"github", "marketplace"} {
		for _, failed := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/failed=%t", source, failed), func(t *testing.T) {
				var archive bytes.Buffer
				writer := zip.NewWriter(&archive)
				manifest, _ := writer.Create("manifest.yaml")
				io.WriteString(manifest, "metadata:\n  author: manifest-author\n  name: manifest-name\n")
				writer.Close()
				confirmed, downloaded := 0, 0
				server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
					if operationPreflight(writer, request, true) {
						return
					}
					switch request.URL.Path {
					case "/api/v1/plugins/install/github", "/api/v1/operations/plugin.install.marketplace":
						fmt.Fprint(writer, `{"code":0,"data":{"task_id":1}}`)
					case "/api/v1/system/tasks/1":
						if failed {
							fmt.Fprint(writer, `{"code":0,"data":{"id":1,"task_type":"user","kind":"plugin-operation","status":"failed","error":{"type":"task_failed","message":"failed"},"result":null,"created_at":1}}`)
						} else {
							fmt.Fprint(writer, `{"code":0,"data":{"id":1,"task_type":"user","kind":"plugin-operation","status":"succeeded","error":null,"result":null,"created_at":1}}`)
						}
					case "/api/v1/operations/plugin.permissions.get":
						fmt.Fprint(writer, `{"code":0,"data":{"permissions":[],"compatibility_mode":false}}`)
					case "/api/v1/operations/plugin.permissions.update":
						var body map[string]any
						json.NewDecoder(request.Body).Decode(&body)
						if body["author"] != "manifest-author" || body["plugin_name"] != "manifest-name" {
							t.Error(body)
						}
						confirmed++
						fmt.Fprint(writer, `{"code":0,"data":{"enabled":true,"pending_confirmation":false}}`)
					default:
						t.Error(request.URL.Path)
						writer.WriteHeader(404)
					}
				}))
				defer server.Close()
				transport := operationTransport(func(request *http.Request) (*http.Response, error) {
					if request.URL.Host == "api.github.com" {
						downloaded++
						if request.Header.Get("X-API-Key") != "" || request.Header.Get("Authorization") != "" {
							t.Error("Workspace credential sent to GitHub")
						}
						return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(archive.Bytes()))}, nil
					}
					return http.DefaultTransport.RoundTrip(request)
				})
				arguments := []string{"plugin", "install", source, "--confirm-permissions", "--poll-interval", "1ms"}
				body := ""
				if source == "github" {
					arguments = append(arguments, "--file", "-")
					body = `{"owner":"repo-owner","repo":"repo-name","release_tag":"v1","release_id":8,"asset_id":7}`
				} else {
					arguments = append(arguments, "manifest-author", "manifest-name")
				}
				code, output := operationCLITransport(t, server, body, transport, arguments...)
				if failed {
					if code == 0 || confirmed != 0 {
						t.Fatalf("failed installation confirmed: %s", output)
					}
				} else if code != 0 || confirmed != 1 {
					t.Fatalf("code=%d confirmed=%d %s", code, confirmed, output)
				}
				if source == "github" && downloaded != 1 {
					t.Fatal("archive identity not inspected")
				}
			})
		}
	}
}
