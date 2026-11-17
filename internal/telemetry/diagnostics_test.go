package telemetry

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/langbot-app/langbot-cli/internal/diagnostic"
)

func TestEnvironmentProxyPresence(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	for _, test := range []struct {
		name string
		env  map[string]string
		want bool
	}{
		{"absent", nil, false},
		{"empty uppercase", map[string]string{"HTTP_PROXY": "", "HTTPS_PROXY": ""}, false},
		{"empty lowercase", map[string]string{"http_proxy": "", "https_proxy": ""}, false},
		{"uppercase", map[string]string{"HTTP_PROXY": "http://proxy", "HTTPS_PROXY": "http://proxy"}, true},
		{"lowercase with empty uppercase", map[string]string{"HTTP_PROXY": "", "HTTPS_PROXY": "", "http_proxy": "http://proxy", "https_proxy": "http://proxy"}, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			client := New(Options{Home: t.TempDir(), LookupEnv: lookup(test.env)})
			operation := &Operation{client: client}
			environment := operation.environment(t.Context())
			for _, key := range []string{"http_proxy_set", "https_proxy_set"} {
				if environment[key] != test.want {
					t.Fatalf("%s = %v, want %v", key, environment[key], test.want)
				}
			}
		})
	}
}

func TestFailureDiagnostics(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	home := t.TempDir()
	dir := filepath.Join(home, "instance")
	if err := os.MkdirAll(filepath.Join(dir, "logs"), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"install.log", "langbot.log"} {
		if err := os.WriteFile(filepath.Join(dir, "logs", name), []byte(strings.Repeat("progress\n", 100)+"api_key: secret-value\nplain-custom-local-key\n"+name), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, ".api-key"), []byte("plain-custom-local-key"), 0o600); err != nil {
		t.Fatal(err)
	}
	var mutex sync.Mutex
	var events []Event
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var event Event
		if err := json.NewDecoder(request.Body).Decode(&event); err != nil {
			t.Error(err)
		}
		mutex.Lock()
		events = append(events, event)
		mutex.Unlock()
	}))
	defer server.Close()
	for index, step := range []string{"install_package", "health_check", "start_service"} {
		client := New(Options{Home: home, LookupEnv: lookup(map[string]string{"LBCTL_TELEMETRY_ENDPOINT": server.URL, "HTTPS_PROXY": "https://secret-value"})})
		ctx, operation := client.Begin(t.Context(), "install")
		if operation.Event.PreviousFailures != index {
			t.Fatalf("counter: %+v", operation.Event)
		}
		SetDirectory(ctx, dir)
		SetStep(ctx, step)
		RecordCommand(ctx, []string{"uv", "--token", "secret-value", "pip"}, 17, strings.Repeat("output\n", 90), "token=secret-value\nconnection refused")
		outcome := "dependency"
		if index == 2 {
			outcome = "interrupted"
		}
		operation.Finish(errors.New("failed at /home/alice/config.yaml"), outcome)
		client.Close()
		if operation.Event.Outcome == "succeeded" || operation.Event.Details == nil {
			t.Fatal(operation.Event)
		}
		body, _ := json.Marshal(operation.Event)
		if strings.Contains(string(body), "secret-value") || strings.Contains(string(body), "plain-custom-local-key") || len(body) > diagnostic.EventLimit {
			t.Fatalf("unsafe details: %s", body)
		}
		log := operation.Event.Details["log"].(map[string]any)
		want := "install.log"
		if index > 0 {
			want = "langbot.log"
		}
		if log["file"] != want || len(strings.Split(log["tail"].(string), "\n")) > 80 {
			t.Fatal(log)
		}
		process := operation.Event.Details["process"].(map[string]any)
		if process["exit_code"] != 17 || len(strings.Split(process["stdout"].(string), "\n")) > 60 {
			t.Fatal(process)
		}
		environment := operation.Event.Details["environment"].(map[string]any)
		if environment["https_proxy_set"] != true || environment["cpu_count"] == nil {
			t.Fatal(environment)
		}
	}
	mutex.Lock()
	defer mutex.Unlock()
	started := map[string]int{}
	for _, event := range events {
		if event.Outcome == "started" {
			started[event.AttemptID]++
		}
	}
	if len(events) != 6 || len(started) != 3 {
		t.Fatalf("events=%+v", events)
	}
	for _, event := range events {
		if started[event.AttemptID] != 1 {
			t.Fatalf("missing correlation: %+v", event)
		}
	}
}

func TestProbeSources(t *testing.T) {
	var methods []string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		methods = append(methods, request.Method)
		if request.URL.RawQuery != "" || request.Header.Get("Authorization") != "" {
			t.Error("probe sent credentials")
		}
		writer.WriteHeader(http.StatusForbidden)
	}))
	defer server.Close()
	op := &Operation{Event: Event{Step: "install_package"}}
	ctx := context.WithValue(t.Context(), operationKey{}, op)
	RecordSource(ctx, strings.Replace(server.URL, "://", "://user:pass@", 1)+"/simple?token=secret-value")
	RecordSource(ctx, server.URL+"/simple")
	var wait sync.WaitGroup
	for range 10 {
		wait.Add(1)
		go func() { defer wait.Done(); RecordSource(ctx, server.URL+"/simple") }()
	}
	wait.Wait()
	result := probeSources(t.Context(), op.sources["install_package"])
	if len(result) != 1 || result[0].(map[string]any)["status_code"] != 403 || len(methods) != 1 || methods[0] != "HEAD" {
		t.Fatal(result, methods)
	}
	SetStep(ctx, "health_check")
	RecordSource(ctx, server.URL)
	if len(op.sources["health_check"]) != 0 {
		t.Fatal("probed non-download step")
	}
}

func TestProbeDeadline(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) { <-request.Context().Done() }))
	defer server.Close()
	started := time.Now()
	result := probeSources(t.Context(), []string{server.URL + "/one", server.URL + "/two", server.URL + "/three"})
	if time.Since(started) > 5*time.Second {
		t.Fatal("probe budget exceeded")
	}
	for _, probe := range result {
		if probe.(map[string]any)["error_type"] != "timeout" {
			t.Fatal(probe)
		}
	}
}

func TestEventBudget(t *testing.T) {
	client := New(Options{Home: t.TempDir(), LookupEnv: lookup(nil)})
	defer client.Close()
	var details = map[string]any{}
	for _, key := range []string{"stdout", "stderr", "log", "args", "environment"} {
		details[key] = strings.Repeat("\"文\n", 8192)
	}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var event map[string]any
		if err := json.NewDecoder(request.Body).Decode(&event); err != nil {
			t.Error(err)
		}
		body, _ := json.Marshal(event)
		if len(body) > diagnostic.EventLimit {
			t.Errorf("event is %d bytes", len(body))
		}
	}))
	defer server.Close()
	client.lookup = lookup(map[string]string{"LBCTL_TELEMETRY_ENDPOINT": server.URL})
	client.send(Event{Details: diagnostic.Object(details), Error: strings.Repeat("文", 2700)})
	client.Close()
}
