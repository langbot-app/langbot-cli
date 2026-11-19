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
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"go.yaml.in/yaml/v3"
)

type operationTransport func(*http.Request) (*http.Response, error)

func artifactMetadataResponse(w http.ResponseWriter, identifier, digest string, size int) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{
		"id": identifier, "name": "../../same-name.bin", "size": size, "digest": digest,
	}})
}

func TestArtifactCollectBinaryDuplicatesAndExistingFiles(t *testing.T) {
	contents := map[string][]byte{"art_first": {'a', 0, 'b', '\n'}, "art_second": {255, 0, 10, 20}, "art_empty": {}}
	metadataCalls, contentCalls := 0, 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if operationPreflight(w, r, true) {
			return
		}
		if r.Header.Get("X-API-Key") != "test-secret" || r.Header.Get("Authorization") != "" {
			t.Error("unexpected collection identity")
		}
		if r.URL.Path == "/api/v1/operations/artifact.get" {
			var body struct {
				ID string `json:"artifact_id"`
			}
			if json.NewDecoder(r.Body).Decode(&body) != nil {
				t.Error("invalid metadata request")
				return
			}
			metadataCalls++
			payload := contents[body.ID]
			artifactMetadataResponse(w, body.ID, fmt.Sprintf("%x", sha256.Sum256(payload)), len(payload))
			return
		}
		id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api/v1/artifacts/"), "/content")
		payload, exists := contents[id]
		if !exists || r.Method != http.MethodGet {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(404)
			return
		}
		contentCalls++
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write(payload)
	}))
	defer server.Close()
	directory := filepath.Join(t.TempDir(), "delivery")
	manifest := `artifacts:
  - {id: art_first, name: first.bin}
  - {id: art_second}
  - {id: art_first, name: first.bin}
  - {id: art_empty}
`
	code, out := operationCLI(t, server, manifest, "artifact", "collect", "--manifest", "-", "--dir", directory)
	if code != 0 || metadataCalls != 3 || contentCalls != 3 {
		t.Fatalf("code=%d metadata=%d content=%d %s", code, metadataCalls, contentCalls, out)
	}
	for id, payload := range contents {
		name := id + ".bin"
		if id == "art_first" {
			name = "first.bin"
		}
		got, err := os.ReadFile(filepath.Join(directory, name))
		if err != nil || !bytes.Equal(got, payload) {
			t.Fatalf("%s=%v error=%v", name, got, err)
		}
	}
	var envelope struct {
		Data struct {
			Summary map[string]int `json:"summary"`
		} `json:"data"`
	}
	if json.Unmarshal([]byte(out), &envelope) != nil || envelope.Data.Summary["duplicates"] != 1 || envelope.Data.Summary["saved"] != 3 {
		t.Fatal(out)
	}
	code, out = operationCLI(t, server, manifest, "artifact", "collect", "--manifest", "-", "--dir", directory)
	if code != 0 || metadataCalls != 6 || contentCalls != 6 || !strings.Contains(out, "already_present") {
		t.Fatalf("existing files bypassed authorization: %s", out)
	}
	if err := os.WriteFile(filepath.Join(directory, "first.bin"), []byte("local edit"), 0o600); err != nil {
		t.Fatal(err)
	}
	code, out = operationCLI(t, server, manifest, "artifact", "collect", "--manifest", "-", "--dir", directory)
	if code == 0 || !strings.Contains(out, "artifact_destination_conflict") {
		t.Fatal(out)
	}
	got, _ := os.ReadFile(filepath.Join(directory, "first.bin"))
	if string(got) != "local edit" {
		t.Fatal("overwrote different existing content")
	}
	entries, err := os.ReadDir(directory)
	if err != nil || len(entries) != 3 {
		t.Fatalf("left temporary or server-named files: %v %v", entries, err)
	}
}

func TestArtifactCollectHTTPAndIntegrityEvidence(t *testing.T) {
	identifiers := []string{"art_meta_400", "art_meta_403", "art_meta_404", "art_meta_410", "art_content_400", "art_content_403", "art_content_404", "art_content_410", "art_too_big", "art_invalid_digest", "art_bad_digest", "art_short", "art_overflow", "art_stream", "art_wrong_id", "art_good"}
	contentCalls := map[string]int{}
	payload := []byte{'a', 0, 'b', 'c', 'd'}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if operationPreflight(w, r, true) {
			return
		}
		w.Header().Set("X-Request-Id", "collect-request")
		if r.URL.Path == "/api/v1/operations/artifact.get" {
			var body struct {
				ID string `json:"artifact_id"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			if strings.HasPrefix(body.ID, "art_meta_") {
				var status int
				_, _ = fmt.Sscanf(strings.TrimPrefix(body.ID, "art_meta_"), "%d", &status)
				w.WriteHeader(status)
				fmt.Fprintf(w, `{"code":"artifact_%d","msg":"unavailable"}`, status)
				return
			}
			digest, size, id := fmt.Sprintf("%x", sha256.Sum256(payload)), len(payload), body.ID
			switch id {
			case "art_too_big":
				size = 10<<20 + 1
			case "art_invalid_digest":
				digest = "bad digest"
			case "art_bad_digest":
				digest = strings.Repeat("0", 64)
			case "art_short":
				size++
			case "art_overflow":
				size--
			case "art_wrong_id":
				id = "art_other"
			}
			artifactMetadataResponse(w, id, digest, size)
			return
		}
		id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api/v1/artifacts/"), "/content")
		contentCalls[id]++
		if strings.HasPrefix(id, "art_content_") {
			var status int
			_, _ = fmt.Sscanf(strings.TrimPrefix(id, "art_content_"), "%d", &status)
			w.WriteHeader(status)
			fmt.Fprintf(w, `{"code":"artifact_%d","msg":"unavailable"}`, status)
			return
		}
		if id == "art_stream" {
			w.Header().Set("Content-Length", "5")
			_, _ = w.Write(payload[:2])
			return
		}
		_, _ = w.Write(payload)
	}))
	defer server.Close()
	var manifest strings.Builder
	manifest.WriteString("artifacts:\n")
	for _, id := range identifiers {
		fmt.Fprintf(&manifest, "  - {id: %s}\n", id)
	}
	directory := t.TempDir()
	code, out := operationCLI(t, server, manifest.String(), "artifact", "collect", "--manifest", "-", "--dir", directory)
	if code == 0 {
		t.Fatal(out)
	}
	var envelope struct {
		Data struct {
			Files []struct {
				ID       string `json:"id"`
				Status   string `json:"status"`
				Received int    `json:"received_bytes"`
				Error    *struct {
					Type       string `json:"type"`
					HTTPStatus int    `json:"http_status"`
					Code       any    `json:"server_code"`
					RequestID  string `json:"request_id"`
				} `json:"error"`
			} `json:"files"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(out), &envelope); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	for index, row := range envelope.Data.Files {
		if index < 8 {
			want := []int{400, 403, 404, 410}[index%4]
			if row.Error == nil || row.Error.HTTPStatus != want || row.Error.Code != fmt.Sprintf("artifact_%d", want) || row.Error.RequestID != "collect-request" {
				t.Fatalf("missing HTTP evidence: %+v %s", row, out)
			}
		}
		if row.ID == "art_bad_digest" || row.ID == "art_short" {
			if row.Error == nil || row.Error.Type != "artifact_integrity_failed" || row.Received != len(payload) {
				t.Fatalf("missing integrity evidence: %+v", row)
			}
		}
		if row.ID == "art_stream" && (row.Error == nil || row.Received != 2) {
			t.Fatalf("missing interrupted download evidence: %+v", row)
		}
	}
	for _, id := range []string{"art_too_big", "art_invalid_digest", "art_wrong_id"} {
		if contentCalls[id] != 0 {
			t.Errorf("downloaded invalid metadata for %s", id)
		}
	}
	entries, _ := os.ReadDir(directory)
	if len(entries) != 1 || entries[0].Name() != "art_good.bin" {
		t.Fatalf("partial files survived failures: %v %s", entries, out)
	}
}

func TestArtifactCollectValidatesManifestOffline(t *testing.T) {
	for _, manifest := range []string{
		`{"artifacts":[]}`, `{"ids":["art_a"]}`,
		`{"artifacts":[{"id":"../outside"}]}`,
		`{"artifacts":[{"id":"art_a","name":"../outside"}]}`,
		`{"artifacts":[{"id":"art_a","name":"C:\\outside.bin"}]}`,
		`{"artifacts":[{"id":"art_a","name":"NUL.bin"}]}`,
		`{"artifacts":[{"id":"art_a","name":"bad\u0000.bin"}]}`,
		`{"artifacts":[{"id":"art_a","name":"same.bin"},{"id":"art_b","name":"SAME.bin"}]}`,
		`{"artifacts":[{"id":"art_a","name":"one.bin"},{"id":"art_a","name":"two.bin"}]}`,
		`{"artifacts":[{"id":"art_a","unknown":true}]}`,
	} {
		t.Run(manifest, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; w.WriteHeader(500) }))
			defer server.Close()
			directory := filepath.Join(t.TempDir(), "delivery")
			code, out := operationCLI(t, server, manifest, "artifact", "collect", "--manifest", "-", "--dir", directory)
			if code != 2 || calls != 0 {
				t.Fatalf("code=%d calls=%d %s", code, calls, out)
			}
			if _, err := os.Stat(directory); !os.IsNotExist(err) {
				t.Fatal("invalid manifest created an output directory")
			}
		})
	}
	t.Run("output path is a file", func(t *testing.T) {
		calls := 0
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; w.WriteHeader(500) }))
		defer server.Close()
		path := filepath.Join(t.TempDir(), "existing.bin")
		if err := os.WriteFile(path, []byte("existing"), 0o600); err != nil {
			t.Fatal(err)
		}
		code, out := operationCLI(t, server, `artifacts: [{id: art_a}]`, "artifact", "collect", "--manifest", "-", "--dir", path)
		if code != 2 || calls != 0 {
			t.Fatalf("code=%d calls=%d %s", code, calls, out)
		}
	})
}

func TestArtifactCollectCancellationRetainsOnlyCompletedFiles(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	payload := []byte{1, 0, 2, 3}
	metadataCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if operationPreflight(w, r, true) {
			return
		}
		if r.URL.Path == "/api/v1/operations/artifact.get" {
			var body struct {
				ID string `json:"artifact_id"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			metadataCalls++
			artifactMetadataResponse(w, body.ID, fmt.Sprintf("%x", sha256.Sum256(payload)), len(payload))
			return
		}
		if strings.Contains(r.URL.Path, "art_second") {
			cancel()
			return
		}
		_, _ = w.Write(payload)
	}))
	defer server.Close()
	directory := t.TempDir()
	code, out := operationCLIContext(t, ctx, server, `artifacts: [{id: art_first}, {id: art_second}, {id: art_third}]`, nil, "artifact", "collect", "--manifest", "-", "--dir", directory)
	if code == 0 || metadataCalls != 2 || !strings.Contains(out, "cancelled") || !strings.Contains(out, "not_started") {
		t.Fatal(out)
	}
	entries, _ := os.ReadDir(directory)
	if len(entries) != 1 || entries[0].Name() != "art_first.bin" {
		t.Fatalf("interrupted files survived: %v %s", entries, out)
	}
}

func TestArtifactCollectKeepsMemberAuthorizationForMetadataAndContent(t *testing.T) {
	payloads := map[string][]byte{"art_a": {'a', 0, 255}, "art_b": {'b', 0, 128}, "art_read_denied": {0, 'x'}}
	metadataCalls, contentCalls := map[string]int{}, map[string]int{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		actor := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if (actor != "member-a-token" && actor != "member-b-token") || r.Header.Get("X-API-Key") != "" {
			t.Errorf("wrong member headers: %v", r.Header)
			w.WriteHeader(401)
			return
		}
		if r.URL.Path == "/api/v1/workspaces/current" {
			fmt.Fprint(w, `{"code":0,"data":{"workspace":{"uuid":"workspace","instance_uuid":"instance"},"permissions":["resource.view"]}}`)
			return
		}
		var id string
		metadata := r.URL.Path == "/api/v1/operations/artifact.get"
		if metadata {
			var body struct {
				ID string `json:"artifact_id"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			id = body.ID
			metadataCalls[actor+"/"+id]++
		} else {
			id = strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api/v1/artifacts/"), "/content")
			contentCalls[actor+"/"+id]++
		}
		forbidden := id == "art_a" && actor != "member-a-token" || id == "art_b" && actor != "member-b-token" || id == "art_read_denied" && !metadata
		if forbidden {
			w.Header().Set("X-Request-Id", "member-denied")
			w.WriteHeader(403)
			fmt.Fprint(w, `{"code":"artifact_forbidden","msg":"denied"}`)
			return
		}
		payload, exists := payloads[id]
		if !exists {
			t.Errorf("unexpected Artifact request %s", r.URL.Path)
			w.WriteHeader(404)
			return
		}
		if metadata {
			artifactMetadataResponse(w, id, fmt.Sprintf("%x", sha256.Sum256(payload)), len(payload))
			return
		}
		_, _ = w.Write(payload)
	}))
	defer server.Close()
	for _, actor := range []string{"member-a-token", "member-b-token"} {
		directory := t.TempDir()
		if err := os.WriteFile(filepath.Join(directory, "art_read_denied.bin"), payloads["art_read_denied"], 0o600); err != nil {
			t.Fatal(err)
		}
		configuration := filepath.Join(t.TempDir(), "config.yaml")
		body := fmt.Sprintf("version: 1\ncurrent_context: test\ncontexts:\n  test:\n    endpoint: %s\n    expected_workspace_uuid: workspace\n    user_token: %s\n", server.URL, actor)
		if err := os.WriteFile(configuration, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		var out bytes.Buffer
		code := executeChinese(context.Background(), []string{"--config", configuration, "--as-member", "-o", "json", "artifact", "collect", "--manifest", "-", "--dir", directory}, Dependencies{
			In: strings.NewReader(`artifacts: [{id: art_a}, {id: art_b}, {id: art_read_denied}]`), Out: &out, Err: &out, Version: "test",
			LookupEnv: func(string) (string, bool) { return "", false },
		})
		if code == 0 || strings.Contains(out.String(), actor) || !strings.Contains(out.String(), `"http_status": 403`) || !strings.Contains(out.String(), "member-denied") {
			t.Fatal(out.String())
		}
		own, foreign := "art_a", "art_b"
		if actor == "member-b-token" {
			own, foreign = foreign, own
		}
		got, err := os.ReadFile(filepath.Join(directory, own+".bin"))
		if err != nil || !bytes.Equal(got, payloads[own]) {
			t.Fatalf("lost authorized member file: %v %s", err, out.String())
		}
		if _, err := os.Stat(filepath.Join(directory, foreign+".bin")); !os.IsNotExist(err) {
			t.Fatal("delivered another member's Artifact")
		}
		if metadataCalls[actor+"/"+foreign] != 1 || contentCalls[actor+"/"+foreign] != 0 || contentCalls[actor+"/"+own] != 1 || contentCalls[actor+"/art_read_denied"] != 1 {
			t.Fatalf("authorization phases skipped: %v %v", metadataCalls, contentCalls)
		}
	}
}

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
	return operationCLIFormatContext(t, ctx, server, input, "json", transport, arguments...)
}

func operationCLIFormatContext(t *testing.T, ctx context.Context, server *httptest.Server, input, format string, transport http.RoundTripper, arguments ...string) (int, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	configuration := fmt.Sprintf("version: 1\ncurrent_context: test\ncontexts:\n  test:\n    endpoint: %s\n    expected_workspace_uuid: workspace\n    credential:\n      type: env\n      name: TEST_KEY\n", server.URL)
	if err := os.WriteFile(path, []byte(configuration), 0600); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	prefix := []string{"--config", path}
	if format != "" {
		prefix = append(prefix, "-o", format)
	}
	code := executeChinese(ctx, append(prefix, arguments...), Dependencies{
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
	jsonDecoder := json.NewDecoder(strings.NewReader(output))
	jsonDecoder.UseNumber()
	var decoder interface{ Decode(any) error } = jsonDecoder
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

type agentWatchReply struct {
	after  int64
	value  any
	status int
	cancel context.CancelFunc
}

func agentWatchPageValue(status string, sequences ...int64) map[string]any {
	items := make([]any, 0, len(sequences))
	for _, sequence := range sequences {
		items = append(items, map[string]any{"run_id": "run-a", "sequence": sequence, "type": "run.output", "data": map[string]any{"text": "你好🙂"}})
	}
	return map[string]any{"run": map[string]any{"run_id": "run-a", "agent_id": "processor-a", "status": status}, "items": items, "next_cursor": nil, "has_more": false}
}

func agentWatchHTTP(t *testing.T, replies []agentWatchReply) (*httptest.Server, *atomic.Int32, *atomic.Int32) {
	t.Helper()
	var calls, requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests.Add(1)
		if request.Header.Get("X-API-Key") != "test-secret" || request.Header.Get("X-LangBot-Client-Command") != "agent.runs.watch" {
			t.Error("Agent run watch lost its credential or command identity")
		}
		if operationPreflight(writer, request, true) {
			return
		}
		if request.Method != http.MethodPost || request.URL.Path != "/api/v1/operations/agent.runs.events" {
			t.Errorf("watch attempted an unexpected operation %s %s", request.Method, request.URL.Path)
			writer.WriteHeader(http.StatusInternalServerError)
			return
		}
		index := int(calls.Add(1)) - 1
		if index >= len(replies) {
			t.Errorf("watch exceeded its expected requests at page %d", index)
			writer.WriteHeader(http.StatusInternalServerError)
			return
		}
		reply := replies[index]
		var body map[string]any
		decoder := json.NewDecoder(request.Body)
		decoder.UseNumber()
		if err := decoder.Decode(&body); err != nil {
			t.Error(err)
		}
		if body["processor_id"] != "processor-a" || body["run_id"] != "run-a" || fmt.Sprint(body["after_sequence"]) != strconv.FormatInt(reply.after, 10) || len(body) != 3 {
			t.Errorf("watch page %d body=%#v, expected after_sequence=%d", index, body, reply.after)
		}
		if reply.cancel != nil {
			reply.cancel()
			<-request.Context().Done()
			return
		}
		if reply.status != 0 {
			writer.WriteHeader(reply.status)
		}
		json.NewEncoder(writer).Encode(map[string]any{"code": 0, "data": reply.value})
	}))
	return server, &calls, &requests
}

func TestAgentRunWatchDrainsPagesAndResumesFromEmittedSequences(t *testing.T) {
	first := agentWatchPageValue("running", 2, 1, 1)
	first["has_more"], first["next_cursor"] = true, int64(2)
	const largeSequence int64 = 9007199254740993
	for _, test := range []struct {
		name, format string
		after        int64
		replies      []agentWatchReply
		sequences    []int64
	}{
		{name: "overlap unordered pages and late terminal event JSON", format: "json", replies: []agentWatchReply{{value: first}, {after: 2, value: agentWatchPageValue("completed", 2, 3)}, {after: 3, value: agentWatchPageValue("completed", 4)}, {after: 4, value: agentWatchPageValue("completed")}}, sequences: []int64{1, 2, 3, 4}},
		{name: "empty active pages and nil tail YAML", format: "yaml", replies: []agentWatchReply{{value: agentWatchPageValue("created")}, {value: agentWatchPageValue("queued")}, {value: agentWatchPageValue("claimed", 1)}, {after: 1, value: agentWatchPageValue("running")}, {after: 1, value: agentWatchPageValue("completed", 2)}, {after: 2, value: agentWatchPageValue("completed")}}, sequences: []int64{1, 2}},
		{name: "terminal changes before confirmation", format: "json", replies: []agentWatchReply{{value: agentWatchPageValue("completed", 1)}, {after: 1, value: agentWatchPageValue("queued")}, {after: 1, value: agentWatchPageValue("running", 2)}, {after: 2, value: agentWatchPageValue("completed")}, {after: 2, value: agentWatchPageValue("completed")}}, sequences: []int64{1, 2}},
		{name: "large resume sequence", format: "json", after: largeSequence, replies: []agentWatchReply{{after: largeSequence, value: agentWatchPageValue("completed", largeSequence+1)}, {after: largeSequence + 1, value: agentWatchPageValue("completed")}}, sequences: []int64{largeSequence + 1}},
	} {
		t.Run(test.name, func(t *testing.T) {
			server, calls, _ := agentWatchHTTP(t, test.replies)
			defer server.Close()
			code, output := operationCLIFormatContext(t, context.Background(), server, "", test.format, nil, "agent", "runs", "watch", "processor-a", "run-a", "--after-sequence", strconv.FormatInt(test.after, 10), "--poll-interval", "1ms")
			envelopes := decodeOperationStream(t, output, test.format)
			if code != 0 || int(calls.Load()) != len(test.replies) || len(envelopes) != len(test.sequences)+1 {
				t.Fatalf("code=%d calls=%d output=%s", code, calls.Load(), output)
			}
			for index, sequence := range test.sequences {
				data := envelopes[index]["data"].(map[string]any)
				if envelopes[index]["ok"] != true || fmt.Sprint(data["last_sequence"]) != strconv.FormatInt(sequence, 10) || fmt.Sprint(data["event"].(map[string]any)["sequence"]) != strconv.FormatInt(sequence, 10) {
					t.Fatalf("event %d = %#v", index, envelopes[index])
				}
			}
			last := envelopes[len(envelopes)-1]
			data := last["data"].(map[string]any)
			if last["ok"] != true || data["completed"] != true || data["status"] != "completed" || fmt.Sprint(data["last_sequence"]) != strconv.FormatInt(test.sequences[len(test.sequences)-1], 10) {
				t.Fatalf("watch summary = %#v", last)
			}
		})
	}
}

func TestAgentRunWatchReportsTerminalOutcomesAndHumanEvents(t *testing.T) {
	for _, test := range []struct {
		status string
		code   int
	}{{"failed", 10}, {"cancelled", 6}, {"timeout", 7}} {
		t.Run(test.status, func(t *testing.T) {
			server, calls, _ := agentWatchHTTP(t, []agentWatchReply{{value: agentWatchPageValue(test.status, 1)}, {after: 1, value: agentWatchPageValue(test.status)}})
			defer server.Close()
			code, output := operationCLI(t, server, "", "agent", "runs", "watch", "processor-a", "run-a")
			envelopes := decodeOperationStream(t, output, "json")
			if code != test.code || calls.Load() != 2 || len(envelopes) != 2 {
				t.Fatalf("code=%d calls=%d output=%s", code, calls.Load(), output)
			}
			last := envelopes[1]
			data := last["data"].(map[string]any)
			if last["ok"] != false || data["completed"] != false || data["status"] != test.status || fmt.Sprint(data["last_sequence"]) != "1" {
				t.Fatalf("terminal summary = %#v", last)
			}
		})
	}
	t.Run("human", func(t *testing.T) {
		server, calls, _ := agentWatchHTTP(t, []agentWatchReply{{value: agentWatchPageValue("completed", 1)}, {after: 1, value: agentWatchPageValue("completed")}})
		defer server.Close()
		code, output := operationCLIFormatContext(t, context.Background(), server, "", "", nil, "agent", "runs", "watch", "processor-a", "run-a")
		if code != 0 || calls.Load() != 2 || strings.Count(output, "[1] run.output") != 1 || !strings.Contains(output, "你好🙂") || !strings.Contains(output, "completed") || strings.Contains(output, `"ok":`) {
			t.Fatalf("human watch code=%d calls=%d output=%s", code, calls.Load(), output)
		}
	})
}

func TestAgentRunWatchRejectsMalformedPagesWithoutAdvancing(t *testing.T) {
	for _, kind := range []string{"run identity", "unknown status", "missing status", "items type", "event sequence", "event identity", "missing event type", "has_more type", "cursor type", "cursor skips event", "empty continuation", "stalled continuation"} {
		t.Run(kind, func(t *testing.T) {
			page := agentWatchPageValue("completed", 8)
			event := page["items"].([]any)[0].(map[string]any)
			switch kind {
			case "run identity":
				page["run"].(map[string]any)["run_id"] = "other"
			case "unknown status":
				page["run"].(map[string]any)["status"] = "succeeded"
			case "missing status":
				delete(page["run"].(map[string]any), "status")
			case "items type":
				page["items"] = nil
			case "event sequence":
				event["sequence"] = 8.5
			case "event identity":
				event["run_id"] = "other"
			case "missing event type":
				delete(event, "type")
			case "has_more type":
				page["has_more"] = "false"
			case "cursor type":
				page["next_cursor"] = "8"
			case "cursor skips event":
				page["has_more"], page["next_cursor"] = true, 9
			case "empty continuation":
				page["items"], page["has_more"], page["next_cursor"] = []any{}, true, 8
			case "stalled continuation":
				event["sequence"] = 7
				page["has_more"], page["next_cursor"] = true, 7
			}
			first := agentWatchPageValue("running", 7)
			first["has_more"], first["next_cursor"] = true, 7
			server, calls, _ := agentWatchHTTP(t, []agentWatchReply{{value: first}, {after: 7, value: page}})
			defer server.Close()
			code, output := operationCLI(t, server, "", "agent", "runs", "watch", "processor-a", "run-a")
			envelopes := decodeOperationStream(t, output, "json")
			if code != 8 || calls.Load() != 2 || len(envelopes) != 2 {
				t.Fatalf("code=%d calls=%d output=%s", code, calls.Load(), output)
			}
			last := envelopes[1]
			data := last["data"].(map[string]any)
			if last["ok"] != false || data["completed"] != false || fmt.Sprint(data["last_sequence"]) != "7" {
				t.Fatalf("malformed page advanced the watch: %#v", last)
			}
		})
	}
}

func TestAgentRunWatchSourceFailurePreservesUsableResumeCommand(t *testing.T) {
	first := agentWatchPageValue("running", 5)
	first["has_more"], first["next_cursor"] = true, 5
	server, calls, _ := agentWatchHTTP(t, []agentWatchReply{{value: first}, {after: 5, status: http.StatusServiceUnavailable}, {after: 5, value: agentWatchPageValue("completed", 6)}, {after: 6, value: agentWatchPageValue("completed")}})
	defer server.Close()
	code, output := operationCLI(t, server, "", "agent", "runs", "watch", "processor-a", "run-a")
	envelopes := decodeOperationStream(t, output, "json")
	if code != 10 || calls.Load() != 2 || len(envelopes) != 2 {
		t.Fatalf("source failure code=%d calls=%d output=%s", code, calls.Load(), output)
	}
	last := envelopes[1]
	data := last["data"].(map[string]any)
	if last["ok"] != false || fmt.Sprint(data["last_sequence"]) != "5" || data["status"] != "running" {
		t.Fatalf("source failure lost watch progress: %#v", last)
	}
	var resume []string
	steps, _ := last["meta"].(map[string]any)["next_steps"].([]any)
	for _, raw := range steps {
		step := raw.(map[string]any)
		encoded, _ := json.Marshal(step["argv"])
		var argv []string
		if json.Unmarshal(encoded, &argv) == nil && strings.Contains(strings.Join(argv, " "), "agent runs watch processor-a run-a --after-sequence 5") {
			resume = argv
		}
	}
	if len(resume) == 0 || resume[0] != "lbctl" || !strings.Contains(strings.Join(resume, " "), "--config") {
		t.Fatalf("usable resume command is missing: %#v", last["meta"])
	}
	code, output = operationCLI(t, server, "", resume[1:]...)
	envelopes = decodeOperationStream(t, output, "json")
	if code != 0 || calls.Load() != 4 || len(envelopes) != 2 || fmt.Sprint(envelopes[0]["data"].(map[string]any)["event"].(map[string]any)["sequence"]) != "6" {
		t.Fatalf("resume code=%d calls=%d output=%s", code, calls.Load(), output)
	}
}

func TestAgentRunWatchCancellationAndTimeoutPreserveSequence(t *testing.T) {
	for _, mode := range []string{"cancel", "timeout", "cancel terminal tail"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			replies := []agentWatchReply{{value: agentWatchPageValue("running", 5)}}
			args := []string{"agent", "runs", "watch", "processor-a", "run-a", "--poll-interval", "2s", "--timeout", "200ms"}
			status := "running"
			if mode == "cancel" {
				replies[0].value.(map[string]any)["has_more"] = true
				replies[0].value.(map[string]any)["next_cursor"] = 5
				replies = append(replies, agentWatchReply{after: 5, cancel: cancel})
			}
			if mode == "cancel terminal tail" {
				status = "completed"
				replies = []agentWatchReply{{value: agentWatchPageValue(status, 5)}, {after: 5, value: agentWatchPageValue(status)}}
			}
			server, calls, _ := agentWatchHTTP(t, replies)
			defer server.Close()
			var transport http.RoundTripper
			if mode == "cancel terminal tail" {
				transport = operationTransport(func(request *http.Request) (*http.Response, error) {
					response, err := http.DefaultTransport.RoundTrip(request)
					if err == nil && request.URL.Path == "/api/v1/operations/agent.runs.events" && calls.Load() == 2 {
						response.Body = cancelOnResponseClose{ReadCloser: response.Body, cancel: cancel}
					}
					return response, err
				})
			}
			code, output := operationCLIContext(t, ctx, server, "", transport, args...)
			envelopes := decodeOperationStream(t, output, "json")
			if code != 7 || int(calls.Load()) != len(replies) || len(envelopes) != 2 {
				t.Fatalf("watch interruption code=%d calls=%d output=%s", code, calls.Load(), output)
			}
			last := envelopes[1]
			if last["ok"] != false || fmt.Sprint(last["data"].(map[string]any)["last_sequence"]) != "5" || last["data"].(map[string]any)["status"] != status || last["data"].(map[string]any)["completed"] != false {
				t.Fatalf("watch interruption lost progress: %#v", last)
			}
		})
	}
}

func TestAgentRunWatchRejectsInvalidFlagsBeforeRequests(t *testing.T) {
	for _, flags := range [][]string{{"--after-sequence", "-1"}, {"--after-sequence", "invalid"}, {"--poll-interval", "0"}, {"--poll-interval", "-1s"}, {"--poll-interval", "invalid"}, {"--timeout", "0"}, {"--timeout", "invalid"}, {"--timeout", ""}} {
		t.Run(strings.Join(flags, " "), func(t *testing.T) {
			server, calls, requests := agentWatchHTTP(t, nil)
			defer server.Close()
			code, output := operationCLI(t, server, "", append([]string{"agent", "runs", "watch", "processor-a", "run-a"}, flags...)...)
			if code != 2 || calls.Load() != 0 || requests.Load() != 0 {
				t.Fatalf("invalid watch flags code=%d requests=%d output=%s", code, requests.Load(), output)
			}
		})
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

type agentDebugAttachment struct {
	path, mime, id string
	content        []byte
}

func agentDebugAttachmentFiles(t *testing.T) []agentDebugAttachment {
	t.Helper()
	png, err := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+jL1sAAAAASUVORK5CYII=")
	if err != nil {
		t.Fatal(err)
	}
	files := []agentDebugAttachment{
		{path: filepath.Join(t.TempDir(), "图片🙂.png"), mime: "image/png", id: "art_00000000000000000000000000000001", content: png},
		{path: filepath.Join(t.TempDir(), "说明.pdf"), mime: "application/pdf", id: "art_00000000000000000000000000000002", content: []byte("%PDF-1.7\n1 0 obj\n<< /Type /Catalog >>\nendobj\n%%EOF\n")},
	}
	for _, file := range files {
		if err := os.WriteFile(file.path, file.content, 0600); err != nil {
			t.Fatal(err)
		}
	}
	return files
}

type agentDebugAttachmentFixture struct {
	t                          *testing.T
	files                      []agentDebugAttachment
	want                       map[string]any
	member                     bool
	unfinishedStream           bool
	rejectUpload, streamStatus int
	requests, uploads, streams atomic.Int32
}

func (fixture *agentDebugAttachmentFixture) server() *httptest.Server {
	fixture.t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		fixture.requests.Add(1)
		credentialOK := request.Header.Get("X-API-Key") == "test-secret" && request.Header.Get("Authorization") == ""
		if fixture.member {
			credentialOK = request.Header.Get("Authorization") == "Bearer member-secret" && request.Header.Get("X-API-Key") == ""
		}
		if !credentialOK || request.Header.Get("X-LangBot-Client-Command") != "agent.debug" {
			fixture.t.Error("attachment debug lost its credential or command identity")
		}
		if operationPreflight(writer, request, true) {
			return
		}
		switch {
		case request.Method == http.MethodPost && request.URL.Path == "/api/v1/artifacts":
			index := int(fixture.uploads.Add(1)) - 1
			if index >= len(fixture.files) {
				fixture.t.Error("attachment upload was repeated")
				writer.WriteHeader(500)
				return
			}
			file, header, err := request.FormFile("file")
			if err != nil {
				fixture.t.Error(err)
				writer.WriteHeader(400)
				return
			}
			defer file.Close()
			defer request.MultipartForm.RemoveAll()
			content, err := io.ReadAll(file)
			want := fixture.files[index]
			if err != nil || !bytes.Equal(content, want.content) || header.Filename != filepath.Base(want.path) || header.Header.Get("Content-Type") != want.mime || len(request.MultipartForm.File) != 1 || len(request.MultipartForm.File["file"]) != 1 {
				fixture.t.Errorf("attachment %d name=%q mime=%q bytes=%d, want name=%q mime=%q bytes=%d read=%v", index, header.Filename, header.Header.Get("Content-Type"), len(content), filepath.Base(want.path), want.mime, len(want.content), err)
			}
			if fixture.rejectUpload == index+1 {
				writer.WriteHeader(http.StatusForbidden)
				fmt.Fprint(writer, `{"code":"permission_denied","data":null}`)
				return
			}
			json.NewEncoder(writer).Encode(map[string]any{"code": 0, "data": map[string]any{"id": want.id, "expires_at": 1893456000.25}})
		case request.Method == http.MethodPost && request.URL.Path == "/api/v1/operations/agent.debug.stream/stream":
			fixture.streams.Add(1)
			var body map[string]any
			if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
				fixture.t.Error(err)
			}
			if !reflect.DeepEqual(body, fixture.want) {
				fixture.t.Errorf("attachment debug body=%#v, want %#v", body, fixture.want)
			}
			if fixture.streamStatus != 0 {
				writer.WriteHeader(fixture.streamStatus)
				fmt.Fprint(writer, `{"code":"permission_denied","data":null}`)
				return
			}
			writer.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(writer, "data: {\"kind\":\"result\",\"data\":{\"text\":\"回应🙂\"}}\n\n")
			if !fixture.unfinishedStream {
				fmt.Fprint(writer, "event: done\ndata: {}\n\n")
			}
		default:
			fixture.t.Errorf("unexpected attachment debug request %s %s", request.Method, request.URL.Path)
			writer.WriteHeader(500)
		}
	}))
}

func agentDebugAttachmentArgs(files []agentDebugAttachment) []string {
	args := []string{"agent", "debug", "agent", "--yes"}
	for _, file := range files {
		args = append(args, "--attach", file.path)
	}
	return args
}

func agentDebugAttachmentSummary(t *testing.T, output, format string) map[string]any {
	t.Helper()
	envelopes := decodeOperationStream(t, output, format)
	if len(envelopes) == 0 {
		t.Fatalf("attachment debug produced no output %s", output)
	}
	data, ok := envelopes[len(envelopes)-1]["data"].(map[string]any)
	if !ok {
		t.Fatalf("attachment debug lost its final result %s", output)
	}
	return data
}

func assertAgentDebugAttachments(t *testing.T, data map[string]any, files []agentDebugAttachment, statuses ...string) {
	t.Helper()
	items, ok := data["attachments"].([]any)
	if !ok || len(items) != len(statuses) {
		t.Fatalf("attachment outcomes=%#v, want %v", data, statuses)
	}
	for index, status := range statuses {
		item := items[index].(map[string]any)
		if item["name"] != filepath.Base(files[index].path) || item["status"] != status {
			t.Fatalf("attachment %d=%#v, want status=%s", index, item, status)
		}
		if status == "uploaded" {
			expires, _ := json.Marshal(item["expires_at"])
			if item["artifact_id"] != files[index].id || string(expires) != "1893456000.25" {
				t.Fatalf("uploaded attachment identity or expiry lost %#v", item)
			}
		} else if item["artifact_id"] != nil {
			t.Fatalf("unconfirmed attachment claimed an Artifact ID %#v", item)
		}
	}
}

func TestAgentDebugAttachmentsUploadBinaryAndMergePayload(t *testing.T) {
	for _, test := range []struct {
		source, format string
		member         bool
	}{
		{source: "message", format: "json"}, {source: "file", format: "yaml"}, {source: "empty-payload", format: "json", member: true},
	} {
		t.Run(fmt.Sprint(test.source, "/", test.format, "/member=", test.member), func(t *testing.T) {
			files := agentDebugAttachmentFiles(t)
			payload := map[string]any{"text": " 你好🙂\n第二行 "}
			input := ""
			args := agentDebugAttachmentArgs(files)
			if test.source == "message" {
				args = append(args, "--message", payload["text"].(string))
			} else {
				payload = map[string]any{"artifact_ids": []any{"art_00000000000000000000000000000099"}, "event_type": "custom.event", "event_data": map[string]any{"count": float64(2)}}
				if test.source == "empty-payload" {
					payload = map[string]any{}
				}
				encoded, _ := json.Marshal(map[string]any{"payload": payload})
				input = string(encoded)
				args = append(args, "--file", "-")
			}
			ids, _ := payload["artifact_ids"].([]any)
			for _, file := range files {
				ids = append(ids, file.id)
			}
			payload["artifact_ids"] = ids
			fixture := &agentDebugAttachmentFixture{t: t, files: files, member: test.member, want: map[string]any{"agent_uuid": "agent", "payload": payload}}
			server := fixture.server()
			defer server.Close()
			args = append(args, "-o", test.format)
			var code int
			var output string
			if test.member {
				code, output = memberCLI(t, memberConfig(t, server.URL, "member-secret"), input, nil, append(args, "--as-member")...)
			} else {
				code, output = operationCLI(t, server, input, args...)
			}
			if code != 0 || fixture.uploads.Load() != 2 || fixture.streams.Load() != 1 || strings.Contains(output, "test-secret") || strings.Contains(output, "member-secret") {
				t.Fatalf("code=%d uploads=%d streams=%d output=%s", code, fixture.uploads.Load(), fixture.streams.Load(), output)
			}
			data := agentDebugAttachmentSummary(t, output, test.format)
			if data["debug_status"] != "completed" || data["completed"] != true {
				t.Fatalf("successful debug outcome %#v", data)
			}
			assertAgentDebugAttachments(t, data, files, "uploaded", "uploaded")
		})
	}
}

func TestAgentDebugAttachmentsValidateAllFilesAndCombinedLimitBeforeHTTP(t *testing.T) {
	files := agentDebugAttachmentFiles(t)
	oversized := filepath.Join(t.TempDir(), "oversized.png")
	large, err := os.Create(oversized)
	if err != nil {
		t.Fatal(err)
	}
	if err := large.Truncate((10 << 20) + 1); err != nil {
		t.Fatal(err)
	}
	large.Close()
	existing := []string{}
	for index := 0; index < 20; index++ {
		existing = append(existing, fmt.Sprintf("art_%032x", index+100))
	}
	combined, _ := json.Marshal(map[string]any{"payload": map[string]any{"artifact_ids": existing}})
	for _, test := range []struct {
		name, input string
		flags       []string
	}{
		{name: "later missing file", flags: []string{"--message", "hello", "--attach", files[0].path, "--attach", filepath.Join(t.TempDir(), "missing.png")}},
		{name: "later oversized file", flags: []string{"--message", "hello", "--attach", files[0].path, "--attach", oversized}},
		{name: "directory", flags: []string{"--message", "hello", "--attach", t.TempDir()}},
		{name: "blank path", flags: []string{"--message", "hello", "--attach", ""}},
		{name: "combined artifact limit", input: string(combined), flags: []string{"--file", "-", "--attach", files[0].path}},
		{name: "invalid artifact list", input: `{"payload":{"artifact_ids":"bad"}}`, flags: []string{"--file", "-", "--attach", files[0].path}},
		{name: "duplicate artifact IDs", input: `{"payload":{"artifact_ids":["art_00000000000000000000000000000099","art_00000000000000000000000000000099"]}}`, flags: []string{"--file", "-", "--attach", files[0].path}},
		{name: "missing text or event source", flags: []string{"--attach", files[0].path}},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := &agentDebugAttachmentFixture{t: t}
			server := fixture.server()
			defer server.Close()
			code, output := operationCLI(t, server, test.input, append([]string{"agent", "debug", "agent", "--yes"}, test.flags...)...)
			if code != 2 || fixture.requests.Load() != 0 {
				t.Fatalf("invalid attachments reached Core code=%d requests=%d output=%s", code, fixture.requests.Load(), output)
			}
		})
	}
	t.Run("21 new attachments", func(t *testing.T) {
		fixture := &agentDebugAttachmentFixture{t: t}
		server := fixture.server()
		defer server.Close()
		args := []string{"agent", "debug", "agent", "--message", "hello", "--yes"}
		for index := 0; index < 21; index++ {
			args = append(args, "--attach", files[0].path)
		}
		code, output := operationCLI(t, server, "", args...)
		if code != 2 || fixture.requests.Load() != 0 {
			t.Fatalf("oversized attachment count reached Core code=%d output=%s", code, output)
		}
	})
}

func TestAgentDebugAttachmentsAcceptExactSizeAndCombinedCountBoundaries(t *testing.T) {
	files := agentDebugAttachmentFiles(t)[:1]
	content := make([]byte, 10<<20)
	copy(content, files[0].content)
	content[len(content)-1] = 0x7f
	files[0].content = content
	if err := os.WriteFile(files[0].path, content, 0600); err != nil {
		t.Fatal(err)
	}
	ids := []any{}
	for index := 0; index < 19; index++ {
		ids = append(ids, fmt.Sprintf("art_%032x", index+100))
	}
	input, _ := json.Marshal(map[string]any{"payload": map[string]any{"artifact_ids": ids}})
	want := map[string]any{"agent_uuid": "agent", "payload": map[string]any{"artifact_ids": append(ids, files[0].id)}}
	fixture := &agentDebugAttachmentFixture{t: t, files: files, want: want}
	server := fixture.server()
	defer server.Close()
	transport := operationTransport(func(request *http.Request) (*http.Response, error) {
		if request.URL.Path == "/api/v1/artifacts" {
			response := httptest.NewRecorder()
			server.Config.Handler.ServeHTTP(response, request)
			return response.Result(), nil
		}
		return http.DefaultTransport.RoundTrip(request)
	})
	code, output := operationCLITransport(t, server, string(input), transport, append(agentDebugAttachmentArgs(files), "--file", "-")...)
	if code != 0 || fixture.uploads.Load() != 1 || fixture.streams.Load() != 1 {
		t.Fatalf("valid limit boundary failed code=%d output=%s", code, output)
	}
	assertAgentDebugAttachments(t, agentDebugAttachmentSummary(t, output, "json"), files, "uploaded")
}

func TestAgentDebugAttachmentsRequirePermissionsAndConfirmationBeforeUpload(t *testing.T) {
	file := agentDebugAttachmentFiles(t)[0]
	for _, permission := range []string{"resource.view", "resource.manage", "runtime.operate", "confirmation"} {
		t.Run(permission, func(t *testing.T) {
			var writes atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				if request.URL.Path == "/api/v1/system/context" && permission != "confirmation" {
					allowed := []string{}
					for _, value := range []string{"resource.view", "resource.manage", "runtime.operate"} {
						if value != permission {
							allowed = append(allowed, value)
						}
					}
					json.NewEncoder(writer).Encode(map[string]any{"code": 0, "data": map[string]any{"instance_uuid": "instance", "workspace_uuid": "workspace", "api_key_id": "key", "permissions": allowed}})
					return
				}
				if operationPreflight(writer, request, true) {
					return
				}
				writes.Add(1)
				writer.WriteHeader(500)
			}))
			defer server.Close()
			args := []string{"agent", "debug", "agent", "--message", "hello", "--attach", file.path}
			want := 6
			if permission != "confirmation" {
				args = append(args, "--yes")
				want = 4
			}
			code, output := operationCLI(t, server, "", args...)
			if code != want || writes.Load() != 0 {
				t.Fatalf("attachment write was not gated code=%d writes=%d output=%s", code, writes.Load(), output)
			}
			data := agentDebugAttachmentSummary(t, output, "json")
			if data["debug_status"] != "not_started" {
				t.Fatalf("permission or confirmation failure lost its debug stage %#v", data)
			}
			assertAgentDebugAttachments(t, data, []agentDebugAttachment{file}, "pending")
		})
	}
}

type agentDebugBrokenStream struct{ io.ReadCloser }

func (body agentDebugBrokenStream) Read(data []byte) (int, error) {
	n, err := body.ReadCloser.Read(data)
	if err == io.EOF {
		err = io.ErrUnexpectedEOF
	}
	return n, err
}

func TestAgentDebugAttachmentsPreservePartialAndUnknownResultsWithoutRetry(t *testing.T) {
	for _, scenario := range []string{"upload-rejected", "upload-unknown", "stream-rejected", "stream-unknown"} {
		t.Run(scenario, func(t *testing.T) {
			files := agentDebugAttachmentFiles(t)
			if strings.HasPrefix(scenario, "upload-") {
				third := files[0]
				third.path = filepath.Join(t.TempDir(), "pending.png")
				third.id = "art_00000000000000000000000000000003"
				if err := os.WriteFile(third.path, third.content, 0600); err != nil {
					t.Fatal(err)
				}
				files = append(files, third)
			}
			ids := []any{}
			for _, file := range files {
				ids = append(ids, file.id)
			}
			fixture := &agentDebugAttachmentFixture{t: t, files: files, want: map[string]any{"agent_uuid": "agent", "payload": map[string]any{"text": "hello", "artifact_ids": ids}}}
			if scenario == "upload-rejected" {
				fixture.rejectUpload = 2
			} else if scenario == "stream-rejected" {
				fixture.streamStatus = http.StatusForbidden
			} else if scenario == "stream-unknown" {
				fixture.unfinishedStream = true
			}
			server := fixture.server()
			defer server.Close()
			var attempts atomic.Int32
			transport := operationTransport(func(request *http.Request) (*http.Response, error) {
				if request.URL.Path == "/api/v1/artifacts" && attempts.Add(1) == 2 && scenario == "upload-unknown" {
					io.Copy(io.Discard, request.Body)
					request.Body.Close()
					return nil, io.ErrUnexpectedEOF
				}
				response, err := http.DefaultTransport.RoundTrip(request)
				if err == nil && strings.HasSuffix(request.URL.Path, "/stream") && scenario == "stream-unknown" {
					response.Body = agentDebugBrokenStream{ReadCloser: response.Body}
				}
				return response, err
			})
			code, output := operationCLITransport(t, server, "", transport, append(agentDebugAttachmentArgs(files), "--message", "hello")...)
			data := agentDebugAttachmentSummary(t, output, "json")
			wantCode := 4
			if strings.HasSuffix(scenario, "unknown") {
				wantCode = 7
			}
			if code != wantCode || attempts.Load() != 2 {
				t.Fatalf("attachment side effect retried code=%d attempts=%d output=%s", code, attempts.Load(), output)
			}
			if strings.HasPrefix(scenario, "upload-") {
				status := "failed"
				if scenario == "upload-unknown" {
					status = "unknown"
				}
				if data["debug_status"] != "not_started" || fixture.streams.Load() != 0 {
					t.Fatalf("debug started after a partial upload %#v", data)
				}
				assertAgentDebugAttachments(t, data, files, "uploaded", status, "pending")
			} else {
				status := "failed"
				if scenario == "stream-unknown" {
					status = "unknown"
				}
				if data["debug_status"] != status || fixture.streams.Load() != 1 {
					t.Fatalf("debug execution outcome lost %#v", data)
				}
				assertAgentDebugAttachments(t, data, files, "uploaded", "uploaded")
			}
		})
	}
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

func TestBotBindPreviewsOneRouteAndVerifiesTheWrite(t *testing.T) {
	for _, mode := range []string{"preview", "success", "conflict", "different readback", "ambiguous", "missing confirmation"} {
		t.Run(mode, func(t *testing.T) {
			selected := map[string]any{"id": "r", "target_uuid": "old", "target_type": "agent", "filters": []any{"private"}, "order": 2}
			other := map[string]any{"id": "other", "enabled": false}
			var saved map[string]any
			reads, binds := 0, 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if operationPreflight(w, r, true) {
					return
				}
				switch r.URL.Path {
				case "/api/v1/operations/bot.get":
					reads++
					binding := selected
					if saved != nil && mode != "different readback" {
						binding = saved
					}
					json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{"uuid": "bot", "adapter_config": map[string]any{"token": "unrelated-adapter-token"}, "event_bindings": []any{binding, other}}})
				case "/api/v1/operations/bot.routes.bind":
					binds++
					var body map[string]any
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Error(err)
					}
					old, _ := body["expected_binding"].(map[string]any)
					if old["id"] != "r" || old["target_uuid"] != "old" || old["order"] != float64(2) || body["target_uuid"] != "new" || body["target_type"] != "pipeline" || body["bot_uuid"] != "bot" || body["dry_run"] != (mode == "preview") {
						t.Errorf("binding request %v", body)
					}
					if mode == "conflict" {
						w.WriteHeader(409)
						fmt.Fprint(w, `{"code":"binding_conflict"}`)
						return
					}
					saved = map[string]any{"id": "r", "target_uuid": "new", "target_type": "pipeline", "filters": []any{"private"}, "order": 2}
					json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{"bot_uuid": "bot", "before": old, "binding": saved, "changed": true, "dry_run": mode == "preview"}})
				default:
					t.Errorf("unexpected request %s", r.URL.Path)
				}
			}))
			defer server.Close()
			args := []string{"bot", "routes", "bind", "bot", "--pipeline", "new"}
			if mode != "ambiguous" {
				args = append(args, "--binding", "r")
			}
			if mode == "preview" {
				args = append(args, "--dry-run")
			} else if mode != "missing confirmation" {
				args = append(args, "--yes")
			}
			code, out := operationCLI(t, server, "", args...)
			wantCode, wantReads, wantBinds := 0, 1, 1
			switch mode {
			case "success":
				wantReads = 2
			case "conflict":
				wantCode = 6
			case "different readback":
				wantReads, wantCode = 2, 6
			case "ambiguous":
				wantBinds, wantCode = 0, 2
			case "missing confirmation":
				wantBinds, wantCode = 0, 6
			}
			if binds != wantBinds || reads != wantReads || code != wantCode || strings.Contains(out, "unrelated-adapter-token") {
				t.Fatalf("binds=%d reads=%d code=%d %s", binds, reads, code, out)
			}
		})
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
