package api

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestKnowledgeBaseReadMethodsUseContractPathsAndSafeProjection(t *testing.T) {
	var paths []string
	client := Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		paths = append(paths, request.URL.RequestURI())
		var body string
		switch request.URL.Path {
		case knowledgeBasesPath:
			body = `{"code":0,"data":{"bases":[{"uuid":"kb-a","name":"KB","knowledge_engine_plugin_id":"engine","creation_settings":{"password":"secret"}}]}}`
		case knowledgeBasesPath + "/kb-a/files":
			body = `{"code":0,"data":{"files":[{"id":"file-a","name":"doc","value":"secret"}]}}`
		default:
			return nil, nil
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
	})}
	target := Target{Endpoint: "http://example.test", APIKey: "secret"}
	bases, err := client.KnowledgeBases(context.Background(), target)
	if err != nil || len(bases) != 1 || bases[0]["knowledge_engine_plugin_id"] != "engine" {
		t.Fatalf("KnowledgeBases() = %#v, error = %v", bases, err)
	}
	if _, leaked := bases[0]["creation_settings"]; leaked {
		t.Fatal("KnowledgeBases() exposed creation settings")
	}
	files, err := client.KnowledgeBaseFiles(context.Background(), target, "kb-a")
	if err != nil || files[0].(map[string]any)["value"] != "***" {
		t.Fatalf("KnowledgeBaseFiles() = %#v, error = %v", files, err)
	}
	if len(paths) != 2 || paths[1] != knowledgeBasesPath+"/kb-a/files" {
		t.Fatalf("request paths = %#v", paths)
	}
}

func TestKnowledgeBaseWriteMethodsUseOnlyRegisteredPaths(t *testing.T) {
	var requests []string
	client := Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		requests = append(requests, request.Method+" "+request.URL.RequestURI())
		body := `{"code":0,"data":null}`
		if request.Method == http.MethodPost {
			body = `{"code":0,"data":{"uuid":"kb-a"}}`
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
	})}
	target := Target{Endpoint: "http://example.test", APIKey: "secret"}

	if result, err := client.KnowledgeBaseCreate(context.Background(), target, map[string]any{"name": "KB"}); err != nil || result.UUID != "kb-a" {
		t.Fatalf("KnowledgeBaseCreate() = %#v, error = %v", result, err)
	}
	if _, err := client.KnowledgeBaseUpdate(context.Background(), target, "kb-a", map[string]any{"name": "Updated"}); err != nil {
		t.Fatalf("KnowledgeBaseUpdate() error = %v", err)
	}
	if err := client.KnowledgeBaseFileDelete(context.Background(), target, "kb-a", "file-a"); err != nil {
		t.Fatalf("KnowledgeBaseFileDelete() error = %v", err)
	}
	if _, err := client.KnowledgeBaseDelete(context.Background(), target, "kb-a"); err != nil {
		t.Fatalf("KnowledgeBaseDelete() error = %v", err)
	}

	want := []string{
		"POST /api/v1/knowledge/bases",
		"PUT /api/v1/knowledge/bases/kb-a",
		"DELETE /api/v1/knowledge/bases/kb-a/files/file-a",
		"DELETE /api/v1/knowledge/bases/kb-a",
	}
	if strings.Join(requests, "|") != strings.Join(want, "|") {
		t.Fatalf("requests = %#v", requests)
	}
}
