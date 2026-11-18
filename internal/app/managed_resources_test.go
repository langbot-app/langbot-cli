package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/langbot-app/langbot-cli/internal/config"
	"github.com/langbot-app/langbot-cli/internal/result"
)

func TestKnowledgeEvaluationRulesAndTopK(t *testing.T) {
	for _, body := range []string{
		`{"cases":[]}`, `{"cases":[{"case_id":"x","query":"x","expect":{}}]}`,
		`{"cases":[{"case_id":"x","query":" ","expect":{"id":"id"}}]}`,
		`{"top_k":0,"cases":[{"case_id":"x","query":"x","expect":{"id":"id"}}]}`,
		`{"cases":[{"case_id":"x","query":"x","top_k":-1,"expect":{"id":"id"}}]}`,
		`{"cases":[{"case_id":"x","query":"x","expect":{"unknown":"id"}}]}`,
		`{"cases":[{"case_id":"x","query":"x","expect":{"id":"id"}},{"case_id":" x ","query":"y","expect":{"id":"id"}}]}`,
	} {
		var input map[string]any
		if err := json.Unmarshal([]byte(body), &input); err != nil {
			t.Fatal(err)
		}
		if _, err := parseKnowledgeEvaluationCases(input); err == nil {
			t.Errorf("accepted invalid cases %s", body)
		}
	}
	topK := 1
	row := knowledgeEvaluationRow{knowledgeEvaluationCase: knowledgeEvaluationCase{TopK: &topK, Expect: knowledgeExpectation{ID: "second"}}}
	data := map[string]any{"results": []any{
		map[string]any{"id": "first", "score": 0}, map[string]any{"id": "second"},
	}}
	if err := evaluateKnowledgeResults(&row, data); err != nil || row.Status != "failed" || row.ResultCount != 1 || row.ScoreMissingCount != 0 {
		t.Fatalf("row=%+v error=%v", row, err)
	}
	row.Expect = knowledgeExpectation{TextContains: "match", Metadata: map[string]any{"source": "guide"}}
	if matchesKnowledgeExpectation(row.Expect, map[string]any{"content": []any{map[string]any{"type": "image_url", "text": "match"}}, "metadata": map[string]any{"source": "guide"}}) {
		t.Fatal("matched a non-text content element")
	}
	if matchesKnowledgeExpectation(row.Expect, map[string]any{"content": []any{map[string]any{"type": "text", "text": "match"}}, "metadata": map[string]any{"source": "other"}}) {
		t.Fatal("ignored metadata assertion")
	}
	if !knowledgeMetadataMatches(map[string]any{"page": json.Number("1")}, map[string]any{"page": json.Number("1.0")}) {
		t.Fatal("equivalent JSON numbers did not match")
	}
	if knowledgeJSONEqual(json.Number("9007199254740992"), json.Number("9007199254740993")) {
		t.Fatal("large numeric metadata lost precision")
	}
	one, two := 1, 2
	cases := []knowledgeEvaluationCase{{CaseID: "a", Query: "a", TopK: &one}, {CaseID: "b", Query: "b", TopK: &two}}
	if knowledgeCasesDigest(cases) != knowledgeCasesDigest([]knowledgeEvaluationCase{cases[1], cases[0]}) {
		t.Fatal("case ordering changed rule digest")
	}
	changed := append([]knowledgeEvaluationCase(nil), cases...)
	changed[0].TopK = &two
	if knowledgeCasesDigest(cases) == knowledgeCasesDigest(changed) {
		t.Fatal("top_k change retained rule digest")
	}
}

func TestKnowledgeEvaluationBaselineRulesAndPartialComparisons(t *testing.T) {
	one := 1
	current := knowledgeEvaluationReport{
		Target: knowledgeEvaluationTarget{InstanceUUID: "instance", WorkspaceUUID: "workspace", KnowledgeBaseID: "kb"},
		Cases: []knowledgeEvaluationRow{
			{knowledgeEvaluationCase: knowledgeEvaluationCase{CaseID: "private", Query: "query", TopK: &one, Expect: knowledgeExpectation{Metadata: map[string]any{"api_key": "private-value"}}}, Status: "passed"},
			{knowledgeEvaluationCase: knowledgeEvaluationCase{CaseID: "other", Query: "other", TopK: &one, Expect: knowledgeExpectation{ID: "hit"}}, Status: "failed"},
		},
	}
	current.CasesDigest = knowledgeCasesDigest([]knowledgeEvaluationCase{current.Cases[0].knowledgeEvaluationCase, current.Cases[1].knowledgeEvaluationCase})
	current.Summary = knowledgeEvaluationCounts(current.Cases)
	baseline := current
	baseline.Cases = append([]knowledgeEvaluationRow(nil), current.Cases...)
	// A saved JSON report contains the shared output redactor's representation.
	baseline.Cases[0].knowledgeEvaluationCase = knowledgeEvaluationCase{}
	if err := json.Unmarshal(visibleKnowledgeRule(current.Cases[0].knowledgeEvaluationCase), &baseline.Cases[0].knowledgeEvaluationCase); err != nil {
		t.Fatal(err)
	}
	baseline.Cases[0].Status = "error"
	baseline.Cases[0], baseline.Cases[1] = baseline.Cases[1], baseline.Cases[0]
	if err := validateKnowledgeBaseline(&baseline, current); err != nil {
		t.Fatalf("reordered/redacted baseline rejected: %v", err)
	}
	comparison := compareKnowledgeEvaluation(&baseline, current)
	if len(comparison.NotComparable) != 1 || comparison.NotComparable[0] != "private" {
		t.Fatalf("comparison=%+v", comparison)
	}
	baseline.Cases[0].Query = "edited baseline query"
	if err := validateKnowledgeBaseline(&baseline, current); err == nil {
		t.Fatal("accepted edited baseline rules")
	}
	baseline = current
	baseline.Target.WorkspaceUUID = "other-workspace"
	if err := validateKnowledgeBaseline(&baseline, current); err == nil {
		t.Fatal("accepted another Workspace")
	}
	baseline = current
	baseline.CasesDigest = "changed rules"
	if err := validateKnowledgeBaseline(&baseline, current); err == nil {
		t.Fatal("accepted another input digest")
	}
}

func TestKnowledgeEvaluationMemberIdentityCatalogAndBearer(t *testing.T) {
	for _, hasInstance := range []bool{true, false} {
		t.Run(fmt.Sprintf("instance_%t", hasInstance), func(t *testing.T) {
			instance := "instance-a"
			if !hasInstance {
				instance = ""
			}
			identities, catalogs, retrieves := 0, 0, 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
				if request.Header.Get("Authorization") != "Bearer evaluation-member-token" || request.Header.Get("X-API-Key") != "" {
					t.Errorf("member credential missing at %s", request.URL.Path)
				}
				switch request.URL.Path {
				case "/api/v1/workspaces/current":
					identities++
					workspace := map[string]any{"uuid": "workspace-a"}
					if instance != "" {
						workspace["instance_uuid"] = instance
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{"workspace": workspace, "permissions": []string{"resource.view"}}})
				case "/api/v1/operations":
					catalogs++
					fmt.Fprint(w, `{"operations":[{"id":"knowledge.base.retrieve"}]}`)
				case "/api/v1/knowledge/bases/kb-a/retrieve":
					retrieves++
					fmt.Fprint(w, `{"code":0,"data":{"results":[{"id":"chunk-a","content":[{"type":"text","text":"guide"}],"score":1}]}}`)
				default:
					t.Errorf("unexpected member request %s", request.URL.Path)
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer server.Close()
			directory := t.TempDir()
			service := New(Dependencies{
				LookupEnv:         func(name string) (string, bool) { return "evaluation-member-token", name == "LANGBOT_USER_TOKEN" },
				DefaultConfigPath: func() (string, error) { return filepath.Join(directory, "config.yaml"), nil },
			})
			if _, err := service.ContextAdd("production", server.URL, "KEY", "workspace-a", ""); err != nil {
				t.Fatal(err)
			}
			input := map[string]any{"top_k": 1, "cases": []any{map[string]any{"case_id": "guide", "query": "guide", "expect": map[string]any{"id": "chunk-a"}}}}
			ctx := config.AsMember(context.Background())
			options := CheckOptions{ContextSet: true, Context: "production"}
			value, err := service.KnowledgeBaseEvaluate(ctx, "kb-a", input, "", options)
			if err != nil || retrieves != 1 || identities != 1 || catalogs != 1 {
				t.Fatalf("member evaluation err=%v identity/catalog/retrieve=%d/%d/%d", err, identities, catalogs, retrieves)
			}
			report := value.Data.(knowledgeEvaluationReport)
			if report.Target.InstanceUUID != instance || report.Summary.Passed != 1 {
				t.Fatalf("member target/report=%+v", report)
			}
			baseline := filepath.Join(directory, "baseline.json")
			encoded, err := json.Marshal(map[string]any{"data": report})
			if err != nil || strings.Contains(string(encoded), "evaluation-member-token") {
				t.Fatalf("unsafe report encoding: %v", err)
			}
			if err := os.WriteFile(baseline, encoded, 0o600); err != nil {
				t.Fatal(err)
			}
			compared, err := service.KnowledgeBaseEvaluate(ctx, "kb-a", input, baseline, options)
			if !hasInstance {
				if result.AsError(err).Kind != "precondition" || retrieves != 1 || identities != 2 || catalogs != 2 || compared.Data.(knowledgeEvaluationReport).Summary.NotStarted != 1 {
					t.Fatalf("unbound member baseline incurred retrieval: %v %d/%d/%d", err, identities, catalogs, retrieves)
				}
				return
			}
			if err != nil || retrieves != 2 || identities != 2 || catalogs != 2 || compared.Data.(knowledgeEvaluationReport).Comparison == nil {
				t.Fatalf("member baseline failed: %v %d/%d/%d", err, identities, catalogs, retrieves)
			}
			instance = "another-instance"
			if _, err := service.KnowledgeBaseEvaluate(ctx, "kb-a", input, baseline, options); err == nil || retrieves != 2 {
				t.Fatalf("target drift incurred paid retrieval: %v calls=%d", err, retrieves)
			}
		})
	}
}

func TestKnowledgeBaseWritesUsePreflightConfirmationAndReadback(t *testing.T) {
	baseExists := false
	files := []string{"file-a"}
	var writes []string
	service, options, closeServer := managedResourceService(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/knowledge/bases":
			if r.Method == http.MethodPost {
				baseExists = true
				writes = append(writes, r.Method+" "+r.URL.Path)
				fmt.Fprint(w, `{"code":0,"data":{"uuid":"kb-a"}}`)
				return
			}
		case "/api/v1/knowledge/bases/kb-a":
			switch r.Method {
			case http.MethodGet:
				if !baseExists {
					writeNotFound(w)
					return
				}
				fmt.Fprint(w, `{"code":0,"data":{"base":{"uuid":"kb-a","name":"KB","retrieval_settings":{"token":"hidden"}}}}`)
				return
			case http.MethodPut:
				writes = append(writes, r.Method+" "+r.URL.Path)
				fmt.Fprint(w, `{"code":0,"data":{"uuid":"kb-a"}}`)
				return
			case http.MethodDelete:
				writes = append(writes, r.Method+" "+r.URL.Path)
				baseExists = false
				fmt.Fprint(w, `{"code":0,"data":null}`)
				return
			}
		case "/api/v1/knowledge/bases/kb-a/files":
			items := make([]map[string]any, 0, len(files))
			for _, id := range files {
				items = append(items, map[string]any{"uuid": id, "file_name": "doc.txt"})
			}
			writeData(w, map[string]any{"files": items})
			return
		case "/api/v1/knowledge/bases/kb-a/files/file-a":
			writes = append(writes, r.Method+" "+r.URL.Path)
			files = nil
			fmt.Fprint(w, `{"code":0,"data":null}`)
			return
		}
		writeNotFound(w)
	})
	defer closeServer()

	request := map[string]any{"name": "KB", "knowledge_engine_plugin_id": "engine"}
	dryRun, err := service.KnowledgeBaseCreate(context.Background(), request, true, options)
	if err != nil || dryRun.Data.(map[string]any)["server_write"] != false || len(writes) != 0 {
		t.Fatalf("dry-run = %#v, error = %v, writes = %#v", dryRun, err, writes)
	}
	created, err := service.KnowledgeBaseCreate(context.Background(), request, false, options)
	if err != nil || created.Data.(map[string]any)["verified"] != true {
		t.Fatalf("create = %#v, error = %v", created, err)
	}
	if _, err := service.KnowledgeBaseUpdate(context.Background(), "kb-a", map[string]any{"description": "updated"}, false, options); err != nil {
		t.Fatalf("update error = %v", err)
	}
	if _, err := service.KnowledgeBaseFileDelete(context.Background(), "kb-a", "file-a", false, false, options); resultError(err).Kind != "input" {
		t.Fatalf("delete without confirmation error = %+v", resultError(err))
	}
	if _, err := service.KnowledgeBaseFileDelete(context.Background(), "kb-a", "file-a", true, false, options); err != nil {
		t.Fatalf("file delete error = %v", err)
	}
	if _, err := service.KnowledgeBaseDelete(context.Background(), "kb-a", true, false, options); err != nil {
		t.Fatalf("delete error = %v", err)
	}
	if got := strings.Join(writes, "|"); got != "POST /api/v1/knowledge/bases|PUT /api/v1/knowledge/bases/kb-a|DELETE /api/v1/knowledge/bases/kb-a/files/file-a|DELETE /api/v1/knowledge/bases/kb-a" {
		t.Fatalf("writes = %s", got)
	}
}

func TestMCPServerUpdateSupportsRenameAndVerifiesUUID(t *testing.T) {
	serverName := "old"
	const serverUUID = "mcp-a"
	var writes []string
	service, options, closeServer := managedResourceService(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/mcp/servers/old" && r.Method == http.MethodPut {
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			serverName = body["name"].(string)
			writes = append(writes, r.Method+" "+r.URL.Path)
			fmt.Fprint(w, `{"code":0,"data":null}`)
			return
		}
		if r.Method == http.MethodGet && r.URL.Path == "/api/v1/mcp/servers/"+serverName {
			fmt.Fprintf(w, `{"code":0,"data":{"server":{"uuid":%q,"name":%q,"mode":"remote","config":{"token":"***"}}}}`, serverUUID, serverName)
			return
		}
		writeNotFound(w)
	})
	defer closeServer()

	updated, err := service.MCPServerUpdate(context.Background(), "old", map[string]any{"name": "new", "extra_args": map[string]any{"token": "***"}}, false, options)
	if err != nil {
		t.Fatalf("update error = %v", err)
	}
	data := updated.Data.(map[string]any)
	if data["uuid"] != serverUUID || data["server_name"] != "new" || data["verified"] != true {
		t.Fatalf("update = %#v", updated)
	}
	if strings.Join(writes, "|") != "PUT /api/v1/mcp/servers/old" {
		t.Fatalf("writes = %#v", writes)
	}
	if _, err := service.MCPServerLogs(context.Background(), "new", "", 0, options); resultError(err).Kind != "input" {
		t.Fatalf("invalid log limit error = %+v", resultError(err))
	}
}

func managedResourceService(t *testing.T, handler http.HandlerFunc) (*Service, CheckOptions, func()) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/system/context":
			fmt.Fprint(w, `{"code":0,"data":{"instance_uuid":"instance-a","workspace_uuid":"workspace-a","api_key_id":"key-a","permissions":["resource.manage","resource.view","audit.view"]}}`)
		case "/api/v1/system/capabilities":
			operations := map[string]map[string]bool{}
			for _, operation := range []string{
				"knowledge_base.get", "knowledge_base.create", "knowledge_base.update", "knowledge_base.delete", "knowledge_base.file.list", "knowledge_base.file.delete",
				"mcp_server.get", "mcp_server.create", "mcp_server.update", "mcp_server.delete", "mcp_server.logs",
			} {
				operations[operation] = map[string]bool{"supported": true}
			}
			writeData(w, map[string]any{"schema_version": 1, "operations": operations})
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

func writeData(w http.ResponseWriter, data any) {
	_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": data})
}

func writeNotFound(w http.ResponseWriter) {
	w.WriteHeader(http.StatusNotFound)
	fmt.Fprint(w, `{"code":404,"data":null}`)
}
