package integration_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func TestBotWriteCommandsDryRunConfirmAndReadBack(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	const key = "bot-write-key"
	botExists := false
	var writes []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-API-Key") != key {
			w.WriteHeader(http.StatusUnauthorized)
			fmt.Fprint(w, `{"code":401,"data":null}`)
			return
		}
		switch r.URL.Path {
		case "/api/v1/system/context":
			fmt.Fprint(w, `{"code":0,"data":{"instance_uuid":"instance-a","workspace_uuid":"workspace-a","api_key_id":"key-a","permissions":["resource.manage","resource.view"]}}`)
		case "/api/v1/system/capabilities":
			fmt.Fprint(w, `{"code":0,"data":{"schema_version":1,"operations":{"bot.get":{"supported":true},"bot.create":{"supported":true},"bot.update":{"supported":true},"bot.delete":{"supported":true}}}}`)
		case "/api/v1/platform/bots":
			writes = append(writes, r.Method+" "+r.URL.Path)
			botExists = true
			fmt.Fprint(w, `{"code":0,"data":{"uuid":"bot-a"}}`)
		case "/api/v1/platform/bots/bot-a":
			switch r.Method {
			case http.MethodGet:
				if !botExists {
					w.WriteHeader(http.StatusNotFound)
					fmt.Fprint(w, `{"code":-1,"data":null}`)
					return
				}
				fmt.Fprint(w, `{"code":0,"data":{"bot":{"uuid":"bot-a","name":"Bot A","adapter":"http_bot","adapter_config":{"token":"bot-secret"}}}}`)
			case http.MethodPut:
				writes = append(writes, r.Method+" "+r.URL.Path)
				fmt.Fprint(w, `{"code":0,"data":null}`)
			case http.MethodDelete:
				writes = append(writes, r.Method+" "+r.URL.Path)
				botExists = false
				fmt.Fprint(w, `{"code":0,"data":null}`)
			}
		default:
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprint(w, `{"code":-1,"data":null}`)
		}
	}))
	defer server.Close()

	configureWriteContext(t, configPath, server.URL, "BOT_KEY")
	bodyPath := filepath.Join(t.TempDir(), "bot.yaml")
	if err := os.WriteFile(bodyPath, []byte("name: Bot A\ndescription: test\nadapter: http_bot\nadapter_config:\n  token: bot-secret\nenable: false\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	env := map[string]string{"BOT_KEY": key}
	dryRun := run(t, configPath, env, "", "bot", "create", "--file", bodyPath, "--dry-run")
	requireSuccess(t, dryRun)
	if len(writes) != 0 || dryRun.data["data"].(map[string]any)["server_write"] != false {
		t.Fatalf("dry-run wrote data: writes=%v output=%s", writes, dryRun.out)
	}
	created := run(t, configPath, env, "", "bot", "create", "--file", bodyPath)
	requireSuccess(t, created)
	if data := created.data["data"].(map[string]any); data["uuid"] != "bot-a" || data["verified"] != true {
		t.Fatalf("create did not read back: %s", created.out)
	}
	if strings.Contains(created.out, "bot-secret") {
		t.Fatalf("create output exposed resource secret: %s", created.out)
	}
	updated := run(t, configPath, env, "", "bot", "update", "bot-a", "--file", bodyPath)
	requireSuccess(t, updated)
	withoutYes := run(t, configPath, env, "", "bot", "delete", "bot-a")
	if withoutYes.code != 2 || len(writes) != 2 {
		t.Fatalf("delete without confirmation was not stopped: writes=%v output=%s", writes, withoutYes.out)
	}
	deleted := run(t, configPath, env, "", "bot", "delete", "bot-a", "--yes")
	requireSuccess(t, deleted)
	if got := strings.Join(writes, "|"); got != "POST /api/v1/platform/bots|PUT /api/v1/platform/bots/bot-a|DELETE /api/v1/platform/bots/bot-a" {
		t.Fatalf("bot writes = %s", got)
	}
}

func TestPipelineApplyCreateUpdateAndPartialResult(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	const key = "pipeline-write-key"
	var writes []string
	failNextUpdate := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/system/context":
			fmt.Fprint(w, `{"code":0,"data":{"instance_uuid":"instance-a","workspace_uuid":"workspace-a","api_key_id":"key-a","permissions":["resource.manage","resource.view"]}}`)
		case "/api/v1/system/capabilities":
			fmt.Fprint(w, `{"code":0,"data":{"schema_version":1,"operations":{"pipeline.get":{"supported":true},"pipeline.create":{"supported":true},"pipeline.update":{"supported":true}}}}`)
		case "/api/v1/pipelines":
			writes = append(writes, r.Method+" "+r.URL.Path+" "+readJSONName(t, r))
			fmt.Fprint(w, `{"code":0,"data":{"uuid":"pipeline-a"}}`)
		case "/api/v1/pipelines/pipeline-a":
			if r.Method == http.MethodPut {
				writes = append(writes, r.Method+" "+r.URL.Path+" "+readJSONName(t, r))
				if failNextUpdate {
					failNextUpdate = false
					w.WriteHeader(http.StatusBadRequest)
					fmt.Fprint(w, `{"code":-1,"data":null}`)
					return
				}
				fmt.Fprint(w, `{"code":0,"data":null}`)
				return
			}
			fmt.Fprint(w, `{"code":0,"data":{"pipeline":{"uuid":"pipeline-a","name":"Pipeline A","config":{"token":"pipeline-secret"}}}}`)
		default:
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprint(w, `{"code":-1,"data":null}`)
		}
	}))
	defer server.Close()

	configureWriteContext(t, configPath, server.URL, "PIPELINE_KEY")
	env := map[string]string{"PIPELINE_KEY": key}
	createPath := filepath.Join(t.TempDir(), "pipeline.yaml")
	if err := os.WriteFile(createPath, []byte("name: Pipeline A\ndescription: test\nconfig:\n  token: pipeline-secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	created := run(t, configPath, env, "", "pipeline", "apply", "--file", createPath)
	requireSuccess(t, created)
	if strings.Contains(created.out, "pipeline-secret") {
		t.Fatalf("apply output exposed resource secret: %s", created.out)
	}
	updatePath := filepath.Join(t.TempDir(), "pipeline-update.json")
	if err := os.WriteFile(updatePath, []byte(`{"uuid":"pipeline-a","name":"Pipeline A","config":{"token":"pipeline-secret"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	updated := run(t, configPath, env, "", "pipeline", "apply", "--file", updatePath)
	requireSuccess(t, updated)
	if got := strings.Join(writes, "|"); got != "POST /api/v1/pipelines Pipeline A|PUT /api/v1/pipelines/pipeline-a Pipeline A|PUT /api/v1/pipelines/pipeline-a Pipeline A" {
		t.Fatalf("pipeline writes = %s", got)
	}

	writes = nil
	failNextUpdate = true
	partial := run(t, configPath, env, "", "pipeline", "apply", "--file", createPath)
	if partial.code == 0 || partial.data["error"].(map[string]any)["type"] != "partial_write" {
		t.Fatalf("partial apply was not reported: %s", partial.out)
	}
	if data := partial.data["data"].(map[string]any); data["uuid"] != "pipeline-a" || data["phase"] != "created" {
		t.Fatalf("partial apply lost recovery identity: %s", partial.out)
	}
}

func TestWriteBodyAndAPIKeyCannotShareStdin(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	result := run(t, configPath, nil, "secret", "--api-key-stdin", "pipeline", "apply", "--file", "-")
	if result.code != 2 || result.data["error"].(map[string]any)["type"] != "input" {
		t.Fatalf("shared stdin was not rejected: %s", result.out)
	}
}

func TestKnowledgeBaseIngestAndTaskGetCommands(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	filePath := filepath.Join(t.TempDir(), "document.txt")
	if err := os.WriteFile(filePath, []byte("document"), 0o600); err != nil {
		t.Fatal(err)
	}
	var uploads, submissions int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/system/context":
			fmt.Fprint(w, `{"code":0,"data":{"instance_uuid":"instance-a","workspace_uuid":"workspace-a","api_key_id":"key-a","permissions":["resource.manage","resource.view"]}}`)
		case "/api/v1/system/capabilities":
			fmt.Fprint(w, `{"code":0,"data":{"schema_version":1,"operations":{"knowledge_base.file.store":{"supported":true},"knowledge_base.get":{"supported":true},"file.document.upload":{"supported":true},"task.get":{"supported":true}}}}`)
		case "/api/v1/knowledge/bases/kb-a":
			fmt.Fprint(w, `{"code":0,"data":{"base":{"uuid":"kb-a","name":"KB"}}}`)
		case "/api/v1/files/documents":
			uploads++
			fmt.Fprint(w, `{"code":0,"data":{"file_id":"file-a"}}`)
		case "/api/v1/knowledge/bases/kb-a/files":
			submissions++
			fmt.Fprint(w, `{"code":0,"data":{"task_id":17}}`)
		case "/api/v1/system/tasks/17":
			fmt.Fprint(w, `{"code":0,"data":{"id":17,"task_type":"user","kind":"knowledge-operation","status":"succeeded","error":null,"result":null,"created_at":1.0}}`)
		default:
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprint(w, `{"code":404,"data":null}`)
		}
	}))
	defer server.Close()

	configureWriteContext(t, configPath, server.URL, "KB_KEY")
	env := map[string]string{"KB_KEY": "secret"}
	dryRun := run(t, configPath, env, "", "knowledge-base", "ingest", "kb-a", "--file", filePath, "--dry-run")
	requireSuccess(t, dryRun)
	if data := dryRun.data["data"].(map[string]any); data["server_write"] != false || data["business_validation"] != "not_run" {
		t.Fatalf("dry-run output = %s", dryRun.out)
	}
	if uploads != 0 || submissions != 0 {
		t.Fatalf("dry-run wrote data: upload/submission calls = %d/%d", uploads, submissions)
	}
	invalidFlags := run(t, configPath, env, "", "knowledge-base", "ingest", "kb-a", "--file", filePath, "--dry-run", "--wait")
	if invalidFlags.code != 2 || invalidFlags.data["error"].(map[string]any)["type"] != "input" {
		t.Fatalf("dry-run with wait was not rejected: %s", invalidFlags.out)
	}
	ingested := run(
		t, configPath, env, "", "knowledge-base", "ingest", "kb-a", "--file", filePath,
		"--wait", "--poll-interval", "1ms", "--wait-timeout", "1s",
	)
	requireSuccess(t, ingested)
	if data := ingested.data["data"].(map[string]any); data["step"] != "completed" || data["file_id"] != "file-a" || data["task_id"] != "17" {
		t.Fatalf("ingest output = %s", ingested.out)
	}
	if uploads != 1 || submissions != 1 {
		t.Fatalf("upload/submission calls = %d/%d", uploads, submissions)
	}

	task := run(t, configPath, env, "", "task", "get", "17")
	requireSuccess(t, task)
	taskData := task.data["data"].(map[string]any)["task"].(map[string]any)
	if taskData["id"] != float64(17) || taskData["status"] != "succeeded" {
		t.Fatalf("task output = %s", task.out)
	}
}

func TestExtensionCommandsCoverDryRunInstallAndAsyncTest(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	bodyPath := filepath.Join(t.TempDir(), "request.json")
	if err := os.WriteFile(bodyPath, []byte(`{"owner":"demo","repo":"plugin","release_tag":"v1"}`), 0600); err != nil {
		t.Fatal(err)
	}
	var pluginWrites, skillPreview, skillWrites, mcpWrites atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/system/context":
			fmt.Fprint(w, `{"code":0,"data":{"instance_uuid":"instance-a","workspace_uuid":"workspace-a","api_key_id":"key-a","permissions":["resource.manage","resource.view"]}}`)
		case "/api/v1/system/capabilities":
			fmt.Fprint(w, `{"code":0,"data":{"schema_version":1,"operations":{"plugin.install.github":{"supported":true},"skill.install.github":{"supported":true},"mcp_server.get":{"supported":true},"mcp_server.test":{"supported":true},"task.get":{"supported":true}}}}`)
		case "/api/v1/plugins/install/github":
			pluginWrites.Add(1)
			fmt.Fprint(w, `{"code":0,"data":{"task_id":51}}`)
		case "/api/v1/skills/install/github/preview":
			skillPreview.Add(1)
			fmt.Fprint(w, `{"code":0,"data":{"skills":[{"name":"demo"}]}}`)
		case "/api/v1/skills/install/github":
			skillWrites.Add(1)
			fmt.Fprint(w, `{"code":0,"data":{"skills":[{"name":"demo"}]}}`)
		case "/api/v1/mcp/servers/saved":
			fmt.Fprint(w, `{"code":0,"data":{"server":{"uuid":"saved-id","name":"saved","type":"stdio"}}}`)
		case "/api/v1/mcp/servers/saved/test":
			mcpWrites.Add(1)
			fmt.Fprint(w, `{"code":0,"data":{"task_id":52}}`)
		case "/api/v1/system/tasks/51":
			fmt.Fprint(w, `{"code":0,"data":{"id":51,"task_type":"user","kind":"extension-operation","status":"succeeded","error":null,"result":null,"created_at":1}}`)
		case "/api/v1/system/tasks/52":
			fmt.Fprint(w, `{"code":0,"data":{"id":52,"task_type":"user","kind":"extension-operation","status":"succeeded","error":null,"result":null,"created_at":1}}`)
		default:
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprint(w, `{"code":404,"data":null}`)
		}
	}))
	defer server.Close()
	configureWriteContext(t, configPath, server.URL, "EXT_KEY")
	env := map[string]string{"EXT_KEY": "secret"}

	dryRun := run(t, configPath, env, "", "plugin", "install", "github", "--file", bodyPath, "--dry-run")
	requireSuccess(t, dryRun)
	if pluginWrites.Load() != 0 || dryRun.data["data"].(map[string]any)["server_write"] != false {
		t.Fatalf("plugin dry-run wrote data: %s", dryRun.out)
	}
	installed := run(t, configPath, env, "", "plugin", "install", "github", "--file", bodyPath, "--wait", "--poll-interval", "1ms", "--wait-timeout", "1s")
	requireSuccess(t, installed)
	if pluginWrites.Load() != 1 || installed.data["data"].(map[string]any)["task_id"] != "51" {
		t.Fatalf("plugin install = %s", installed.out)
	}

	skill := run(t, configPath, env, "", "skill", "install", "github", "--file", bodyPath, "--dry-run")
	requireSuccess(t, skill)
	if skillPreview.Load() != 1 || skillWrites.Load() != 0 {
		t.Fatalf("skill dry-run calls = preview:%d install:%d", skillPreview.Load(), skillWrites.Load())
	}

	mcpDryRun := run(t, configPath, env, "", "mcp-server", "test", "saved", "--dry-run")
	requireSuccess(t, mcpDryRun)
	if mcpWrites.Load() != 0 || mcpDryRun.data["data"].(map[string]any)["server"] == nil {
		t.Fatalf("MCP dry-run = %s", mcpDryRun.out)
	}
	mcp := run(t, configPath, env, "", "mcp-server", "test", "saved", "--wait", "--poll-interval", "1ms", "--wait-timeout", "1s")
	requireSuccess(t, mcp)
	if mcpWrites.Load() != 1 || mcp.data["data"].(map[string]any)["task_id"] != "52" {
		t.Fatalf("MCP test = %s", mcp.out)
	}
}

func configureWriteContext(t *testing.T, configPath, endpoint, envName string) {
	t.Helper()
	requireSuccess(t, run(t, configPath, nil, "", "context", "add", "production", "--endpoint", endpoint, "--api-key-env", envName, "--expect-workspace", "workspace-a"))
	requireSuccess(t, run(t, configPath, nil, "", "context", "use", "production"))
}

func readJSONName(t *testing.T, request *http.Request) string {
	t.Helper()
	var body map[string]any
	if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
		t.Fatalf("decode request: %v", err)
	}
	name, _ := body["name"].(string)
	return name
}
