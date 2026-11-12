package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/langbot-app/langbot-cli/internal/api"
	"github.com/langbot-app/langbot-cli/internal/result"
)

type taskRoundTripFunc func(*http.Request) (*http.Response, error)

func (fn taskRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}

func taskResponse(body string) *http.Response {
	return taskHTTPResponse(http.StatusOK, body)
}

func taskHTTPResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func TestWaitForTaskPollsUntilSuccess(t *testing.T) {
	var calls atomic.Int32
	client := api.Client{Transport: taskRoundTripFunc(func(*http.Request) (*http.Response, error) {
		if calls.Add(1) == 1 {
			return taskResponse(`{"code":0,"data":{"id":1,"task_type":"user","kind":"knowledge_base.store","status":"running","error":null,"result":null,"created_at":1.0}}`), nil
		}
		return taskResponse(`{"code":0,"data":{"id":1,"task_type":"user","kind":"knowledge_base.store","status":"succeeded","error":null,"result":{"file_id":"file-a"},"created_at":1.0}}`), nil
	})}
	task, err := waitForTask(context.Background(), client, api.Target{Endpoint: "http://example.test"}, "1", WaitOptions{
		PollInterval: time.Millisecond,
		WaitTimeout:  100 * time.Millisecond,
	})
	if err != nil || task.Status != "succeeded" || calls.Load() != 2 {
		t.Fatalf("waitForTask() = %#v, error = %v, calls = %d", task, err, calls.Load())
	}
}

func TestWaitForTaskDistinguishesTimeoutAndCancellation(t *testing.T) {
	client := api.Client{Transport: taskRoundTripFunc(func(*http.Request) (*http.Response, error) {
		return taskResponse(`{"code":0,"data":{"id":1,"task_type":"user","kind":"knowledge_base.store","status":"running","error":null,"result":null,"created_at":1.0}}`), nil
	})}
	_, err := waitForTask(context.Background(), client, api.Target{Endpoint: "http://example.test"}, "1", WaitOptions{
		PollInterval: 50 * time.Millisecond,
		WaitTimeout:  5 * time.Millisecond,
	})
	if result.AsError(err).Type != "wait_timeout" {
		t.Fatalf("timeout error = %+v", result.AsError(err))
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = waitForTask(ctx, client, api.Target{Endpoint: "http://example.test"}, "1", WaitOptions{
		PollInterval: time.Millisecond,
		WaitTimeout:  time.Second,
	})
	if result.AsError(err).Type != "wait_cancelled" || !strings.Contains(result.AsError(err).Message, "未被取消") {
		t.Fatalf("cancel error = %+v", result.AsError(err))
	}
}

func TestWaitForTaskPreservesNetworkErrors(t *testing.T) {
	client := api.Client{Transport: taskRoundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("transport failure")
	})}
	_, err := waitForTask(context.Background(), client, api.Target{Endpoint: "http://example.test"}, "1", WaitOptions{
		PollInterval: time.Millisecond,
		WaitTimeout:  time.Second,
	})
	if result.AsError(err).Kind != "network" {
		t.Fatalf("network error = %+v", result.AsError(err))
	}
}

func TestWaitForTaskTimeoutIncludesInFlightRequest(t *testing.T) {
	client := api.Client{Transport: taskRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		<-request.Context().Done()
		return nil, request.Context().Err()
	})}
	started := time.Now()
	_, err := waitForTask(context.Background(), client, api.Target{
		Endpoint: "http://example.test",
		Timeout:  time.Second,
	}, "1", WaitOptions{
		PollInterval: time.Second,
		WaitTimeout:  10 * time.Millisecond,
	})
	if result.AsError(err).Type != "wait_timeout" {
		t.Fatalf("timeout error = %+v", result.AsError(err))
	}
	if elapsed := time.Since(started); elapsed > 250*time.Millisecond {
		t.Fatalf("wait timeout did not bound the in-flight request: %s", elapsed)
	}
}

func TestKnowledgeBaseIngestRunsUploadAndStoreOnce(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	filePath := filepath.Join(t.TempDir(), "document.txt")
	if err := os.WriteFile(filePath, []byte("document body"), 0600); err != nil {
		t.Fatal(err)
	}
	var uploadCalls, storeCalls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/api/v1/system/context":
			fmt.Fprint(w, `{"code":0,"data":{"instance_uuid":"instance-a","workspace_uuid":"workspace-a","api_key_id":"key-a","permissions":["resource.manage","resource.view"]}}`)
		case "/api/v1/system/capabilities":
			fmt.Fprint(w, `{"code":0,"data":{"schema_version":1,"operations":{"knowledge_base.file.store":{"supported":true},"knowledge_base.get":{"supported":true},"file.document.upload":{"supported":true},"task.get":{"supported":true}}}}`)
		case "/api/v1/knowledge/bases/kb-a":
			fmt.Fprint(w, `{"code":0,"data":{"base":{"uuid":"kb-a","name":"KB"}}}`)
		case "/api/v1/files/documents":
			uploadCalls++
			if err := request.ParseMultipartForm(1 << 20); err != nil {
				t.Fatalf("ParseMultipartForm() error = %v", err)
			}
			fmt.Fprint(w, `{"code":0,"data":{"file_id":"file-a"}}`)
		case "/api/v1/knowledge/bases/kb-a/files":
			storeCalls++
			fmt.Fprint(w, `{"code":0,"data":{"task_id":17}}`)
		case "/api/v1/system/tasks/17":
			fmt.Fprint(w, `{"code":0,"data":{"id":17,"task_type":"user","kind":"knowledge_base.store","status":"succeeded","error":null,"result":{"stored":true},"created_at":1.0}}`)
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
	service := New(Dependencies{LookupEnv: lookup, DefaultConfigPath: func() (string, error) { return configPath, nil }})
	if _, err := service.ContextAdd("production", server.URL, "KEY", "workspace-a", ""); err != nil {
		t.Fatalf("ContextAdd() error = %v", err)
	}
	value, err := service.KnowledgeBaseIngest(context.Background(), "kb-a", filePath, "", false, WaitOptions{
		Wait: true, PollInterval: time.Millisecond, WaitTimeout: time.Second,
	}, CheckOptions{ContextSet: true, Context: "production"})
	if err != nil {
		t.Fatalf("KnowledgeBaseIngest() error = %v", err)
	}
	data := value.Data.(map[string]any)
	if data["step"] != "completed" || data["file_id"] != "file-a" || data["task_id"] != "17" {
		t.Fatalf("KnowledgeBaseIngest() data = %#v", data)
	}
	if uploadCalls != 1 || storeCalls != 1 {
		t.Fatalf("upload/store calls = %d/%d", uploadCalls, storeCalls)
	}
}

func TestKnowledgeBaseIngestPreservesPartialAndUnknownSubmissionState(t *testing.T) {
	for _, test := range []struct {
		name          string
		storeResponse *http.Response
		storeError    error
		wantType      string
		wantStep      string
		wantSubmitted any
	}{
		{
			name:          "known rejection",
			storeResponse: taskHTTPResponse(http.StatusBadRequest, `{"code":"invalid_file","data":null}`),
			wantType:      "partial_write",
			wantStep:      "store_failed",
			wantSubmitted: false,
		},
		{
			name:          "unknown result",
			storeError:    errors.New("connection reset after request"),
			wantType:      "result_unknown",
			wantStep:      "store_unknown",
			wantSubmitted: "unknown",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			configPath := filepath.Join(t.TempDir(), "config.yaml")
			filePath := filepath.Join(t.TempDir(), "document.txt")
			if err := os.WriteFile(filePath, []byte("document body"), 0o600); err != nil {
				t.Fatal(err)
			}
			var uploadCalls, storeCalls atomic.Int32
			transport := taskRoundTripFunc(func(request *http.Request) (*http.Response, error) {
				switch request.URL.Path {
				case "/api/v1/system/context":
					return taskResponse(`{"code":0,"data":{"instance_uuid":"instance-a","workspace_uuid":"workspace-a","api_key_id":"key-a","permissions":["resource.manage","resource.view"]}}`), nil
				case "/api/v1/system/capabilities":
					return taskResponse(`{"code":0,"data":{"schema_version":1,"operations":{"knowledge_base.file.store":{"supported":true},"knowledge_base.get":{"supported":true},"file.document.upload":{"supported":true}}}}`), nil
				case "/api/v1/knowledge/bases/kb-a":
					return taskResponse(`{"code":0,"data":{"base":{"uuid":"kb-a","name":"KB"}}}`), nil
				case "/api/v1/files/documents":
					uploadCalls.Add(1)
					return taskResponse(`{"code":0,"data":{"file_id":"file-a"}}`), nil
				case "/api/v1/knowledge/bases/kb-a/files":
					storeCalls.Add(1)
					return test.storeResponse, test.storeError
				default:
					return nil, fmt.Errorf("unexpected path %s", request.URL.Path)
				}
			})
			service := New(Dependencies{
				LookupEnv:         func(name string) (string, bool) { return "secret", name == "KEY" },
				DefaultConfigPath: func() (string, error) { return configPath, nil },
				Transport:         transport,
			})
			if _, err := service.ContextAdd("production", "http://example.test", "KEY", "workspace-a", ""); err != nil {
				t.Fatalf("ContextAdd() error = %v", err)
			}
			value, err := service.KnowledgeBaseIngest(
				context.Background(), "kb-a", filePath, "", false, WaitOptions{},
				CheckOptions{ContextSet: true, Context: "production"},
			)
			failure := result.AsError(err)
			if failure.Type != test.wantType {
				t.Fatalf("error = %+v, want type %q", failure, test.wantType)
			}
			data := value.Data.(map[string]any)
			if data["step"] != test.wantStep || data["submitted"] != test.wantSubmitted || data["file_id"] != "file-a" {
				t.Fatalf("data = %#v", data)
			}
			if uploadCalls.Load() != 1 || storeCalls.Load() != 1 {
				t.Fatalf("upload/store calls = %d/%d", uploadCalls.Load(), storeCalls.Load())
			}
		})
	}
}

func TestKnowledgeBaseIngestWaitRequiresTaskCapabilityBeforeWriting(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	filePath := filepath.Join(t.TempDir(), "document.txt")
	if err := os.WriteFile(filePath, []byte("document body"), 0o600); err != nil {
		t.Fatal(err)
	}
	var writes atomic.Int32
	transport := taskRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		switch request.URL.Path {
		case "/api/v1/system/context":
			return taskResponse(`{"code":0,"data":{"instance_uuid":"instance-a","workspace_uuid":"workspace-a","api_key_id":"key-a","permissions":["resource.manage","resource.view"]}}`), nil
		case "/api/v1/system/capabilities":
			return taskResponse(`{"code":0,"data":{"schema_version":1,"operations":{"knowledge_base.file.store":{"supported":true},"knowledge_base.get":{"supported":true},"file.document.upload":{"supported":true}}}}`), nil
		case "/api/v1/knowledge/bases/kb-a":
			return taskResponse(`{"code":0,"data":{"base":{"uuid":"kb-a","name":"KB"}}}`), nil
		default:
			writes.Add(1)
			return nil, fmt.Errorf("unexpected request %s", request.URL.Path)
		}
	})
	service := New(Dependencies{
		LookupEnv:         func(name string) (string, bool) { return "secret", name == "KEY" },
		DefaultConfigPath: func() (string, error) { return configPath, nil },
		Transport:         transport,
	})
	if _, err := service.ContextAdd("production", "http://example.test", "KEY", "workspace-a", ""); err != nil {
		t.Fatalf("ContextAdd() error = %v", err)
	}
	if _, err := service.KnowledgeBaseIngest(
		context.Background(), "kb-a", filePath, "", true, WaitOptions{},
		CheckOptions{ContextSet: true, Context: "production"},
	); err != nil {
		t.Fatalf("dry-run should not require task capability: %v", err)
	}
	_, err := service.KnowledgeBaseIngest(
		context.Background(), "kb-a", filePath, "", false, WaitOptions{Wait: true},
		CheckOptions{ContextSet: true, Context: "production"},
	)
	if failure := result.AsError(err); failure.Kind != "precondition" {
		t.Fatalf("error = %+v, want precondition", failure)
	}
	if writes.Load() != 0 {
		t.Fatalf("missing task capability allowed %d resource requests", writes.Load())
	}
}

func TestCollectIngestFilesRecursesDeduplicatesAndSkipsDotEntries(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"a.txt", "nested/b.md", "comma,name.txt", ".env", ".git/config", "nested/.hidden"} {
		path := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(name), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// Symlink creation may be unavailable to non-administrator Windows users.
	_ = os.Symlink(filepath.Join(dir, "a.txt"), filepath.Join(dir, "linked.txt"))
	files, err := collectIngestFiles(context.Background(), []string{filepath.Join(dir, "a.txt"), filepath.Join(dir, ".", "a.txt")}, dir)
	if err != nil || len(files) != 3 {
		t.Fatalf("files=%v err=%v", files, err)
	}
	for index, name := range []string{"a.txt", "comma,name.txt", "b.md"} {
		if filepath.Base(files[index].path) != name {
			t.Fatalf("unexpected order or duplicate: %v", files)
		}
	}
	link := filepath.Join(t.TempDir(), "linked-directory")
	if os.Symlink(dir, link) == nil {
		if _, err := collectIngestFiles(context.Background(), nil, link); err == nil {
			t.Fatal("followed a symlink supplied as the directory root")
		}
	}
}

func TestKnowledgeBatchRejectsInvalidInputsBeforeAnyRequest(t *testing.T) {
	dir := t.TempDir()
	valid := filepath.Join(dir, "valid.txt")
	archive := filepath.Join(dir, "archive.zip")
	large := filepath.Join(dir, "large.pdf")
	for _, path := range []string{valid, archive, large} {
		if err := os.WriteFile(path, []byte("fixture"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	f, err := os.OpenFile(large, os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	err = f.Truncate(maxExtensionUploadBytes + 1)
	_ = f.Close()
	if err != nil {
		t.Fatal(err)
	}
	empty := t.TempDir()
	for _, test := range []struct {
		name  string
		files []string
		dir   string
		wait  bool
	}{
		{"missing", nil, "", false},
		{"stdin", []string{valid, "-"}, "", false},
		{"missing file", []string{valid, filepath.Join(dir, "missing.txt")}, "", false},
		{"oversized", []string{valid, large}, "", false},
		{"directory as file", []string{valid, dir}, "", false},
		{"file as directory", nil, valid, false},
		{"empty directory", nil, empty, false},
		{"archive batch wait", []string{valid, archive}, "", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			var requests int
			service := New(Dependencies{Transport: taskRoundTripFunc(func(*http.Request) (*http.Response, error) {
				requests++
				return nil, errors.New("unexpected network request")
			})})
			_, err := service.KnowledgeBaseIngestFiles(context.Background(), "kb-a", test.files, test.dir, "", false, WaitOptions{Wait: test.wait}, CheckOptions{})
			if result.AsError(err).Kind != "input" || requests != 0 {
				t.Fatalf("err=%v requests=%d", err, requests)
			}
		})
	}
}

func TestKnowledgeBatchKeepsPerFileOutcomesAndUsesOnePreflight(t *testing.T) {
	for _, test := range []struct {
		name    string
		want    []string
		uploads int
	}{
		{"completed", []string{"completed", "completed", "completed"}, 3},
		{"submitted", []string{"submitted", "submitted", "submitted"}, 3},
		{"dry-run", []string{"planned", "planned", "planned"}, 0},
		{"stdin-key", []string{"completed", "completed", "completed"}, 3},
		{"task-failure", []string{"completed", "failed", "completed"}, 3},
		{"store-rejection", []string{"completed", "failed", "completed"}, 3},
		{"unknown", []string{"completed", "unknown", "not_started"}, 2},
		{"wait-timeout", []string{"completed", "pending", "not_started"}, 2},
		{"cancel", []string{"completed", "not_started", "not_started"}, 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			paths := []string{}
			for _, name := range []string{"a.txt", "b.txt", "c.txt"} {
				path := filepath.Join(dir, name)
				if err := os.WriteFile(path, []byte(name), 0o600); err != nil {
					t.Fatal(err)
				}
				paths = append(paths, path)
			}
			var uploads, stores, identities, capabilities int
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			transport := taskRoundTripFunc(func(request *http.Request) (*http.Response, error) {
				switch request.URL.Path {
				case "/api/v1/system/context":
					identities++
					return taskResponse(`{"code":0,"data":{"instance_uuid":"instance-a","workspace_uuid":"workspace-a","api_key_id":"key-a","permissions":["resource.manage","resource.view"]}}`), nil
				case "/api/v1/system/capabilities":
					capabilities++
					return taskResponse(`{"code":0,"data":{"schema_version":1,"operations":{"knowledge_base.file.store":{"supported":true},"knowledge_base.get":{"supported":true},"file.document.upload":{"supported":true},"task.get":{"supported":true}}}}`), nil
				case "/api/v1/knowledge/bases/kb-a":
					return taskResponse(`{"code":0,"data":{"base":{"uuid":"kb-a","name":"KB"}}}`), nil
				case "/api/v1/files/documents":
					uploads++
					file, header, err := request.FormFile("file")
					if err != nil {
						t.Fatal(err)
					}
					content, err := io.ReadAll(file)
					_ = file.Close()
					if err != nil || string(content) != header.Filename || header.Filename != filepath.Base(paths[uploads-1]) {
						t.Fatalf("upload mismatch: %q %q %v", content, header.Filename, err)
					}
					return taskResponse(fmt.Sprintf(`{"code":0,"data":{"file_id":"file-%d"}}`, uploads)), nil
				case "/api/v1/knowledge/bases/kb-a/files":
					stores++
					var body map[string]any
					if err := json.NewDecoder(request.Body).Decode(&body); err != nil || body["parser_plugin_id"] != "parser-a" || body["file_id"] != fmt.Sprintf("file-%d", stores) {
						t.Fatalf("store body=%v err=%v", body, err)
					}
					if stores == 2 && test.name == "unknown" {
						return nil, errors.New("response lost after submission")
					}
					if stores == 2 && test.name == "store-rejection" {
						return taskHTTPResponse(400, `{"code":"invalid_file","data":null}`), nil
					}
					return taskResponse(fmt.Sprintf(`{"code":0,"data":{"task_id":%d}}`, stores)), nil
				default:
					if strings.HasPrefix(request.URL.Path, "/api/v1/system/tasks/") {
						id := strings.TrimPrefix(request.URL.Path, "/api/v1/system/tasks/")
						status, taskError := "succeeded", "null"
						if id == "2" && test.name == "task-failure" {
							status, taskError = "failed", `{"type":"parse_error","message":"fixture rejected"}`
						}
						if id == "2" && test.name == "wait-timeout" {
							status = "running"
						}
						if test.name == "cancel" {
							cancel()
						}
						return taskResponse(fmt.Sprintf(`{"code":0,"data":{"id":%s,"task_type":"user","kind":"knowledge-operation","status":%q,"error":%s,"result":null,"created_at":1}}`, id, status, taskError)), nil
					}
					return nil, errors.New("unexpected request")
				}
			})
			service := New(Dependencies{In: strings.NewReader("secret\n"), Transport: transport,
				LookupEnv:         func(name string) (string, bool) { return "secret", name == "KEY" },
				DefaultConfigPath: func() (string, error) { return filepath.Join(dir, "config.yaml"), nil },
			})
			if _, err := service.ContextAdd("production", "http://example.test", "KEY", "workspace-a", ""); err != nil {
				t.Fatal(err)
			}
			waitTimeout := time.Second
			if test.name == "wait-timeout" {
				waitTimeout = 200 * time.Millisecond
			}
			value, err := service.KnowledgeBaseIngestFiles(ctx, "kb-a", paths, "", "parser-a", test.name == "dry-run", WaitOptions{
				Wait: test.name != "submitted", PollInterval: time.Millisecond, WaitTimeout: waitTimeout,
			}, CheckOptions{Context: "production", ContextSet: true, APIKeyStdin: test.name == "stdin-key"})
			data, ok := value.Data.(map[string]any)
			if !ok {
				t.Fatalf("batch did not return data: %v", err)
			}
			rows := data["files"].([]map[string]any)
			for index, want := range test.want {
				if rows[index]["status"] != want {
					t.Fatalf("row %d: want %s got %#v (err=%v)", index, want, rows[index], err)
				}
			}
			wantError := test.want[1] == "failed" || test.want[1] == "unknown" || test.want[1] == "pending" || test.name == "cancel"
			if (err != nil) != wantError || uploads != test.uploads || stores != test.uploads || identities != 1 || capabilities != 1 {
				t.Fatalf("err=%v upload/store=%d/%d identity/capability=%d/%d", err, uploads, stores, identities, capabilities)
			}
			if test.name == "unknown" && (rows[1]["file_id"] != "file-2" || data["summary"].(map[string]int)["not_started"] != 1) {
				t.Fatalf("lost partial state: %#v", data)
			}
		})
	}
}
