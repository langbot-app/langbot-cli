package command

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type traceFixture struct {
	t        *testing.T
	native   map[string][]map[string]any
	rows     map[monitoringTraceRef]map[string]any
	sections map[monitoringTraceRef]map[string][]map[string]any
	aliases  map[string]monitoringTraceRef
	legacy   map[monitoringTraceRef]bool
	listed   []map[string]any
	hook     func(*http.Request, map[string]any, map[string]any) int
	command  string
	member   bool
	denied   bool
	mu       sync.Mutex
	calls    map[string][]int
	requests atomic.Int32
}

func newTraceFixture(t *testing.T) *traceFixture {
	t.Helper()
	stamp := time.Now().Add(-time.Minute).UTC().Truncate(time.Millisecond)
	ref := monitoringTraceRef{"pipeline", "message-a"}
	input := map[string]any{"id": ref.id, "bot_id": "bot-a", "session_id": "chat-a", "role": "user", "timestamp": stamp.Format(time.RFC3339Nano), "status": "success", "event_id": "event-a", "run_id": "run-a", "content": "PRIVATE-CONVERSATION"}
	delivery := map[string]any{"id": "delivery-a", "bot_id": "bot-a", "session_id": "chat-a", "role": "assistant", "timestamp": stamp.Add(4 * time.Second).Format(time.RFC3339Nano), "status": "error", "parent_message_id": ref.id, "run_id": "run-a", "variables": `{"delivery_error":"platform rejected reply"}`, "content": "PRIVATE-REPLY"}
	llm := map[string]any{"id": "llm-a", "bot_id": "bot-a", "session_id": "chat-a", "message_id": ref.id, "status": "success", "timestamp": stamp.Add(time.Second).Format(time.RFC3339Nano), "model_name": "model-a", "messages": "PRIVATE-PROMPT"}
	tool := map[string]any{"id": "tool-a", "bot_id": "bot-a", "session_id": "chat-a", "message_id": ref.id, "status": "success", "timestamp": stamp.Add(2 * time.Second).Format(time.RFC3339Nano), "tool_name": "search", "parameters": "PRIVATE-ARGS", "result": "PRIVATE-RESULT"}
	row := map[string]any{"source": ref.source, "id": ref.id, "bot_id": "bot-a", "session_id": "chat-a", "event_id": "event-a", "created_at_ms": stamp.UnixMilli(), "finished_at_ms": stamp.Add(3 * time.Second).UnixMilli(), "status": "success"}
	return &traceFixture{t: t, native: map[string][]map[string]any{"messages": {input, delivery}, "llm_calls": {llm}, "tool_calls": {tool}, "errors": {}}, rows: map[monitoringTraceRef]map[string]any{ref: row}, sections: map[monitoringTraceRef]map[string][]map[string]any{ref: {
		"inputs":     {input, {"id": "event-a", "timestamp_ms": stamp.UnixMilli(), "metadata": map[string]any{"routes": []any{map[string]any{"status": "delivered", "target_type": "pipeline", "target_uuid": "pipeline-a"}}}}},
		"outputs":    {{"id": "output-a", "role": "assistant", "origin": "generated", "run_id": "run-a", "timestamp_ms": stamp.Add(3 * time.Second).UnixMilli(), "content": "PRIVATE-GENERATION"}},
		"deliveries": {delivery}, "llm_calls": {llm}, "tool_calls": {tool},
		"events": {{"id": int64(9007199254740993), "sequence": 1, "type": "run.completed", "timestamp_ms": stamp.Add(3 * time.Second).UnixMilli(), "data": map[string]any{"secret": "PRIVATE-EVENT-DATA"}}},
	}}, aliases: map[string]monitoringTraceRef{ref.id: ref}, legacy: map[monitoringTraceRef]bool{}, listed: []map[string]any{row}, calls: map[string][]int{}}
}

func (fixture *traceFixture) server() *httptest.Server {
	fixture.t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		fixture.requests.Add(1)
		command := fixture.command
		if command == "" {
			command = "monitoring.trace"
		}
		credentialOK := request.Header.Get("X-API-Key") == "test-secret"
		if fixture.member {
			credentialOK = request.Header.Get("Authorization") == "Bearer member-private" && request.Header.Get("X-API-Key") == ""
		}
		if !credentialOK || request.Header.Get("X-LangBot-Client-Command") != command {
			fixture.t.Error("trace lost its credential or command identity")
		}
		if fixture.denied && (request.URL.Path == "/api/v1/system/context" || request.URL.Path == "/api/v1/workspaces/current") {
			fmt.Fprint(writer, `{"code":0,"data":{"instance_uuid":"instance","workspace_uuid":"workspace","api_key_id":"key","workspace":{"uuid":"workspace"},"permissions":[]}}`)
			return
		}
		if operationPreflight(writer, request, true) {
			return
		}
		body := map[string]any{}
		var response map[string]any
		key, offset := "", 0
		switch {
		case request.Method == http.MethodGet && request.URL.Path == "/api/v1/operations/monitoring.execution.detail":
			fmt.Fprint(writer, `{"id":"monitoring.execution.detail","risk":"read","permission":"resource.view","streaming":false,"side_effect":false}`)
			return
		case request.Method == http.MethodGet && strings.HasPrefix(request.URL.Path, "/api/v1/monitoring/"):
			kind := strings.ReplaceAll(strings.TrimPrefix(request.URL.Path, "/api/v1/monitoring/"), "-", "_")
			items, exists := fixture.native[kind]
			if !exists || request.URL.Query().Get("botId") != "bot-a" || request.URL.Query().Get("endTime") == "" || request.URL.Query().Get("limit") != "100" {
				fixture.t.Errorf("unexpected native trace query %s", request.URL)
			}
			if (kind == "messages" || kind == "tool_calls") && request.URL.Query().Get("sessionId") != "chat-a" {
				fixture.t.Error("trace omitted native session filter")
			}
			offset, _ = strconv.Atoi(request.URL.Query().Get("offset"))
			response = map[string]any{kind: traceFixturePage(items, offset), "total": len(items), "limit": 100, "offset": offset}
			key = kind
		case request.Method == http.MethodPost && strings.HasPrefix(request.URL.Path, "/api/v1/operations/monitoring."):
			decoder := json.NewDecoder(request.Body)
			decoder.UseNumber()
			if err := decoder.Decode(&body); err != nil {
				fixture.t.Error(err)
			}
			offset, _ = strconv.Atoi(fmt.Sprint(body["offset"]))
			switch request.URL.Path {
			case "/api/v1/operations/monitoring.executions":
				if len(body) != 4 || body["include_summary"] != false || fmt.Sprint(body["bot_ids"]) != "[bot-a]" || fmt.Sprint(body["limit"]) != "100" {
					fixture.t.Errorf("unexpected execution list input %#v", body)
				}
				response = map[string]any{"items": traceFixturePage(fixture.listed, offset), "total": len(fixture.listed), "has_more": offset+100 < len(fixture.listed)}
				key = "executions"
			case "/api/v1/operations/monitoring.execution.detail":
				if len(body) != 5 || fmt.Sprint(body["limit"]) != "100" || body["section"] == "conversation" {
					fixture.t.Errorf("unexpected execution detail input %#v", body)
				}
				ref := monitoringTraceRef{fmt.Sprint(body["source"]), fmt.Sprint(body["execution_id"])}
				if ref.source == "auto" {
					ref = fixture.aliases[ref.id]
				}
				row := fixture.rows[ref]
				if row == nil {
					writer.WriteHeader(http.StatusNotFound)
					fmt.Fprint(writer, `{"code":404,"msg":"execution deleted"}`)
					return
				}
				section := fmt.Sprint(body["section"])
				items := fixture.sections[ref][section]
				response = map[string]any{"source": ref.source, "row": row, "legacy_context": fixture.legacy[ref], "pages": map[string]any{section: map[string]any{"items": traceFixturePage(items, offset), "has_more": offset+100 < len(items), "next_offset": offset + min(100, max(0, len(items)-offset))}}}
				key = ref.source + "/" + ref.id + "/" + section
			default:
				fixture.t.Errorf("trace attempted unexpected operation %s", request.URL)
			}
		default:
			fixture.t.Errorf("trace attempted a write or unexpected endpoint %s %s", request.Method, request.URL)
		}
		fixture.mu.Lock()
		fixture.calls[key] = append(fixture.calls[key], offset)
		fixture.mu.Unlock()
		status := 0
		if fixture.hook != nil {
			status = fixture.hook(request, body, response)
		}
		if status != 0 {
			writer.WriteHeader(status)
			fmt.Fprint(writer, `{"code":503,"msg":"source unavailable"}`)
			return
		}
		json.NewEncoder(writer).Encode(map[string]any{"code": 0, "data": response})
	}))
}

func traceFixturePage(items []map[string]any, offset int) []map[string]any {
	start := min(offset, len(items))
	page := append([]map[string]any{}, items[start:min(start+100, len(items))]...)
	return page
}

func traceCLI(t *testing.T, fixture *traceFixture, format string, args ...string) (int, map[string]any, string) {
	t.Helper()
	server := fixture.server()
	defer server.Close()
	code, output := operationCLIFormatContext(t, context.Background(), server, "", format, nil, append([]string{"monitoring", "trace", "--bot", "bot-a", "--session", "chat-a"}, args...)...)
	if format == "" {
		return code, nil, output
	}
	envelopes := decodeOperationStream(t, output, format)
	if len(envelopes) != 1 {
		t.Fatalf("expected one trace envelope, got %s", output)
	}
	data, ok := envelopes[0]["data"].(map[string]any)
	if !ok {
		t.Fatalf("missing trace data, code=%d output=%s", code, output)
	}
	return code, data, output
}

func traceRecords(t *testing.T, data map[string]any, stage string) []map[string]any {
	t.Helper()
	items := []map[string]any{}
	for _, raw := range data["timeline"].([]any) {
		item := raw.(map[string]any)
		if item["stage"] == stage {
			items = append(items, item)
		}
	}
	return items
}

func traceHasIssue(data map[string]any, reason string) bool {
	items, _ := data["issues"].([]any)
	for _, raw := range items {
		if raw.(map[string]any)["reason"] == reason {
			return true
		}
	}
	return false
}

func TestMonitoringTraceSeparatesGenerationAndDeliveryWithLocators(t *testing.T) {
	for _, format := range []string{"json", "yaml", ""} {
		t.Run("format="+format, func(t *testing.T) {
			fixture := newTraceFixture(t)
			code, data, output := traceCLI(t, fixture, format, "--since", "2h")
			if code != 0 || strings.Contains(output, "PRIVATE-") || strings.Contains(output, "test-secret") {
				t.Fatalf("code=%d output=%s", code, output)
			}
			if format == "" {
				if !strings.Contains(output, "platform rejected reply") || !strings.Contains(output, "delivery-a") || !strings.Contains(output, "output-a") {
					t.Fatalf("human trace omitted failure or timeline %s", output)
				}
				return
			}
			first := data["first_error"].(map[string]any)
			if data["complete"] != true || data["partial"] != false || first["stage"] != "deliveries" || first["id"] != "delivery-a" || first["error"] != "platform rejected reply" || len(traceRecords(t, data, "outputs")) != 1 || len(traceRecords(t, data, "llm_calls")) != 1 || len(traceRecords(t, data, "tool_calls")) != 1 {
				t.Fatalf("trace result %s", output)
			}
			locator := first["locator"].(map[string]any)
			input := locator["input"].(map[string]any)
			if locator["operation"] != "monitoring.execution.detail" || input["execution_id"] != "message-a" || input["section"] != "deliveries" || locator["record_id"] != "delivery-a" {
				t.Fatalf("failure has unusable locator %#v", locator)
			}
			if fmt.Sprint(traceRecords(t, data, "events")[0]["id"]) != "9007199254740993" {
				t.Fatal("numeric monitoring identity lost precision")
			}
		})
	}
}

func TestMonitoringTracePaginatesEverySourceAndDeduplicatesRecords(t *testing.T) {
	fixture := newTraceFixture(t)
	ref := monitoringTraceRef{"pipeline", "message-a"}
	for _, stage := range []string{"inputs", "outputs", "deliveries", "events", "llm_calls", "tool_calls", "errors", "related"} {
		items := []map[string]any{}
		for index := 0; index < 205; index++ {
			id := fmt.Sprintf("%s-%03d", stage, index)
			item := map[string]any{"id": id, "bot_id": "bot-a", "session_id": "chat-a", "status": "success", "timestamp_ms": time.Now().Add(-time.Minute).UnixMilli() + int64(index), "message_id": ref.id}
			if stage == "related" {
				item["source"], item["event_id"], item["created_at_ms"] = "agent", "event-a", item["timestamp_ms"]
				child := monitoringTraceRef{"agent", id}
				fixture.rows[child] = item
				fixture.sections[child] = map[string][]map[string]any{"inputs": {{"id": "event-a", "timestamp_ms": item["timestamp_ms"]}}}
			} else if stage == "outputs" {
				item["role"] = "assistant"
			}
			items = append(items, item)
		}
		fixture.sections[ref][stage] = items
	}
	// Include all records in their native lists as well, proving identity deduplication.
	for _, kind := range []string{"llm_calls", "tool_calls", "errors"} {
		fixture.native[kind] = fixture.sections[ref][kind]
	}
	for _, item := range fixture.sections[ref]["deliveries"] {
		item["role"], item["parent_message_id"] = "assistant", ref.id
	}
	fixture.native["messages"] = fixture.sections[ref]["deliveries"]
	// More than 100 unrelated executions must also be scanned before a matching one.
	fixture.listed = nil
	for index := 0; index < 205; index++ {
		fixture.listed = append(fixture.listed, map[string]any{"source": "event", "id": fmt.Sprint(index), "bot_id": "bot-a", "session_id": "other-chat", "created_at_ms": time.Now().Add(-time.Minute).UnixMilli()})
	}
	fixture.listed = append(fixture.listed, fixture.rows[ref])
	code, data, output := traceCLI(t, fixture, "json", "--timeout", "30s")
	if code != 0 {
		t.Fatalf("code=%d output=%s", code, output)
	}
	for _, key := range []string{"messages", "llm_calls", "tool_calls", "errors", "executions", "pipeline/message-a/inputs", "pipeline/message-a/outputs", "pipeline/message-a/deliveries", "pipeline/message-a/events", "pipeline/message-a/llm_calls", "pipeline/message-a/tool_calls", "pipeline/message-a/errors", "pipeline/message-a/related"} {
		expected := []int{0, 100, 200}
		if key == "pipeline/message-a/inputs" {
			// Calls use Core's auto resolver once to confirm their typed root.
			expected = append(expected, 0)
		}
		if !reflect.DeepEqual(fixture.calls[key], expected) {
			t.Fatalf("pagination %s=%v", key, fixture.calls[key])
		}
	}
	for _, stage := range []string{"outputs", "deliveries", "events", "llm_calls", "tool_calls", "errors"} {
		if count := len(traceRecords(t, data, stage)); count != 205 {
			t.Fatalf("deduplicated %s count=%d", stage, count)
		}
	}
	if len(data["executions"].([]any)) != 206 {
		t.Fatal("related execution pagination lost siblings")
	}
}

func TestMonitoringTraceScopesByBotAndExplicitIdentities(t *testing.T) {
	fixture := newTraceFixture(t)
	ref := monitoringTraceRef{"pipeline", "message-a"}
	for _, item := range []map[string]any{
		{"id": "foreign-bot", "bot_id": "bot-b", "session_id": "chat-a", "role": "user", "status": "error"},
		{"id": "foreign-chat", "bot_id": "bot-a", "session_id": "chat-b", "role": "user", "status": "error"},
	} {
		fixture.native["messages"] = append(fixture.native["messages"], item)
		fixture.sections[ref]["deliveries"] = append(fixture.sections[ref]["deliveries"], item)
	}
	fixture.listed = append(fixture.listed, map[string]any{"source": "agent", "id": "foreign-bot", "bot_id": "bot-b", "session_id": "chat-a", "created_at_ms": time.Now().Add(-time.Minute).UnixMilli()})
	code, data, output := traceCLI(t, fixture, "json")
	if code != 6 || !traceHasIssue(data, "record_scope_unconfirmed") || strings.Contains(output, `"id": "foreign-`) {
		t.Fatalf("code=%d scoped trace=%s", code, output)
	}
	if _, exists := fixture.calls["agent/foreign-bot/inputs"]; exists {
		t.Fatal("trace queried another Bot execution")
	}
}

func TestMonitoringTraceLegacyOrphansKeepIndependentEvidence(t *testing.T) {
	fixture := newTraceFixture(t)
	ref := monitoringTraceRef{"pipeline", "message-a"}
	orphan := monitoringTraceRef{"pipeline", "orphan-reply"}
	fixture.native["messages"] = append(fixture.native["messages"], map[string]any{"id": orphan.id, "bot_id": "bot-a", "session_id": "chat-a", "role": "assistant", "status": "success"})
	fixture.legacy[ref] = true
	fixture.sections[ref]["deliveries"] = nil
	fixture.native["messages"] = append(fixture.native["messages"][:1], fixture.native["messages"][2:]...)
	code, data, output := traceCLI(t, fixture, "json")
	if code != 6 || !traceHasIssue(data, "legacy_context") || !traceHasIssue(data, "delivery_evidence_missing") || !traceHasIssue(data, "unlinked_record") {
		t.Fatalf("code=%d output=%s", code, output)
	}
	items := traceRecords(t, data, "session_context")
	if len(traceRecords(t, data, "deliveries")) != 0 || len(items) != 1 || len(items[0]["executions"].([]any)) != 0 || items[0]["id"] != orphan.id {
		t.Fatalf("unlinked legacy reply was associated by proximity %#v", items)
	}
	if _, queried := fixture.calls["pipeline/"+orphan.id+"/inputs"]; queried {
		t.Fatal("trace treated an unlinked reply as an execution")
	}
}

func TestMonitoringTracePreservesExecutionNamespaces(t *testing.T) {
	fixture := newTraceFixture(t)
	agent := monitoringTraceRef{"agent", "message-a"}
	fixture.rows[agent] = map[string]any{"source": "agent", "id": agent.id, "bot_id": "bot-a", "session_id": "chat-a", "status": "completed", "created_at_ms": time.Now().Add(-time.Minute).UnixMilli()}
	fixture.sections[agent] = map[string][]map[string]any{"inputs": {{"id": "event-a"}}}
	// EventLog and MonitoringMessage are different tables with independent IDs.
	fixture.sections[agent]["inputs"] = append(fixture.sections[agent]["inputs"], map[string]any{"id": "message-a", "event_type": "message.received"})
	fixture.listed = append(fixture.listed, fixture.rows[agent])
	code, data, output := traceCLI(t, fixture, "json")
	if code != 0 || len(data["executions"].([]any)) != 2 || len(traceRecords(t, data, "execution")) != 2 {
		t.Fatalf("code=%d output=%s", code, output)
	}
	sources := map[string]int{}
	for _, input := range traceRecords(t, data, "inputs") {
		if input["id"] == "message-a" {
			sources[fmt.Sprint(input["record_source"])]++
		}
	}
	if !reflect.DeepEqual(sources, map[string]int{"event": 1, "message_view": 1}) {
		t.Fatalf("trace merged IDs from different record sources %#v", sources)
	}
}

func TestMonitoringTraceReportsIncompleteAndChangingSources(t *testing.T) {
	for _, scenario := range []string{"deleted", "missing-section", "bad-cursor", "empty-page", "repeated-page", "native-offset", "source-error", "detail-source-error", "changed-status", "missing-delivery", "list-truncated"} {
		t.Run(scenario, func(t *testing.T) {
			fixture := newTraceFixture(t)
			ref := monitoringTraceRef{"pipeline", "message-a"}
			fixture.hook = func(request *http.Request, body, response map[string]any) int {
				if scenario == "native-offset" && strings.HasSuffix(request.URL.Path, "/messages") {
					response["offset"] = 200
				}
				if scenario == "source-error" && strings.HasSuffix(request.URL.Path, "/tool-calls") {
					return http.StatusServiceUnavailable
				}
				if scenario == "list-truncated" && strings.HasSuffix(request.URL.Path, "/monitoring.executions") {
					response["total"] = 1000
				}
				if body["section"] != "outputs" {
					return 0
				}
				pages := response["pages"].(map[string]any)
				page := pages["outputs"].(map[string]any)
				switch scenario {
				case "deleted":
					return http.StatusNotFound
				case "detail-source-error":
					return http.StatusServiceUnavailable
				case "missing-section":
					delete(pages, "outputs")
				case "bad-cursor":
					page["has_more"], page["next_offset"] = true, 0
				case "empty-page":
					page["items"], page["has_more"], page["next_offset"] = []any{}, true, 100
				case "repeated-page":
					page["items"], page["has_more"], page["next_offset"] = fixture.sections[ref]["outputs"], true, mustTraceInt(body["offset"])+100
				case "changed-status":
					clone := map[string]any{}
					for key, value := range fixture.rows[ref] {
						clone[key] = value
					}
					clone["status"], clone["status_reason"] = "failed", "late failure"
					response["row"] = clone
				}
				return 0
			}
			if scenario == "missing-delivery" {
				fixture.native["messages"] = fixture.native["messages"][:1]
				fixture.sections[ref]["deliveries"] = nil
			}
			code, data, output := traceCLI(t, fixture, "json")
			if code == 0 || data["partial"] != true || data["complete"] != false || len(traceRecords(t, data, "inputs")) == 0 && scenario != "native-offset" {
				t.Fatalf("code=%d output=%s", code, output)
			}
			if scenario == "repeated-page" && len(fixture.calls["pipeline/message-a/outputs"]) != 2 {
				t.Fatal("trace did not stop a repeated section page")
			}
			if (scenario == "source-error" || scenario == "detail-source-error") && code != 10 {
				t.Fatalf("source error exit code lost code=%d output=%s", code, output)
			}
			if scenario == "missing-delivery" && (len(data["errors"].([]any)) != 0 || !traceHasIssue(data, "delivery_evidence_missing")) {
				t.Fatalf("missing evidence was presented as a proved delivery error %s", output)
			}
		})
	}
}

func mustTraceInt(value any) int {
	number, _ := strconv.Atoi(fmt.Sprint(value))
	return number
}

func TestMonitoringTraceDoesNotInventFailureOrdering(t *testing.T) {
	for _, scenario := range []string{"untimed-route", "single-untimed-route", "tied-errors", "failed-execution", "untimed-execution"} {
		t.Run(scenario, func(t *testing.T) {
			fixture := newTraceFixture(t)
			ref := monitoringTraceRef{"pipeline", "message-a"}
			switch scenario {
			case "untimed-route", "single-untimed-route":
				fixture.sections[ref]["inputs"][1]["metadata"] = map[string]any{"routes": []any{map[string]any{"status": "failed", "failure_code": "runner_unavailable"}}}
				if scenario == "single-untimed-route" {
					fixture.native["messages"][1]["status"] = "success"
					delete(fixture.native["messages"][1], "variables")
				}
			case "tied-errors":
				fixture.sections[ref]["errors"] = []map[string]any{{"id": "error-a", "timestamp": fixture.native["messages"][1]["timestamp"], "error_message": "pipeline failed"}}
			case "failed-execution", "untimed-execution":
				fixture.native["messages"] = fixture.native["messages"][:1]
				fixture.sections[ref]["deliveries"] = nil
				fixture.sections[ref]["outputs"] = nil
				fixture.rows[ref]["status"], fixture.rows[ref]["status_reason"] = "failed", "runner crashed"
				if scenario == "untimed-execution" {
					delete(fixture.rows[ref], "finished_at_ms")
				}
			}
			code, data, output := traceCLI(t, fixture, "json")
			if scenario == "failed-execution" {
				first := data["first_error"].(map[string]any)
				if code != 0 || first["stage"] != "execution" || first["error"] != "runner crashed" {
					t.Fatalf("execution failure fallback %s", output)
				}
			} else {
				if code != 6 || data["first_error"] != nil || data["error_ordering"] != "undetermined" || !traceHasIssue(data, "failure_order_unconfirmed") {
					t.Fatalf("unprovable first failure selected code=%d output=%s", code, output)
				}
				if scenario != "tied-errors" && len(data["unordered_errors"].([]any)) != 1 {
					t.Fatalf("missing explicit unordered evidence %s", output)
				}
			}
		})
	}
}

func TestMonitoringTraceValidatesInputsOfflineAndDescribesSchema(t *testing.T) {
	fixture := newTraceFixture(t)
	server := fixture.server()
	defer server.Close()
	for _, arguments := range [][]string{{}, {"--bot", "bot-a"}, {"--bot", "bot-a", "--session", " "}, {"--bot", "bot-a", "--session", "chat-a", "--since", ""}, {"--bot", "bot-a", "--session", "chat-a", "--since", "0"}, {"--bot", "bot-a", "--session", "chat-a", "--since", "-1h"}, {"--bot", "bot-a", "--session", "chat-a", "--since", "9999999999999999999999h"}, {"--bot", "bot-a", "--session", "chat-a", "--since", "yesterday"}} {
		code, output := operationCLI(t, server, "", append([]string{"monitoring", "trace"}, arguments...)...)
		if code != 2 || fixture.requests.Load() != 0 {
			t.Fatalf("code=%d requests=%d output=%s", code, fixture.requests.Load(), output)
		}
	}
	result := executeSchemaForTest(t, "schema", "--command", "monitoring.trace")
	var envelope schemaEnvelope
	if result.code != 0 || json.Unmarshal([]byte(result.output), &envelope) != nil || len(envelope.Data.Commands) != 1 {
		t.Fatalf("trace schema %s", result.output)
	}
	command := envelope.Data.Commands[0]
	if command.Mutating || command.RequiresYes || !hasFlag(command.Flags, "bot", "local", true) || !hasFlag(command.Flags, "session", "local", true) || !strings.Contains(command.Examples, "--since 2h") || !hasFlag(command.Flags, "since", "local", false) {
		t.Fatalf("incorrect trace schema %#v", command)
	}
}

func TestMonitoringTracePreservesPartialDataOnCancellationAndTimeout(t *testing.T) {
	for _, timeout := range []bool{false, true} {
		t.Run(fmt.Sprint("timeout=", timeout), func(t *testing.T) {
			fixture := newTraceFixture(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			fixture.hook = func(request *http.Request, body, response map[string]any) int {
				if body["section"] == "outputs" {
					if !timeout {
						cancel()
					}
					<-request.Context().Done()
				}
				return 0
			}
			server := fixture.server()
			defer server.Close()
			args := []string{"monitoring", "trace", "--bot", "bot-a", "--session", "chat-a"}
			if timeout {
				args = append(args, "--timeout", "150ms")
			}
			code, output := operationCLIContext(t, ctx, server, "", nil, args...)
			data := decodeOperationStream(t, output, "json")[0]["data"].(map[string]any)
			if code == 0 || data["partial"] != true || len(traceRecords(t, data, "inputs")) == 0 || len(fixture.calls["pipeline/message-a/outputs"]) != 1 {
				t.Fatalf("code=%d output=%s", code, output)
			}
		})
	}
}

func TestMonitoringTraceEnforcesPermissionAndSupportsMemberReads(t *testing.T) {
	for _, member := range []bool{false, true} {
		for _, denied := range []bool{false, true} {
			t.Run(fmt.Sprint("member=", member, "/denied=", denied), func(t *testing.T) {
				fixture := newTraceFixture(t)
				fixture.member, fixture.denied = member, denied
				server := fixture.server()
				defer server.Close()
				args := []string{"monitoring", "trace", "--bot", "bot-a", "--session", "chat-a"}
				var code int
				var output string
				if member {
					args = append(args, "--as-member")
					code, output = memberCLI(t, memberConfig(t, server.URL, "member-private"), "", nil, args...)
				} else {
					code, output = operationCLI(t, server, "", args...)
				}
				if denied {
					if code != 4 || len(fixture.calls) != 0 {
						t.Fatalf("permission check failed code=%d output=%s", code, output)
					}
				} else if code != 0 {
					t.Fatalf("read failed code=%d output=%s", code, output)
				}
			})
		}
	}
}

func TestMonitoringTraceLocatorCanReadOriginalEvidence(t *testing.T) {
	fixture := newTraceFixture(t)
	code, data, output := traceCLI(t, fixture, "json")
	if code != 0 {
		t.Fatal(output)
	}
	locator := data["first_error"].(map[string]any)["locator"].(map[string]any)
	body, err := json.Marshal(locator["input"])
	if err != nil {
		t.Fatal(err)
	}
	fixture.command = "op.call"
	server := fixture.server()
	defer server.Close()
	code, output = operationCLI(t, server, string(body), "op", "call", fmt.Sprint(locator["operation"]), "--file", "-")
	if code != 0 || !strings.Contains(output, "delivery-a") || !strings.Contains(output, "platform rejected reply") {
		t.Fatalf("locator could not read its record code=%d output=%s", code, output)
	}
}

func TestMonitoringTraceFiltersExecutionTimesLocally(t *testing.T) {
	fixture := newTraceFixture(t)
	for kind := range fixture.native {
		fixture.native[kind] = nil
	}
	ref := monitoringTraceRef{"pipeline", "message-a"}
	fixture.sections[ref]["outputs"], fixture.sections[ref]["deliveries"] = nil, nil
	fixture.rows[ref]["created_at_ms"] = time.Now().Add(-time.Hour).UnixMilli()
	fixture.listed = []map[string]any{
		{"source": "event", "id": "future", "bot_id": "bot-a", "session_id": "chat-a", "created_at_ms": time.Now().Add(time.Hour).UnixMilli()},
		fixture.rows[ref],
		{"source": "event", "id": "old", "bot_id": "bot-a", "session_id": "chat-a", "created_at_ms": time.Now().Add(-3 * time.Hour).UnixMilli()},
	}
	fixture.hook = func(request *http.Request, _, _ map[string]any) int {
		if request.Method == http.MethodGet && request.URL.Query().Get("startTime") == "" {
			t.Error("native query omitted its fixed time window")
		}
		return 0
	}
	code, data, output := traceCLI(t, fixture, "json", "--since", "2h")
	if code != 0 || len(data["executions"].([]any)) != 1 || strings.Contains(output, `"id": "future"`) || strings.Contains(output, `"id": "old"`) {
		t.Fatalf("code=%d window result=%s", code, output)
	}
}

func TestMonitoringTraceDoesNotTreatInputAggregateAsFailureStage(t *testing.T) {
	fixture := newTraceFixture(t)
	ref := monitoringTraceRef{"pipeline", "message-a"}
	fixture.native["messages"][0]["status"] = "error"
	fixture.rows[ref]["status"] = "error"
	fixture.native["llm_calls"][0]["status"], fixture.native["llm_calls"][0]["error_message"] = "error", "model rejected request"
	code, data, output := traceCLI(t, fixture, "json")
	if code != 0 || data["first_error"].(map[string]any)["stage"] != "llm_calls" || data["first_error"].(map[string]any)["error"] != "model rejected request" || len(data["errors"].([]any)) != 2 {
		t.Fatalf("aggregate status fabricated an earlier failure code=%d output=%s", code, output)
	}
	for _, input := range traceRecords(t, data, "inputs") {
		if input["error"] != nil {
			t.Fatalf("input receipt misreported as failure %#v", input)
		}
	}
}

func TestMonitoringTraceDoesNotJoinDifferentIdentityNamespacesOrSessions(t *testing.T) {
	fixture := newTraceFixture(t)
	fixture.native["messages"][0]["run_id"] = "shared"
	for _, source := range []string{"pipeline", "event"} {
		for _, chat := range []string{"chat-b", ""} {
			ref := monitoringTraceRef{source, "shared" + chat}
			if chat != "" {
				ref.id = "shared"
			}
			row := map[string]any{"source": source, "id": ref.id, "bot_id": "bot-a", "session_id": chat, "created_at_ms": time.Now().Add(-time.Minute).UnixMilli(), "status": "failed"}
			fixture.rows[ref] = row
			fixture.sections[ref] = map[string][]map[string]any{"inputs": {{"id": "FOREIGN-INPUT"}}, "errors": {{"id": "FOREIGN-ERROR", "error_message": "FOREIGN-FAILURE"}}}
			fixture.listed = append(fixture.listed, row)
		}
	}
	// Core's auto resolver prioritizes AgentRun when a call's message_id collides.
	// The resolved row must still belong to the selected Bot and session.
	foreign := monitoringTraceRef{"agent", "message-a"}
	fixture.rows[foreign] = map[string]any{"source": foreign.source, "id": foreign.id, "bot_id": "bot-a", "session_id": "chat-b", "status": "failed"}
	fixture.aliases["message-a"] = foreign
	code, data, output := traceCLI(t, fixture, "json")
	if code != 6 || !traceHasIssue(data, "execution_scope_unconfirmed") || strings.Contains(output, "FOREIGN-") || len(data["executions"].([]any)) != 1 {
		t.Fatalf("cross-namespace association code=%d output=%s", code, output)
	}
	for _, key := range []string{"pipeline/shared/inputs", "event/shared/inputs"} {
		if len(fixture.calls[key]) != 0 {
			t.Fatalf("foreign execution was queried %s", key)
		}
	}
}

func TestMonitoringTraceScansFilteredPagesBeforeMatchingExecution(t *testing.T) {
	fixture := newTraceFixture(t)
	for kind := range fixture.native {
		fixture.native[kind] = nil
	}
	fixture.listed = nil
	for index := 0; index < 100; index++ {
		fixture.listed = append(fixture.listed, map[string]any{"source": "event", "id": fmt.Sprint(index), "bot_id": "bot-a", "session_id": "chat-a", "created_at_ms": time.Now().Add(time.Hour).UnixMilli()})
	}
	fixture.listed = append(fixture.listed, fixture.rows[monitoringTraceRef{"pipeline", "message-a"}])
	code, data, output := traceCLI(t, fixture, "json", "--since", "2h")
	if code != 0 || len(data["executions"].([]any)) != 1 || !reflect.DeepEqual(fixture.calls["executions"], []int{0, 100}) {
		t.Fatalf("filtered page hid matching execution code=%d output=%s", code, output)
	}
}
