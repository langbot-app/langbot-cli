package command

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/langbot-app/langbot-cli/internal/config"
)

type monitoringWatchRequest struct {
	operation string
	round     int
	ref       monitoringTraceRef
	offset    int
}

var monitoringWatchTestTimestamp = time.Now().Add(-time.Minute).UnixMilli()

type monitoringWatchFixture struct {
	t           *testing.T
	rows        func(int) []map[string]any
	deliveries  func(int, monitoringTraceRef) []map[string]any
	hook        func(monitoringWatchRequest, map[string]any) int
	denied      bool
	member      bool
	httpTimeout string
	mu          sync.Mutex
	round       int
	requests    []monitoringWatchRequest
}

func monitoringWatchRow(source, id, status string) map[string]any {
	return map[string]any{"source": source, "id": id, "bot_id": "bot-a", "session_id": "chat-a", "status": status, "created_at_ms": monitoringWatchTestTimestamp, "input_preview": "PRIVATE-CONVERSATION"}
}

func (fixture *monitoringWatchFixture) calls() []monitoringWatchRequest {
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	return append([]monitoringWatchRequest{}, fixture.requests...)
}

func (fixture *monitoringWatchFixture) server() *httptest.Server {
	fixture.t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		credentialOK := request.Header.Get("X-API-Key") == "test-secret"
		if fixture.member {
			credentialOK = request.Header.Get("Authorization") == "Bearer member-private" && request.Header.Get("X-API-Key") == ""
		}
		if !credentialOK || request.Header.Get("X-LangBot-Client-Command") != "monitoring.watch" {
			fixture.t.Error("watch lost its credential or command identity")
		}
		if fixture.denied && (request.URL.Path == "/api/v1/system/context" || request.URL.Path == "/api/v1/workspaces/current") {
			fmt.Fprint(writer, `{"code":0,"data":{"instance_uuid":"instance","workspace_uuid":"workspace","api_key_id":"key","workspace":{"uuid":"workspace"},"permissions":[]}}`)
			return
		}
		if operationPreflight(writer, request, true) {
			return
		}
		if request.Method != http.MethodPost || request.URL.Path != "/api/v1/operations/monitoring.executions" && request.URL.Path != "/api/v1/operations/monitoring.execution.detail" {
			fixture.t.Errorf("watch attempted a write or unexpected endpoint %s %s", request.Method, request.URL)
			writer.WriteHeader(500)
			return
		}
		var body map[string]any
		decoder := json.NewDecoder(request.Body)
		decoder.UseNumber()
		if err := decoder.Decode(&body); err != nil {
			fixture.t.Error(err)
		}
		offset, _ := strconv.Atoi(fmt.Sprint(body["offset"]))
		fixture.mu.Lock()
		if strings.HasSuffix(request.URL.Path, "monitoring.executions") && offset == 0 {
			fixture.round++
		}
		round := fixture.round
		call := monitoringWatchRequest{operation: strings.TrimPrefix(request.URL.Path, "/api/v1/operations/"), round: round, offset: offset}
		if call.operation == "monitoring.execution.detail" {
			call.ref = monitoringTraceRef{fmt.Sprint(body["source"]), fmt.Sprint(body["execution_id"])}
		}
		fixture.requests = append(fixture.requests, call)
		fixture.mu.Unlock()
		rows := fixture.rows(round)
		response := map[string]any{}
		if call.operation == "monitoring.executions" {
			if len(body) != 4 || fmt.Sprint(body["bot_ids"]) != "[bot-a]" || body["include_summary"] != false || fmt.Sprint(body["limit"]) != "100" {
				fixture.t.Errorf("unexpected list input %#v", body)
			}
			response = map[string]any{"items": traceFixturePage(rows, offset), "total": len(rows), "has_more": offset+100 < len(rows)}
		} else {
			if len(body) != 5 || body["section"] != "deliveries" || fmt.Sprint(body["limit"]) != "100" {
				fixture.t.Errorf("unexpected detail input %#v", body)
			}
			var row map[string]any
			for _, candidate := range rows {
				if candidate["source"] == call.ref.source && candidate["id"] == call.ref.id {
					row = candidate
				}
			}
			if row == nil {
				row = monitoringWatchRow(call.ref.source, call.ref.id, "completed")
			}
			items := []map[string]any{}
			if fixture.deliveries != nil {
				items = fixture.deliveries(round, call.ref)
			}
			response = map[string]any{"row": row, "source": call.ref.source, "legacy_context": false, "pages": map[string]any{"deliveries": map[string]any{"items": traceFixturePage(items, offset), "has_more": offset+100 < len(items), "next_offset": offset + min(100, max(0, len(items)-offset))}}}
		}
		if fixture.hook != nil {
			if status := fixture.hook(call, response); status != 0 {
				writer.WriteHeader(status)
				fmt.Fprint(writer, `{"code":1,"msg":"source unavailable"}`)
				return
			}
		}
		json.NewEncoder(writer).Encode(map[string]any{"code": 0, "data": response})
	}))
}

type monitoringWatchOutput struct {
	bytes.Buffer
	cancel context.CancelFunc
	format string
	rounds int
}

func (writer *monitoringWatchOutput) Write(data []byte) (int, error) {
	n, err := writer.Buffer.Write(data)
	marker := `"kind": "round"`
	if writer.format == "yaml" {
		marker = "kind: round"
	} else if writer.format == "" {
		marker = "证据不完整="
	}
	if writer.rounds > 0 && strings.Count(writer.String(), marker) >= writer.rounds {
		writer.cancel()
	}
	return n, err
}

func runMonitoringWatch(t *testing.T, fixture *monitoringWatchFixture, format string, rounds int, args ...string) (int, []map[string]any, string) {
	t.Helper()
	server := fixture.server()
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	path := filepath.Join(t.TempDir(), "config.yaml")
	configuration := fmt.Sprintf("version: 1\ncurrent_context: test\ncontexts:\n  test:\n    endpoint: %s\n    expected_workspace_uuid: workspace\n    credential:\n      type: env\n      name: TEST_KEY\n", server.URL)
	if fixture.httpTimeout != "" {
		configuration += "    timeout: " + fixture.httpTimeout + "\n"
	}
	if err := os.WriteFile(path, []byte(configuration), 0600); err != nil {
		t.Fatal(err)
	}
	if fixture.member {
		path = memberConfig(t, server.URL, "member-private")
	}
	writer := &monitoringWatchOutput{cancel: cancel, format: format, rounds: rounds}
	arguments := []string{"--config", path}
	if format != "" {
		arguments = append(arguments, "-o", format)
	}
	if fixture.member {
		arguments = append(arguments, "--as-member")
	}
	arguments = append(arguments, "monitoring", "watch", "--bot", "bot-a", "--poll-interval", "1ms")
	arguments = append(arguments, args...)
	code := executeChinese(ctx, arguments, Dependencies{In: strings.NewReader(""), Out: writer, Err: writer, Version: "test", LookupEnv: func(name string) (string, bool) { return "test-secret", name == "TEST_KEY" }})
	if format == "" {
		return code, nil, writer.String()
	}
	return code, decodeOperationStream(t, writer.String(), format), writer.String()
}

func monitoringWatchEvents(envelopes []map[string]any) []map[string]any {
	items := []map[string]any{}
	for _, envelope := range envelopes {
		if data, ok := envelope["data"].(map[string]any); ok && data["kind"] == "execution" {
			items = append(items, data)
		}
	}
	return items
}

func TestMonitoringWatchTracksTypedStatesAndDirectDeliveryIndependently(t *testing.T) {
	for _, format := range []string{"json", "yaml", ""} {
		t.Run("format="+format, func(t *testing.T) {
			fixture := &monitoringWatchFixture{t: t, rows: func(round int) []map[string]any {
				status := "running"
				if round > 1 {
					status = "failed"
				}
				return []map[string]any{monitoringWatchRow("agent", "same-id", status), monitoringWatchRow("pipeline", "same-id", "success")}
			}, deliveries: func(round int, ref monitoringTraceRef) []map[string]any {
				if round > 1 && ref.source == "agent" {
					return []map[string]any{{"id": "delivery-a", "status": "success", "content": "PRIVATE-REPLY"}}
				}
				return nil
			}}
			code, envelopes, output := runMonitoringWatch(t, fixture, format, 3)
			if code != 7 || strings.Contains(output, "PRIVATE-") || strings.Contains(output, "test-secret") {
				t.Fatalf("code=%d output=%s", code, output)
			}
			if format == "" {
				if !strings.Contains(output, "账本=failed 投递=recorded_success") || strings.Count(output, "执行 agent/same-id") != 2 {
					t.Fatalf("human watch lost its states %s", output)
				}
				return
			}
			events := monitoringWatchEvents(envelopes)
			if len(events) != 3 {
				t.Fatalf("unchanged snapshots emitted duplicates %s", output)
			}
			last := events[2]
			if last["source"] != "agent" || last["ledger_status"] != "failed" || last["delivery_status"] != "recorded_success" || last["delivery_evidence"] != "observed" {
				t.Fatalf("ledger failure erased delivery evidence %#v", last)
			}
			if events[1]["source"] != "pipeline" || events[1]["ledger_status"] != "unknown" {
				t.Fatal("pipeline status was presented as an Agent ledger status")
			}
		})
	}
}

func TestMonitoringWatchFindsLateDeliveryWithoutLedgerChange(t *testing.T) {
	fixture := &monitoringWatchFixture{t: t, rows: func(int) []map[string]any { return []map[string]any{monitoringWatchRow("agent", "run-a", "failed")} }, deliveries: func(round int, _ monitoringTraceRef) []map[string]any {
		if round > 1 {
			return []map[string]any{{"id": "delivery-a", "status": "success"}}
		}
		return nil
	}}
	code, envelopes, output := runMonitoringWatch(t, fixture, "json", 3)
	events := monitoringWatchEvents(envelopes)
	if code != 7 || len(events) != 2 || events[0]["delivery_evidence"] != "unknown" || events[1]["delivery_status"] != "recorded_success" || events[1]["ledger_status"] != "failed" {
		t.Fatalf("code=%d output=%s", code, output)
	}
	details := 0
	for _, request := range fixture.calls() {
		if request.operation == "monitoring.execution.detail" {
			details++
		}
	}
	if details != 3 {
		t.Fatalf("recent delivery refresh was omitted %d", details)
	}
}

func TestMonitoringWatchBoundsDetailsAndContinuesLongPages(t *testing.T) {
	fixture := &monitoringWatchFixture{t: t, rows: func(int) []map[string]any { return []map[string]any{monitoringWatchRow("agent", "run-a", "completed")} }, deliveries: func(int, monitoringTraceRef) []map[string]any {
		items := make([]map[string]any, 0, 205)
		for index := 0; index < 205; index++ {
			items = append(items, map[string]any{"id": fmt.Sprintf("delivery-%03d", index), "status": "success"})
		}
		return items
	}}
	code, envelopes, output := runMonitoringWatch(t, fixture, "json", 3, "--max-details", "1")
	events := monitoringWatchEvents(envelopes)
	if code != 7 || len(events) != 3 || len(events[2]["delivery_records"].([]any)) != 205 || events[2]["delivery_scan_complete"] != true {
		t.Fatalf("delivery pagination code=%d output=%s", code, output)
	}
	offsets := []int{}
	for _, request := range fixture.calls() {
		if request.operation == "monitoring.execution.detail" {
			offsets = append(offsets, request.offset)
		}
	}
	if !reflect.DeepEqual(offsets, []int{0, 100, 200}) {
		t.Fatalf("bounded delivery cursors %v", offsets)
	}
}

func TestMonitoringWatchRotatesDeferredDetailsFairly(t *testing.T) {
	fixture := &monitoringWatchFixture{t: t, rows: func(int) []map[string]any {
		return []map[string]any{monitoringWatchRow("agent", "a", "failed"), monitoringWatchRow("agent", "b", "failed"), monitoringWatchRow("agent", "c", "failed")}
	}}
	code, envelopes, output := runMonitoringWatch(t, fixture, "json", 3, "--max-details", "1")
	if code != 7 || len(monitoringWatchEvents(envelopes)) != 5 {
		t.Fatalf("deferred states code=%d output=%s", code, output)
	}
	ids := []string{}
	for _, request := range fixture.calls() {
		if request.operation == "monitoring.execution.detail" {
			ids = append(ids, request.ref.id)
		}
	}
	if !reflect.DeepEqual(ids, []string{"a", "b", "c"}) {
		t.Fatalf("detail budget starved pending work %v", ids)
	}
	for _, envelope := range envelopes {
		data, _ := envelope["data"].(map[string]any)
		if data["kind"] == "round" && data["last_round"].(map[string]any)["truncated"] != true {
			t.Fatalf("detail boundary omitted %#v", data)
		}
	}
}

func TestMonitoringWatchPaginatesAndStopsEachRoundAtItsBoundary(t *testing.T) {
	for _, scenario := range []string{"full-pages", "page-budget", "initial-total-boundary", "future-page"} {
		t.Run(scenario, func(t *testing.T) {
			fixture := &monitoringWatchFixture{t: t, rows: func(int) []map[string]any {
				rows := []map[string]any{}
				for index := 0; index < 205; index++ {
					row := monitoringWatchRow("agent", fmt.Sprintf("run-%03d", index), "completed")
					if scenario == "future-page" && index < 100 {
						row["created_at_ms"] = time.Now().Add(time.Hour).UnixMilli()
					}
					rows = append(rows, row)
				}
				return rows
			}}
			args := []string{"--max-pages", "3", "--max-details", "1"}
			if scenario == "page-budget" {
				args = []string{"--max-pages", "1", "--max-details", "1"}
			}
			if scenario == "initial-total-boundary" {
				fixture.hook = func(call monitoringWatchRequest, response map[string]any) int {
					if call.operation == "monitoring.executions" {
						response["total"], response["has_more"] = 100, true
					}
					return 0
				}
			}
			code, envelopes, output := runMonitoringWatch(t, fixture, "json", 1, args...)
			expected := 205
			pages := 3
			if scenario == "future-page" {
				expected = 105
			} else if scenario != "full-pages" {
				expected, pages = 100, 1
			}
			if code != 7 || len(monitoringWatchEvents(envelopes)) != expected {
				t.Fatalf("code=%d events=%d output=%s", code, len(monitoringWatchEvents(envelopes)), output)
			}
			calls := 0
			for _, request := range fixture.calls() {
				if request.operation == "monitoring.executions" {
					calls++
				}
			}
			if calls != pages {
				t.Fatalf("round escaped its page boundary calls=%d", calls)
			}
		})
	}
}

func TestMonitoringWatchReconnectsReadOnlySourcesAndDeduplicates(t *testing.T) {
	fixture := &monitoringWatchFixture{t: t, rows: func(int) []map[string]any { return []map[string]any{monitoringWatchRow("agent", "run-a", "running")} }, hook: func(call monitoringWatchRequest, _ map[string]any) int {
		if call.operation == "monitoring.executions" && call.round == 1 {
			return http.StatusServiceUnavailable
		}
		return 0
	}}
	code, envelopes, output := runMonitoringWatch(t, fixture, "json", 3)
	if code != 7 || len(monitoringWatchEvents(envelopes)) != 1 {
		t.Fatalf("read reconnection duplicated observations code=%d output=%s", code, output)
	}
	last := envelopes[len(envelopes)-1]["data"].(map[string]any)
	if last["partial"] != true || last["last_complete_end"] == nil {
		t.Fatalf("source gap or recovery watermark lost %#v", last)
	}
}

func TestMonitoringWatchCancelAndTimeoutKeepExecutableResume(t *testing.T) {
	for _, timeout := range []bool{false, true} {
		t.Run(fmt.Sprint(timeout), func(t *testing.T) {
			fixture := &monitoringWatchFixture{t: t, rows: func(int) []map[string]any { return []map[string]any{} }}
			rounds := 1
			args := []string{"--start-time", "2026-11-17T00:00:00Z"}
			if timeout {
				rounds = 0
				args = append(args, "--poll-interval", "1s", "--timeout", "80ms")
			}
			code, envelopes, output := runMonitoringWatch(t, fixture, "json", rounds, args...)
			last := envelopes[len(envelopes)-1]
			if code != 7 || last["data"].(map[string]any)["start_time"] != "2026-11-17T00:00:00Z" {
				t.Fatalf("code=%d output=%s", code, output)
			}
			found := false
			for _, raw := range last["meta"].(map[string]any)["next_steps"].([]any) {
				encoded, _ := json.Marshal(raw.(map[string]any)["argv"])
				var argv []string
				json.Unmarshal(encoded, &argv)
				if strings.Contains(strings.Join(argv, " "), "monitoring watch --bot bot-a --start-time 2026-11-17T00:00:00Z") {
					found = true
					if !strings.Contains(strings.Join(argv, " "), "--max-pages 10 --max-details 20") {
						t.Fatalf("resume lost budgets %#v", argv)
					}
					server := fixture.server()
					defer server.Close()
					if err := (config.Store{Path: argv[2]}).Update(context.Background(), func(file *config.File) error {
						selected := file.Contexts["test"]
						selected.Endpoint = server.URL
						file.Contexts["test"] = selected
						return nil
					}); err != nil {
						t.Fatal(err)
					}
					ctx, cancel := context.WithCancel(context.Background())
					defer cancel()
					writer := &monitoringWatchOutput{cancel: cancel, format: "json", rounds: 1}
					resumeCode := executeChinese(ctx, argv[1:], Dependencies{In: strings.NewReader(""), Out: writer, Err: writer, Version: "test", LookupEnv: func(name string) (string, bool) { return "test-secret", name == "TEST_KEY" }})
					resumed := decodeOperationStream(t, writer.String(), "json")
					if resumeCode != 7 || len(resumed) != 2 || resumed[0]["data"].(map[string]any)["start_time"] != "2026-11-17T00:00:00Z" {
						t.Fatalf("printed resume did not execute code=%d output=%s", resumeCode, writer.String())
					}
				}
			}
			if !found {
				t.Fatalf("resume command missing %s", output)
			}
		})
	}
}

func TestMonitoringWatchPermissionAndMemberEntryPoints(t *testing.T) {
	for _, member := range []bool{false, true} {
		for _, denied := range []bool{false, true} {
			t.Run(fmt.Sprint(member, denied), func(t *testing.T) {
				fixture := &monitoringWatchFixture{t: t, denied: denied, member: member, rows: func(int) []map[string]any { return nil }}
				code, _, output := runMonitoringWatch(t, fixture, "json", 1)
				if denied {
					if code != 4 || len(fixture.calls()) != 0 {
						t.Fatalf("permission bypass code=%d output=%s", code, output)
					}
				} else if code != 7 {
					t.Fatalf("member read failed code=%d output=%s", code, output)
				}
			})
		}
	}
}

func TestMonitoringWatchValidatesLocalFlagsAndSchema(t *testing.T) {
	for _, args := range [][]string{{"--bot", ""}, {"--since", "0"}, {"--since", "-1h"}, {"--since", ""}, {"--start-time", "tomorrow"}, {"--start-time", "2999-01-01T00:00:00Z"}, {"--start-time", "2026-11-17T00:00:00Z", "--since", "1h"}, {"--poll-interval", "0"}, {"--max-pages", "0"}, {"--max-details", "-1"}, {"--timeout", "0"}} {
		fixture := &monitoringWatchFixture{t: t, rows: func(int) []map[string]any { return nil }}
		code, _, output := runMonitoringWatch(t, fixture, "json", 1, args...)
		if code != 2 || len(fixture.calls()) != 0 {
			t.Fatalf("invalid flags reached Core args=%v code=%d output=%s", args, code, output)
		}
	}
	result := executeSchemaForTest(t, "schema", "--command", "monitoring.watch")
	var envelope schemaEnvelope
	if result.code != 0 || json.Unmarshal([]byte(result.output), &envelope) != nil || len(envelope.Data.Commands) != 1 {
		t.Fatal(result.output)
	}
	command := envelope.Data.Commands[0]
	if command.Mutating || command.RequiresYes || !hasFlag(command.Flags, "bot", "local", true) || len(command.ConditionalOutputs) != 2 || len(command.InputConflicts) != 1 || !strings.Contains(command.Examples, "--start-time") {
		t.Fatalf("watch schema contract %#v", command)
	}
}

func TestMonitoringWatchRefreshesPendingAndAdditionalDeliveries(t *testing.T) {
	fixture := &monitoringWatchFixture{t: t, rows: func(int) []map[string]any { return []map[string]any{monitoringWatchRow("agent", "run-a", "failed")} }, deliveries: func(round int, _ monitoringTraceRef) []map[string]any {
		status := "pending"
		if round >= 2 {
			status = "success"
		}
		items := []map[string]any{{"id": "delivery-a", "status": status}}
		if round >= 3 {
			items = append(items, map[string]any{"id": "delivery-b", "status": "error", "delivery": map[string]any{"delivery_error": "platform rejected reply"}})
		}
		return items
	}}
	code, envelopes, output := runMonitoringWatch(t, fixture, "json", 4)
	events := monitoringWatchEvents(envelopes)
	if code != 7 || len(events) != 3 || events[0]["delivery_status"] != "unknown" || events[1]["delivery_status"] != "recorded_success" || events[2]["delivery_status"] != "mixed" {
		t.Fatalf("late delivery changes lost code=%d output=%s", code, output)
	}
	for _, event := range events {
		if event["ledger_status"] != "failed" || event["delivery_watch_until"] == nil {
			t.Fatal("direct delivery overwrote ledger failure or its finite observation deadline")
		}
	}
}

func TestMonitoringWatchDefaultsToContinuousWithBoundedHTTPRequests(t *testing.T) {
	fixture := &monitoringWatchFixture{t: t, httpTimeout: "100ms", rows: func(int) []map[string]any { return nil }}
	code, envelopes, output := runMonitoringWatch(t, fixture, "json", 2, "--poll-interval", "150ms")
	last := envelopes[len(envelopes)-1]["data"].(map[string]any)
	if code != 7 || fmt.Sprint(last["round"]) != "2" || last["last_complete_end"] == nil {
		t.Fatalf("HTTP timeout ended the watch lifetime code=%d output=%s", code, output)
	}
}

func TestMonitoringWatchFollowsActiveRunOutsideRollingWindow(t *testing.T) {
	finished := time.Now().UnixMilli()
	fixture := &monitoringWatchFixture{t: t, rows: func(round int) []map[string]any {
		row := monitoringWatchRow("agent", "old-run", "running")
		row["created_at_ms"] = time.Now().Add(-time.Hour).Truncate(time.Minute).UnixMilli()
		if round > 1 {
			row["status"], row["finished_at_ms"] = "completed", finished
		}
		return []map[string]any{row}
	}, deliveries: func(round int, _ monitoringTraceRef) []map[string]any {
		if round > 1 {
			return []map[string]any{{"id": "late-reply", "status": "success"}}
		}
		return nil
	}}
	code, envelopes, output := runMonitoringWatch(t, fixture, "json", 3, "--since", "2h")
	events := monitoringWatchEvents(envelopes)
	last := envelopes[len(envelopes)-1]["data"].(map[string]any)
	if code != 7 || len(events) != 2 || events[1]["ledger_status"] != "completed" || events[1]["delivery_status"] != "recorded_success" || fmt.Sprint(last["cache_evicted"]) != "0" {
		t.Fatalf("active run expired before its late terminal evidence code=%d output=%s", code, output)
	}
}

func TestMonitoringWatchReportsNormalExpiryAndBoundedStateRetention(t *testing.T) {
	for _, overflow := range []bool{false, true} {
		t.Run(fmt.Sprint("overflow=", overflow), func(t *testing.T) {
			fixture := &monitoringWatchFixture{t: t, rows: func(round int) []map[string]any {
				if !overflow {
					row := monitoringWatchRow("agent", "expired-run", "completed")
					row["created_at_ms"] = time.Now().Add(-30 * time.Minute).Truncate(time.Minute).UnixMilli()
					return []map[string]any{row}
				}
				rows := []map[string]any{}
				for index := 0; index < 100; index++ {
					rows = append(rows, monitoringWatchRow("agent", fmt.Sprintf("round-%d-%d", round, index), "running"))
				}
				return rows
			}}
			code, envelopes, output := runMonitoringWatch(t, fixture, "json", 2, "--since", "1h", "--max-pages", "1", "--max-details", "1")
			last := envelopes[len(envelopes)-1]["data"].(map[string]any)
			if code != 7 || last["scan_start_time"] == last["start_time"] || fmt.Sprint(last["cache_limit"]) != "100" {
				t.Fatalf("finite retention boundary lost code=%d output=%s", code, output)
			}
			if overflow {
				if fmt.Sprint(last["executions_retained"]) != "100" || fmt.Sprint(last["cache_dropped"]) != "100" || last["partial"] != true {
					t.Fatalf("active cache overflow hidden %s", output)
				}
			} else if fmt.Sprint(last["cache_expired"]) != "1" || fmt.Sprint(last["executions_retained"]) != "0" || last["partial"] != false || monitoringWatchEvents(envelopes)[0]["delivery_watch_expired"] != true {
				t.Fatalf("natural retirement was reported as a current-window gap %s", output)
			}
		})
	}
}

func TestMonitoringWatchCapsDeliveryEvidenceWithoutClaimingCompletion(t *testing.T) {
	fixture := &monitoringWatchFixture{t: t, rows: func(int) []map[string]any { return []map[string]any{monitoringWatchRow("agent", "run-a", "completed")} }, deliveries: func(int, monitoringTraceRef) []map[string]any {
		items := make([]map[string]any, 0, monitoringWatchRetentionLimit+1)
		for index := 0; index <= monitoringWatchRetentionLimit; index++ {
			items = append(items, map[string]any{"id": fmt.Sprint(index), "status": "success"})
		}
		return items
	}}
	code, envelopes, _ := runMonitoringWatch(t, fixture, "json", 10, "--max-details", "1")
	events := monitoringWatchEvents(envelopes)
	lastEvent := events[len(events)-1]
	last := envelopes[len(envelopes)-1]["data"].(map[string]any)
	if code != 7 || len(lastEvent["delivery_records"].([]any)) != monitoringWatchRetentionLimit || lastEvent["delivery_scan_complete"] != false || lastEvent["delivery_truncated"] != true || fmt.Sprint(last["delivery_truncated"]) != "1" || last["partial"] != true {
		t.Fatalf("bounded evidence claimed completeness code=%d summary=%#v", code, last)
	}
}

func TestMonitoringWatchRejectsMalformedOrForeignEvidenceAndRetainsProgress(t *testing.T) {
	for _, scenario := range []string{"malformed-list", "foreign-list", "foreign-detail", "permission"} {
		t.Run(scenario, func(t *testing.T) {
			fixture := &monitoringWatchFixture{t: t, rows: func(int) []map[string]any {
				row := monitoringWatchRow("agent", "run-a", "running")
				if scenario == "foreign-list" {
					row["bot_id"], row["id"] = "bot-b", "FOREIGN-RUN"
				}
				return []map[string]any{row}
			}, hook: func(call monitoringWatchRequest, response map[string]any) int {
				if scenario == "permission" {
					return http.StatusForbidden
				}
				if scenario == "malformed-list" && call.operation == "monitoring.executions" {
					response["total"] = "bad"
				}
				if scenario == "foreign-detail" && call.operation == "monitoring.execution.detail" {
					response["row"].(map[string]any)["bot_id"] = "bot-b"
					response["pages"].(map[string]any)["deliveries"] = map[string]any{"items": []any{map[string]any{"id": "FOREIGN-REPLY", "status": "success"}}, "has_more": false}
				}
				return 0
			}}
			rounds, expected := 0, 8
			if scenario == "foreign-list" {
				rounds, expected = 1, 7
			} else if scenario == "permission" {
				expected = 4
			}
			code, envelopes, output := runMonitoringWatch(t, fixture, "json", rounds, "--timeout", "1s")
			last := envelopes[len(envelopes)-1]["data"].(map[string]any)
			if code != expected || last["partial"] != true || strings.Contains(output, "FOREIGN-") || last["last_complete_end"] != nil {
				t.Fatalf("unsafe evidence or progress code=%d output=%s", code, output)
			}
		})
	}
}

func TestMonitoringWatchDeduplicatesOverlappingListPages(t *testing.T) {
	fixture := &monitoringWatchFixture{t: t, rows: func(int) []map[string]any {
		rows := []map[string]any{}
		for index := 0; index < 205; index++ {
			rows = append(rows, monitoringWatchRow("agent", fmt.Sprintf("run-%03d", index), "completed"))
		}
		return rows
	}, hook: func(call monitoringWatchRequest, response map[string]any) int {
		if call.operation == "monitoring.executions" && call.offset == 100 {
			response["items"].([]map[string]any)[0] = monitoringWatchRow("agent", "run-099", "completed")
		}
		return 0
	}}
	code, envelopes, output := runMonitoringWatch(t, fixture, "json", 1, "--max-pages", "3", "--max-details", "1")
	last := envelopes[len(envelopes)-1]["data"].(map[string]any)
	if code != 7 || len(monitoringWatchEvents(envelopes)) != 204 || last["partial"] != true || last["last_complete_end"] != nil {
		t.Fatalf("overlapping pages lost their gap marker code=%d count=%d output=%s", code, len(monitoringWatchEvents(envelopes)), output)
	}
}

func TestMonitoringWatchBoundsCacheForLargestPageBudget(t *testing.T) {
	fixture := &monitoringWatchFixture{t: t, rows: func(int) []map[string]any { return nil }}
	largestInt := int(^uint(0) >> 1)
	code, envelopes, output := runMonitoringWatch(t, fixture, "json", 1, "--max-pages", strconv.Itoa(largestInt))
	last := envelopes[len(envelopes)-1]["data"].(map[string]any)
	if code != 7 || fmt.Sprint(last["cache_limit"]) != "1000" {
		t.Fatalf("page budget overflowed its bounded cache code=%d output=%s", code, output)
	}
}
