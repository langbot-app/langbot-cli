package app

import (
	"context"
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
