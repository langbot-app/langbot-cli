package command

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func TestAssistantChatConversation(t *testing.T) {
	revision, status := 0, "ready"
	var calls []string
	var turns []map[string]any
	view := func() map[string]any {
		out := map[string]any{"uuid": "room", "revision": revision, "status": status, "messages": []any{}, "pending": []any{}}
		if status == "approval" {
			out["pending"] = []any{map[string]any{"name": "workspace_call", "arguments": map[string]any{"operation_id": "skill.update", "round": revision}}}
		}
		return out
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if operationPreflight(w, r, true) {
			return
		}
		op := strings.TrimPrefix(r.URL.Path, "/api/v1/operations/")
		calls = append(calls, op)
		if r.Header.Get("X-API-Key") != "" || r.Header.Get("Authorization") == "" {
			t.Error("expected member credentials")
		}
		if op == "assistant.stream/stream" {
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			turns = append(turns, body)
			if body["conversation_id"] != "room" || body["revision"] != float64(revision) {
				t.Errorf("turn %+v", body)
			}
			revision++
			status = "approval"
			if body["approved"] == false {
				status = "ready"
			}
			w.Header().Set("Content-Type", "text/event-stream")
			data, _ := json.Marshal(view())
			fmt.Fprintf(w, "data: {\"kind\":\"completed\",\"data\":%s}\n\nevent: done\ndata: {}\n\n", data)
			return
		}
		if op != "assistant.create" && op != "assistant.get" {
			t.Errorf("unexpected op %s", op)
		}
		json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": view()})
	}))
	defer server.Close()
	for _, tc := range []struct {
		args   []string
		want   int
		status string
	}{
		{[]string{"--message", "hello", "--yes"}, 0, "approval"},
		{[]string{"room", "--yes"}, 0, "approval"},
		{[]string{"room", "--approve", "--yes"}, 2, ""},
		{[]string{"room", "--approve", "--revision", "0", "--yes"}, 6, "approval"},
		{[]string{"room", "--approve", "--revision", "1", "--yes"}, 0, "approval"},
		{[]string{"room", "--reject", "--revision", "2", "--yes"}, 0, "ready"},
	} {
		code, output := operationCLI(t, server, "", append([]string{"assistant", "chat"}, tc.args...)...)
		if code != tc.want || (tc.status != "" && !strings.Contains(output, tc.status)) {
			t.Fatalf("%v = %d %s", tc.args, code, output)
		}
	}
	if !reflect.DeepEqual(calls, []string{"assistant.create", "assistant.stream/stream", "assistant.get", "assistant.get", "assistant.get", "assistant.stream/stream", "assistant.get", "assistant.stream/stream"}) || len(turns) != 3 || turns[0]["text"] != "hello" || turns[1]["approved"] != true || turns[2]["approved"] != false {
		t.Fatalf("calls %v, turns %v", calls, turns)
	}
}

func TestAssistantChatUncertainStreamOnlyReadsProgress(t *testing.T) {
	for _, tc := range []struct {
		name, frame string
		code        int
	}{
		{"disconnected", "", 7}, {"stale", "data: {\"kind\":\"error\",\"code\":\"stale_turn\"}\n\n", 6},
		{"missing result", "event: done\ndata: {}\n\n", 7},
		{"wrong conversation", "data: {\"kind\":\"completed\",\"data\":{\"uuid\":\"another\",\"revision\":3,\"status\":\"ready\"}}\n\n", 8},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls []string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if operationPreflight(w, r, true) {
					return
				}
				op := strings.TrimPrefix(r.URL.Path, "/api/v1/operations/")
				calls = append(calls, op)
				switch op {
				case "assistant.get":
					status := "ready"
					if len(calls) > 1 {
						status = "running"
					}
					fmt.Fprintf(w, `{"code":0,"data":{"uuid":"room","revision":2,"status":%q}}`, status)
				case "assistant.stream/stream":
					w.Header().Set("Content-Type", "text/event-stream")
					fmt.Fprint(w, tc.frame)
				default:
					t.Errorf("unexpected %s", op)
				}
			}))
			defer server.Close()
			code, output := operationCLI(t, server, "", "assistant", "chat", "room", "--message", "once", "--yes")
			if code != tc.code || !strings.Contains(output, "running") || !reflect.DeepEqual(calls, []string{"assistant.get", "assistant.stream/stream", "assistant.get"}) {
				t.Fatalf("%v %d %s", calls, code, output)
			}
		})
	}
}

func TestAssistantChatPromptShowsBatchForEachDecision(t *testing.T) {
	reader := bufio.NewReader(strings.NewReader("yes\nno\n\n"))
	for _, want := range []any{true, false, nil} {
		var out bytes.Buffer
		body, err := chatPrompt(reader, &out, chatView{revision: 8, status: "approval", data: map[string]any{"pending": []any{map[string]any{"name": "change", "arguments": map[string]any{"risk": "write"}}}}})
		if err != nil || body["approved"] != want || !strings.Contains(out.String(), "change") || !strings.Contains(out.String(), "write") || !strings.Contains(out.String(), "8") {
			t.Fatalf("%v %v %s", body, err, &out)
		}
	}
}
