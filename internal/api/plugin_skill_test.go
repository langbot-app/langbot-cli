package api

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/langbot-app/langbot-cli/internal/result"
)

func TestPluginAndSkillReadsUseSafeProjectionsAndValidatedPaths(t *testing.T) {
	client := Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		var body string
		switch request.URL.Path {
		case pluginsPath:
			body = `{"code":0,"data":{"plugins":[{"manifest":{"manifest":{"metadata":{"author":"a","name":"p","version":"1"}}},"plugin_config":{"token":"secret"}}]}}`
		case pluginsPath + "/a/p/config":
			body = `{"code":0,"data":{"config":{"token":"secret","timeout":3}}}`
		case skillsPath:
			body = `{"code":0,"data":{"skills":[{"name":"demo","description":"secret","revision":"r1","private_key":"secret"}]}}`
		case skillsPath + "/demo":
			body = `{"code":0,"data":{"skill":{"name":"demo","revision":"r1","instructions":"Use it"}}}`
		case skillsPath + "/demo/files":
			body = `{"code":0,"data":{"entries":[{"path":"README.md"}]}}`
		default:
			return nil, nil
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
	})}
	target := Target{Endpoint: "http://example.test", APIKey: "secret"}
	plugins, err := client.Plugins(context.Background(), target)
	if err != nil || len(plugins) != 1 || plugins[0]["author"] != "a" || plugins[0]["name"] != "p" {
		t.Fatalf("Plugins() = %#v, error = %v", plugins, err)
	}
	if _, leaked := plugins[0]["plugin_config"]; leaked {
		t.Fatal("Plugins() exposed plugin config")
	}
	config, err := client.PluginConfig(context.Background(), target, "a", "p")
	if err != nil || config.(map[string]any)["config"].(map[string]any)["token"] != "***" {
		t.Fatalf("PluginConfig() = %#v, error = %v", config, err)
	}
	skills, err := client.Skills(context.Background(), target)
	if err != nil || len(skills) != 1 || skills[0]["name"] != "demo" || skills[0]["revision"] != "r1" {
		t.Fatalf("Skills() = %#v, error = %v", skills, err)
	}
	if _, leaked := skills[0]["private_key"]; leaked {
		t.Fatal("Skills() exposed unknown sensitive field")
	}
	if skill, err := client.SkillGet(context.Background(), target, "demo"); err != nil || skill["revision"] != "r1" {
		t.Fatalf("SkillGet() = %#v, error = %v", skill, err)
	}
	if _, err := client.SkillFiles(context.Background(), target, "demo", "../", false); err == nil {
		t.Fatal("SkillFiles() accepted traversal path")
	}
	if _, err := client.SkillFiles(context.Background(), target, "demo", ".", false); err != nil {
		t.Fatalf("SkillFiles() root path error = %v", err)
	}
}

func TestPluginAndSkillWritesUseOnlyDeclaredPathsAndVerifyFileContentInternally(t *testing.T) {
	var requests []string
	client := Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		key := request.Method + " " + request.URL.Path
		requests = append(requests, key)
		var body string
		switch key {
		case "PUT /api/v1/plugins/a/p/config":
			body = `{"code":0,"data":{}}`
		case "DELETE /api/v1/plugins/a/p":
			if request.URL.Query().Get("delete_data") != "true" {
				t.Fatal("PluginDelete() did not preserve delete_data=true")
			}
			body = `{"code":0,"data":{"task_id":9}}`
		case "POST /api/v1/skills":
			body = `{"code":0,"data":{"skill":{"name":"demo","instructions":"created","revision":"r1"}}}`
		case "PUT /api/v1/skills/demo":
			body = `{"code":0,"data":{"skill":{"name":"demo","description":"updated","revision":"r2"}}}`
		case "DELETE /api/v1/skills/demo":
			body = `{"code":0,"data":null}`
		case "PUT /api/v1/skills/demo/files/SKILL.md":
			body = `{"code":0,"data":{"skill":"demo","path":"SKILL.md","bytes_written":23}}`
		case "GET /api/v1/skills/demo/files/SKILL.md":
			body = `{"code":0,"data":{"skill":"demo","path":"SKILL.md","content":"token=connection-secret","revision":"r3"}}`
		default:
			return nil, fmt.Errorf("unexpected request: %s", key)
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
	})}
	target := Target{Endpoint: "http://example.test", APIKey: "connection-secret"}
	if err := client.PluginConfigUpdate(context.Background(), target, "a", "p", map[string]any{"token": "***"}); err != nil {
		t.Fatalf("PluginConfigUpdate() error = %v", err)
	}
	taskID, err := client.PluginDelete(context.Background(), target, "a", "p", true)
	if err != nil || taskID != "9" {
		t.Fatalf("PluginDelete() task = %q, error = %v", taskID, err)
	}
	if skill, err := client.SkillCreate(context.Background(), target, map[string]any{"name": "demo"}); err != nil || skill["name"] != "demo" || skill["revision"] != "r1" {
		t.Fatalf("SkillCreate() = %#v, error = %v", skill, err)
	}
	if skill, err := client.SkillUpdate(context.Background(), target, "demo", map[string]any{"description": "updated"}); err != nil || skill["name"] != "demo" || skill["revision"] != "r2" {
		t.Fatalf("SkillUpdate() = %#v, error = %v", skill, err)
	}
	if _, err := client.SkillFileWrite(context.Background(), target, "demo", "SKILL.md", "token=connection-secret"); err != nil {
		t.Fatalf("SkillFileWrite() error = %v", err)
	}
	file, matches, err := client.SkillFileMatches(context.Background(), target, "demo", "SKILL.md", "token=connection-secret")
	if err != nil || !matches || file.(map[string]any)["content"] != "token=***" || file.(map[string]any)["revision"] != "r3" {
		t.Fatalf("SkillFileMatches() = %#v, %v, error = %v", file, matches, err)
	}
	if err := client.SkillDelete(context.Background(), target, "demo"); err != nil {
		t.Fatalf("SkillDelete() error = %v", err)
	}
	if _, err := client.write(context.Background(), target, http.MethodDelete, pluginsPath+"/a/p?delete_data=false", nil); err == nil {
		t.Fatal("write() accepted an undeclared Plugin delete query")
	}
	if _, err := client.SkillFileWrite(context.Background(), target, "demo", "%2e%2e/secret", "x"); err == nil {
		t.Fatal("SkillFileWrite() accepted encoded traversal")
	}
	if len(requests) != 7 {
		t.Fatalf("requests = %#v", requests)
	}
}

func TestSkillPackageUpdateRejectsAmbiguousSuccessAndPreservesConflicts(t *testing.T) {
	for _, scenario := range []struct {
		name     string
		preview  bool
		status   int
		body     string
		wantType string
	}{
		{"invalid-preview", true, 200, `{"code":0,"data":{"skill_name":"other"}}`, ""},
		{"invalid-publish", false, 200, `{"code":0,"data":{"skill_name":"other"}}`, "result_unknown"},
		{"revision-mismatch", false, 200, `{"code":0,"data":{"skill_name":"packet","base_revision":"old","proposed_revision":"new","dry_run":false,"changes":{},"skill":{"name":"packet","revision":"other"}}}`, "result_unknown"},
		{"conflict", false, 409, `{"code":-1,"msg":"stale revision"}`, "skill_revision_conflict"},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(scenario.status)
				fmt.Fprint(w, scenario.body)
			}))
			defer server.Close()
			_, err := (Client{}).SkillPackageUpdate(context.Background(), Target{Endpoint: server.URL}, "packet", "old", "packet.zip", strings.NewReader("archive"), scenario.preview)
			if err == nil || calls != 1 {
				t.Fatalf("error=%v calls=%d", err, calls)
			}
			if scenario.wantType != "" && result.AsError(err).Type != scenario.wantType {
				t.Fatalf("error=%#v", result.AsError(err))
			}
			if scenario.preview && result.AsError(err).Type == "result_unknown" {
				t.Fatal("preview error marked as a possible write")
			}
		})
	}
}
