package integration_test

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
)

func TestKnowledgeEvaluationCommandsAndBaseline(t *testing.T) {
	directory := t.TempDir()
	casesPath := filepath.Join(directory, "cases.yaml")
	cases := `top_k: 2
cases:
  - case_id: alpha
    query: alpha
    top_k: 1
    expect:
      id: alpha
      text_contains: install
      metadata: {source: guide}
  - case_id: beta
    query: beta
    expect:
      metadata: {category: faq}
  - case_id: gamma
    query: gamma
    top_k: 1
    expect: {id: gamma}
`
	if err := os.WriteFile(casesPath, []byte(cases), 0o600); err != nil {
		t.Fatal(err)
	}
	phase := "before"
	var queries []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/system/context":
			fmt.Fprint(w, `{"code":0,"data":{"instance_uuid":"instance-a","workspace_uuid":"workspace-a","api_key_id":"key-a","permissions":["resource.view"]}}`)
		case "/api/v1/system/capabilities":
			fmt.Fprint(w, capabilitiesJSON("knowledge_base.retrieve"))
		case "/api/v1/knowledge/bases/kb-a/retrieve":
			var body struct {
				Query    string `json:"query"`
				Settings struct {
					TopK int `json:"top_k"`
				} `json:"retrieval_settings"`
			}
			if r.Method != http.MethodPost || json.NewDecoder(r.Body).Decode(&body) != nil {
				t.Error("invalid retrieve request")
				w.WriteHeader(400)
				return
			}
			queries = append(queries, body.Query)
			wantTopK := 1
			if body.Query == "beta" {
				wantTopK = 2
			}
			if body.Settings.TopK != wantTopK {
				t.Errorf("query=%q top_k=%d", body.Query, body.Settings.TopK)
			}
			if phase == "error" && body.Query == "beta" {
				w.WriteHeader(503)
				fmt.Fprint(w, `{"code":-1,"msg":"backend unavailable"}`)
				return
			}
			switch body.Query {
			case "alpha":
				if phase == "after" {
					fmt.Fprint(w, `{"code":0,"data":{"results":[]}}`)
					return
				}
				fmt.Fprint(w, `{"code":0,"data":{"results":[{"id":"alpha","content":[{"type":"text","text":"install guide"}],"metadata":{"source":"guide"},"score":0}]}}`)
			case "beta":
				fmt.Fprint(w, `{"code":0,"data":{"results":[{"id":"b1","content":[],"metadata":{"category":"other"}},{"id":"b2","content":[],"metadata":{"category":"faq"},"score":null}]}}`)
			case "gamma":
				if phase == "before" {
					fmt.Fprint(w, `{"code":0,"data":{"results":[]}}`)
					return
				}
				fmt.Fprint(w, `{"code":0,"data":{"results":[{"id":"gamma","content":[],"metadata":{}}]}}`)
			default:
				t.Errorf("unexpected query=%q", body.Query)
				w.WriteHeader(400)
			}
		default:
			t.Errorf("unexpected request %s", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	configPath := filepath.Join(directory, "config.yaml")
	configureWriteContext(t, configPath, server.URL, "FEATURE_KEY")
	env := map[string]string{"FEATURE_KEY": "connection-secret"}
	before := run(t, configPath, env, "", "knowledge-base", "evaluate", "kb-a", "--cases", casesPath)
	if before.code != 6 || !strings.Contains(before.out, "evaluation_failed") {
		t.Fatalf("before=%s", before.out)
	}
	baselinePath := filepath.Join(directory, "before.json")
	if err := os.WriteFile(baselinePath, []byte(before.out), 0o600); err != nil {
		t.Fatal(err)
	}
	data := before.data["data"].(map[string]any)
	rows := data["cases"].([]any)
	alpha, beta, gamma := rows[0].(map[string]any), rows[1].(map[string]any), rows[2].(map[string]any)
	if alpha["score_missing_count"] != float64(0) || beta["matched_rank"] != float64(2) || beta["score_missing_count"] != float64(2) || gamma["empty_result"] != true || gamma["score_missing_count"] != float64(0) {
		t.Fatal(before.out)
	}
	phase = "after"
	after := run(t, configPath, env, "", "knowledge-base", "evaluate", "kb-a", "--cases", casesPath, "--baseline", baselinePath)
	if after.code != 6 {
		t.Fatal(after.out)
	}
	comparison := after.data["data"].(map[string]any)["comparison"].(map[string]any)
	if fmt.Sprint(comparison["improved"]) != "[gamma]" || fmt.Sprint(comparison["regressed"]) != "[alpha]" || comparison["hit_rate_delta"] != float64(0) {
		t.Fatal(after.out)
	}
	count := len(queries)
	wrongTarget := run(t, configPath, env, "", "knowledge-base", "evaluate", "kb-b", "--cases", casesPath, "--baseline", baselinePath)
	if wrongTarget.code != 2 || len(queries) != count {
		t.Fatalf("target mismatch submitted requests: %s", wrongTarget.out)
	}
	if err := os.WriteFile(casesPath, []byte(strings.Replace(cases, "text_contains: install", "text_contains: changed", 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	wrongRules := run(t, configPath, env, "", "knowledge-base", "evaluate", "kb-a", "--cases", casesPath, "--baseline", baselinePath)
	if wrongRules.code != 2 || len(queries) != count {
		t.Fatalf("rule mismatch submitted requests: %s", wrongRules.out)
	}
	if err := os.WriteFile(casesPath, []byte(cases), 0o600); err != nil {
		t.Fatal(err)
	}
	phase = "error"
	errors := run(t, configPath, env, "", "knowledge-base", "evaluate", "kb-a", "--cases", casesPath)
	if errors.code == 0 || len(queries) != count+3 {
		t.Fatalf("service errors retried or lost results: %s", errors.out)
	}
	errorRows := errors.data["data"].(map[string]any)["cases"].([]any)
	if errorRows[0].(map[string]any)["status"] != "passed" || errorRows[1].(map[string]any)["status"] != "error" || errorRows[2].(map[string]any)["status"] != "passed" {
		t.Fatal(errors.out)
	}
}

func TestKnowledgeEvaluationCancellationKeepsCompletedAndUnstartedCases(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/system/context":
			fmt.Fprint(w, `{"code":0,"data":{"instance_uuid":"instance-a","workspace_uuid":"workspace-a","api_key_id":"key-a","permissions":["resource.view"]}}`)
		case "/api/v1/system/capabilities":
			fmt.Fprint(w, capabilitiesJSON("knowledge_base.retrieve"))
		default:
			calls++
			if calls == 2 {
				cancel()
				return
			}
			fmt.Fprint(w, `{"code":0,"data":{"results":[{"id":"hit","content":[],"metadata":{}}]}}`)
		}
	}))
	defer server.Close()
	directory := t.TempDir()
	casesPath := filepath.Join(directory, "cases.json")
	if err := os.WriteFile(casesPath, []byte(`{"cases":[{"case_id":"one","query":"one","expect":{"id":"hit"}},{"case_id":"two","query":"two","expect":{"id":"hit"}},{"case_id":"three","query":"three","expect":{"id":"hit"}}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(directory, "config.yaml")
	configureWriteContext(t, configPath, server.URL, "FEATURE_KEY")
	env := map[string]string{"FEATURE_KEY": "connection-secret"}
	code, out, _, err := invokeRawLookupContext(ctx, configPath, env, func(name string) (string, bool) { value, ok := env[name]; return value, ok }, strings.NewReader(""), "-o", "json", "knowledge-base", "evaluate", "kb-a", "--cases", casesPath)
	if err != nil || code == 0 || calls != 2 {
		t.Fatalf("code=%d calls=%d error=%v output=%s", code, calls, err, out)
	}
	var envelope struct {
		Data struct {
			Cases []struct {
				Status string `json:"status"`
			} `json:"cases"`
		} `json:"data"`
	}
	if json.Unmarshal([]byte(out), &envelope) != nil || len(envelope.Data.Cases) != 3 {
		t.Fatal(out)
	}
	if envelope.Data.Cases[0].Status != "passed" || envelope.Data.Cases[1].Status != "cancelled" || envelope.Data.Cases[2].Status != "not_started" {
		t.Fatal(out)
	}
}

func TestRAGDiscoveryCommands(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	permissions := []string{"resource.view"}
	supported, missing := true, false
	var requests []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("unexpected mutation %s", r.Method)
			w.WriteHeader(405)
			return
		}
		switch r.URL.Path {
		case "/api/v1/system/context":
			p, _ := json.Marshal(permissions)
			fmt.Fprintf(w, `{"code":0,"data":{"instance_uuid":"instance-a","workspace_uuid":"workspace-a","api_key_id":"key-a","permissions":%s}}`, p)
		case "/api/v1/system/capabilities":
			if supported {
				fmt.Fprint(w, capabilitiesJSON("knowledge_engine.list", "knowledge_engine.creation_schema", "knowledge_engine.retrieval_schema", "knowledge_parser.list"))
			} else {
				fmt.Fprint(w, capabilitiesJSON())
			}
		default:
			requests = append(requests, r.URL.RequestURI())
			if missing {
				writeCLIError(w, http.StatusNotFound)
				return
			}
			fmt.Fprint(w, `{"code":0,"data":{"items":[],"text":"connection-secret"}}`)
		}
	}))
	defer server.Close()
	configureWriteContext(t, configPath, server.URL, "FEATURE_KEY")
	env := map[string]string{"FEATURE_KEY": "connection-secret"}
	for _, test := range []struct {
		args []string
		path string
	}{{[]string{"knowledge-engine", "list"}, "/api/v1/knowledge/engines"},
		{[]string{"knowledge-engine", "creation-schema", "author/engine"}, "/api/v1/knowledge/engines/author/engine/creation-schema"},
		{[]string{"knowledge-engine", "retrieval-schema", "author/engine"}, "/api/v1/knowledge/engines/author/engine/retrieval-schema"},
		{[]string{"knowledge-parser", "list", "--mime-type", "text/plain"}, "/api/v1/knowledge/parsers?mime_type=text%2Fplain"}} {
		got := run(t, configPath, env, "", test.args...)
		requireSuccess(t, got)
		if strings.Contains(got.out, "connection-secret") {
			t.Fatalf("secret leaked: %s", got.out)
		}
		if requests[len(requests)-1] != test.path {
			t.Fatalf("request=%s want=%s", requests[len(requests)-1], test.path)
		}
	}
	missing = true
	if got := run(t, configPath, env, "", "knowledge-engine", "list"); got.code != 5 {
		t.Fatalf("not found: %s", got.out)
	}
	missing = false
	permissions = []string{}
	before := len(requests)
	if got := run(t, configPath, env, "", "knowledge-engine", "list"); got.code != 4 || len(requests) != before {
		t.Fatalf("permission: %s", got.out)
	}
	permissions = []string{"resource.view"}
	supported = false
	if got := run(t, configPath, env, "", "knowledge-engine", "list"); got.code != 6 || len(requests) != before {
		t.Fatalf("capability: %s", got.out)
	}

}
