package app

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/langbot-app/langbot-cli/internal/api"
)

func TestModelListAggregatesAllModelTypes(t *testing.T) {
	service, options, closeServer := providerModelTestService(t, []string{"resource.view"}, nil, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Query().Get("include_secret") != "false" {
			t.Fatalf("model list request = %s %s", r.Method, r.URL.RequestURI())
		}
		modelType := strings.TrimPrefix(r.URL.Path, "/api/v1/provider/models/")
		writeData(w, map[string]any{"models": []map[string]any{{"uuid": modelType + "-model", "name": modelType}}})
	})
	defer closeServer()

	result, err := service.ModelList(context.Background(), "", "", options)
	if err != nil {
		t.Fatalf("ModelList() error = %v", err)
	}
	models := result.Data.(map[string]any)["models"].(map[string]any)
	for _, modelType := range []string{api.ModelTypeLLM, api.ModelTypeEmbedding, api.ModelTypeRerank} {
		items, ok := models[modelType].([]map[string]any)
		if !ok || len(items) != 1 || items[0]["uuid"] != modelType+"-model" {
			t.Fatalf("models[%s] = %#v", modelType, models[modelType])
		}
	}
}

func TestProviderCreateUsesSensitivePermissionAndSafeReadback(t *testing.T) {
	var providerExists bool
	var writes int
	service, options, closeServer := providerModelTestService(t, []string{"provider_secret.manage", "resource.view"}, nil, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/provider/providers":
			providerExists = true
			writes++
			fmt.Fprint(w, `{"code":0,"data":{"uuid":"provider-a"}}`)
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/provider/providers/provider-a":
			if !providerExists {
				writeNotFound(w)
				return
			}
			fmt.Fprint(w, `{"code":0,"data":{"provider":{"uuid":"provider-a","name":"OpenAI","requester":"openai","api_keys":["***"],"base_url":"https://***@example.test"}}}`)
		default:
			writeNotFound(w)
		}
	})
	defer closeServer()

	body := map[string]any{"name": "OpenAI", "requester": "openai", "api_key": "request-secret"}
	dryRun, err := service.ProviderCreate(context.Background(), body, true, options)
	if err != nil || writes != 0 {
		t.Fatalf("dry-run = %#v, error = %v, writes = %d", dryRun, err, writes)
	}
	created, err := service.ProviderCreate(context.Background(), body, false, options)
	if err != nil {
		t.Fatalf("ProviderCreate() error = %v", err)
	}
	if created.Data.(map[string]any)["verified"] != true {
		t.Fatalf("ProviderCreate() = %#v", created)
	}
	encoded, _ := json.Marshal(created.Data)
	if strings.Contains(string(encoded), "request-secret") {
		t.Fatalf("secret leaked in result: %s", encoded)
	}
}

func TestProviderDeleteRequiresConfirmationAndVerifiesNotFound(t *testing.T) {
	var providerExists = true
	var writes int
	service, options, closeServer := providerModelTestService(t, []string{"resource.manage", "resource.view"}, nil, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/provider/providers/provider-a" {
			writeNotFound(w)
			return
		}
		switch r.Method {
		case http.MethodDelete:
			writes++
			providerExists = false
			fmt.Fprint(w, `{"code":0,"data":null}`)
		case http.MethodGet:
			if providerExists {
				fmt.Fprint(w, `{"code":0,"data":{"provider":{"uuid":"provider-a"}}}`)
			} else {
				writeNotFound(w)
			}
		default:
			writeNotFound(w)
		}
	})
	defer closeServer()

	if _, err := service.ProviderDelete(context.Background(), "provider-a", false, false, options); resultError(err).Kind != "input" {
		t.Fatalf("delete without confirmation error = %+v", resultError(err))
	}
	if writes != 0 {
		t.Fatalf("delete without confirmation wrote %d times", writes)
	}
	deleted, err := service.ProviderDelete(context.Background(), "provider-a", true, false, options)
	if err != nil || deleted.Data.(map[string]any)["verified"] != true || writes != 1 {
		t.Fatalf("delete = %#v, error = %v, writes = %d", deleted, err, writes)
	}
}

func TestModelTestDryRunDoesNotPost(t *testing.T) {
	var testCalls int
	var requestBody string
	service, options, closeServer := providerModelTestService(t, []string{"provider_secret.manage", "resource.view"}, nil, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/provider/models/llm/model-a":
			fmt.Fprint(w, `{"code":0,"data":{"model":{"uuid":"model-a","name":"model"}}}`)
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/provider/models/llm/model-a/test":
			testCalls++
			body, err := io.ReadAll(r.Body)
			if err != nil {
				t.Fatal(err)
			}
			requestBody = string(body)
			fmt.Fprint(w, `{"code":0,"data":null}`)
		default:
			writeNotFound(w)
		}
	})
	defer closeServer()

	dryRun, err := service.ModelTest(context.Background(), api.ModelTypeLLM, "model-a", map[string]any{"prompt": "hello"}, true, options)
	if err != nil || testCalls != 0 || dryRun.Data.(map[string]any)["server_write"] != false {
		t.Fatalf("dry-run = %#v, error = %v, test calls = %d", dryRun, err, testCalls)
	}
	tested, err := service.ModelTest(context.Background(), api.ModelTypeLLM, "model-a", nil, false, options)
	if err != nil || testCalls != 1 || requestBody != "{}" {
		t.Fatalf("test = %#v, error = %v, calls = %d, body = %q", tested, err, testCalls, requestBody)
	}
	data := tested.Data.(map[string]any)
	if data["executed"] != true || data["business_validation"] != "target_exists" {
		t.Fatalf("test result = %#v", data)
	}
}

func TestModelTestReportsUnknownWithoutRetry(t *testing.T) {
	var testCalls int
	service, options, closeServer := providerModelTestService(t, []string{"provider_secret.manage", "resource.view"}, nil, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/provider/models/llm/model-a":
			fmt.Fprint(w, `{"code":0,"data":{"model":{"uuid":"model-a","name":"model"}}}`)
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/provider/models/llm/model-a/test":
			testCalls++
			w.WriteHeader(http.StatusInternalServerError)
			fmt.Fprint(w, `{"code":"test_failed","data":null}`)
		default:
			writeNotFound(w)
		}
	})
	defer closeServer()

	result, err := service.ModelTest(context.Background(), api.ModelTypeLLM, "model-a", nil, false, options)
	failure := resultError(err)
	if failure.Type != "result_unknown" || testCalls != 1 {
		t.Fatalf("result = %#v, error = %+v, calls = %d", result, failure, testCalls)
	}
}

func providerModelTestService(t *testing.T, permissions, operations []string, handler http.HandlerFunc) (*Service, CheckOptions, func()) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/system/context":
			writeData(w, map[string]any{
				"instance_uuid": "instance-a", "workspace_uuid": "workspace-a", "api_key_id": "key-a", "permissions": permissions,
			})
		case "/api/v1/system/capabilities":
			available := map[string]bool{}
			for _, operation := range api.CapabilityOperationIDs() {
				available[operation] = true
			}
			for _, operation := range operations {
				available[operation] = true
			}
			items := make(map[string]map[string]bool, len(available))
			for operation, supported := range available {
				items[operation] = map[string]bool{"supported": supported}
			}
			writeData(w, map[string]any{"schema_version": 1, "operations": items})
		default:
			handler(w, r)
		}
	}))
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	service := New(Dependencies{
		LookupEnv:         func(name string) (string, bool) { return "secret", name == "KEY" },
		DefaultConfigPath: func() (string, error) { return configPath, nil },
	})
	if _, err := service.ContextAdd("production", server.URL, "KEY", "workspace-a", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := service.ContextUse("production"); err != nil {
		t.Fatal(err)
	}
	return service, CheckOptions{ContextSet: true, Context: "production"}, server.Close
}
