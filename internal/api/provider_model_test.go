package api

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestProviderAndModelReadsForceSafeProjection(t *testing.T) {
	var requests []string
	client := Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		requests = append(requests, request.Method+" "+request.URL.RequestURI())
		var body string
		switch request.URL.Path {
		case providersPath:
			body = `{"code":0,"data":{"providers":[{"uuid":"provider-a","name":"Provider","requester":"chatcmpl","base_url":"https://user:password@example.test/v1?token=url-secret","api_keys":["provider-secret"],"unknown_secret":"hidden"}]}}`
		case providersPath + "/provider-a":
			body = `{"code":0,"data":{"provider":{"uuid":"provider-a","name":"Provider","requester":"chatcmpl","base_url":"https://user:password@example.test/v1?token=url-secret","api_keys":["provider-secret"],"unknown_secret":"hidden"}}}`
		case llmModelsPath, embeddingModelsPath, rerankModelsPath:
			body = `{"code":0,"data":{"models":[{"uuid":"model-a","name":"Model","provider_uuid":"provider-a","extra_args":{"Authorization":"Bearer model-secret","temperature":0.2},"unknown_secret":"hidden"}]}}`
		case llmModelsPath + "/model-a", embeddingModelsPath + "/model-a", rerankModelsPath + "/model-a":
			body = `{"code":0,"data":{"model":{"uuid":"model-a","name":"Model","provider_uuid":"provider-a","extra_args":{"Authorization":"Bearer model-secret","temperature":0.2},"unknown_secret":"hidden"}}}`
		default:
			return nil, fmt.Errorf("unexpected path %s", request.URL.Path)
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
	})}
	target := Target{Endpoint: "http://example.test", APIKey: "connection-secret"}
	providerList, err := client.Providers(context.Background(), target)
	if err != nil || len(providerList) != 1 {
		t.Fatalf("Providers() = %#v, error = %v", providerList, err)
	}
	provider, err := client.Provider(context.Background(), target, "provider-a")
	if err != nil {
		t.Fatalf("Provider() error = %v", err)
	}
	if provider["api_keys"].([]any)[0] != "***" || provider["base_url"] != "https://***@example.test/v1?token=***" {
		t.Fatalf("Provider() did not redact credentials: %#v", provider)
	}
	if _, exposed := provider["unknown_secret"]; exposed {
		t.Fatal("Provider() exposed an unknown response field")
	}
	for _, modelType := range []string{ModelTypeLLM, ModelTypeEmbedding, ModelTypeRerank} {
		models, err := client.Models(context.Background(), target, modelType, "")
		if err != nil || len(models) != 1 {
			t.Fatalf("Models(%s) = %#v, error = %v", modelType, models, err)
		}
		model, err := client.Model(context.Background(), target, modelType, "model-a")
		if err != nil {
			t.Fatalf("Model(%s) error = %v", modelType, err)
		}
		if model["extra_args"].(map[string]any)["Authorization"] != "***" {
			t.Fatalf("Model(%s) did not redact extra_args: %#v", modelType, model)
		}
		if _, exposed := model["unknown_secret"]; exposed {
			t.Fatalf("Model(%s) exposed an unknown response field", modelType)
		}
	}
	for _, request := range requests {
		if !strings.Contains(request, "scan-models") && !strings.Contains(request, "/test") &&
			!strings.Contains(request, "include_secret=false") {
			t.Fatalf("read request omitted include_secret=false: %s", request)
		}
	}
}

func TestProviderModelOperationsDoNotExposeActiveOperationsThroughGenericAPI(t *testing.T) {
	for _, test := range []struct {
		method string
		path   string
	}{
		{method: http.MethodGet, path: providersPath},
		{method: http.MethodGet, path: providersPath + "/provider-a"},
		{method: http.MethodGet, path: llmModelsPath},
		{method: http.MethodPost, path: llmModelsPath + "/model-a/test"},
		{method: http.MethodGet, path: providersPath + "/provider-a/scan-models"},
	} {
		if _, ok := ResolveOperation(test.method, test.path); ok {
			t.Fatalf("ResolveOperation(%s, %s) unexpectedly allowed provider/model operation", test.method, test.path)
		}
	}
}

func TestProviderScanModelsDropsUnboundedDebugData(t *testing.T) {
	client := Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		body := `{"code":0,"data":{"models":[{"id":"model-a","name":"Model A","type":"llm","already_added":false,"unknown":"hidden"}],"debug":{"Authorization":"Bearer provider-secret","raw":"unbounded"}}}`
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
	})}
	result, err := client.ProviderScanModels(context.Background(), Target{Endpoint: "http://example.test", APIKey: "connection-secret"}, "provider-a", "")
	if err != nil {
		t.Fatalf("ProviderScanModels() error = %v", err)
	}
	if _, exposed := result["debug"]; exposed {
		t.Fatal("ProviderScanModels() exposed debug data")
	}
	models := result["models"].([]map[string]any)
	if len(models) != 1 || models[0]["id"] != "model-a" {
		t.Fatalf("ProviderScanModels() = %#v", result)
	}
	if _, exposed := models[0]["unknown"]; exposed {
		t.Fatal("ProviderScanModels() exposed an unknown model field")
	}
}

func TestProviderModelReadQueryAllowlist(t *testing.T) {
	allowed := []string{
		providersPath + "?include_secret=false",
		providersPath + "/provider-a?include_secret=false",
		llmModelsPath + "?include_secret=false",
		embeddingModelsPath + "?include_secret=false&provider_uuid=provider-a",
		rerankModelsPath + "/model-a?include_secret=false",
		providersPath + "/provider-a/scan-models",
		providersPath + "/provider-a/scan-models?type=llm",
	}
	for _, path := range allowed {
		if !knownGetPath(path) {
			t.Fatalf("knownGetPath(%q) rejected an allowed query", path)
		}
	}
	for _, path := range []string{
		providersPath,
		providersPath + "?include_secret=true",
		providersPath + "?include_secret=false&unexpected=true",
		llmModelsPath + "?provider_uuid=provider-a",
		llmModelsPath + "?include_secret=false&provider_uuid=../bad",
		providersPath + "/provider-a/scan-models?type=unknown",
		providersPath + "/provider-a/scan-models?unexpected=true",
	} {
		if knownGetPath(path) {
			t.Fatalf("knownGetPath(%q) accepted an unsafe query", path)
		}
	}
}

func TestProviderModelWritePathAllowlist(t *testing.T) {
	allowed := []struct {
		method string
		path   string
	}{
		{http.MethodPost, providersPath},
		{http.MethodPut, providersPath + "/provider-a"},
		{http.MethodDelete, providersPath + "/provider-a"},
		{http.MethodPost, llmModelsPath},
		{http.MethodPut, embeddingModelsPath + "/model-a"},
		{http.MethodDelete, rerankModelsPath + "/model-a"},
		{http.MethodPost, llmModelsPath + "/model-a/test"},
	}
	for _, test := range allowed {
		if !knownWritePath(test.method, test.path) {
			t.Fatalf("knownWritePath(%s, %q) rejected an allowed path", test.method, test.path)
		}
	}
	for _, test := range []struct {
		method string
		path   string
	}{
		{http.MethodPost, providersPath + "/provider-a/scan-models"},
		{http.MethodPost, llmModelsPath + "/model-a/test/extra"},
		{http.MethodPost, llmModelsPath + "/model-a/test?include_secret=false"},
		{http.MethodPut, providersPath + "/provider-a/child"},
	} {
		if knownWritePath(test.method, test.path) {
			t.Fatalf("knownWritePath(%s, %q) accepted an unsafe path", test.method, test.path)
		}
	}
}
