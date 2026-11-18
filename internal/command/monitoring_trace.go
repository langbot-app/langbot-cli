package command

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/langbot-app/langbot-cli/internal/api"
	"github.com/langbot-app/langbot-cli/internal/app"
	"github.com/langbot-app/langbot-cli/internal/i18n"
	"github.com/langbot-app/langbot-cli/internal/result"
	"github.com/spf13/cobra"
)

type monitoringTraceRef struct{ source, id string }

type monitoringTraceEvidence struct{ failed, generated, delivered bool }

type monitoringTrace struct {
	ctx          context.Context
	session      *app.OperationSession
	bot, chat    string
	start, end   time.Time
	queue        []monitoringTraceRef
	queued, done map[monitoringTraceRef]bool
	identities   map[monitoringTraceRef]bool
	records      map[string]map[string]any
	executions   []map[string]any
	issues       []map[string]any
}

func addMonitoringTrace(root *cobra.Command, service *app.Service, deps Dependencies, flags *globalFlags) {
	var bot, chat, since string
	cmd := &cobra.Command{Use: "trace", Short: i18n.T("Trace a Bot session using recorded identities"), Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		return emitCall(deps, flags, func() (app.Result, error) {
			if strings.TrimSpace(bot) == "" || strings.TrimSpace(chat) == "" {
				return app.Result{}, result.New("input", i18n.T("monitoring trace requires --bot and --session"))
			}
			end := time.Now().UTC()
			start := time.Time{}
			if cmd.Flags().Changed("since") {
				duration, err := time.ParseDuration(since)
				if err != nil || duration <= 0 {
					return app.Result{}, result.New("input", i18n.T("since must be a positive duration"))
				}
				start = end.Add(-duration)
			}
			session, err := service.OpenOperations(cmd.Context(), connectionOptions(flags), false)
			if err != nil {
				return app.Result{}, err
			}
			if err := session.Authorize(friendlyOperationDetails["monitoring.executions"].description()); err != nil {
				return session.Result(nil), err
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), session.Target.Timeout)
			defer cancel()
			trace := monitoringTrace{
				ctx: ctx, session: session, bot: bot, chat: chat, start: start, end: end,
				queued: map[monitoringTraceRef]bool{}, done: map[monitoringTraceRef]bool{},
				identities: map[monitoringTraceRef]bool{}, records: map[string]map[string]any{},
				executions: []map[string]any{}, issues: []map[string]any{},
			}
			err = trace.collect()
			data := trace.report()
			if err == nil && len(trace.issues) > 0 {
				err = result.New("precondition", i18n.T("Monitoring trace is partial. Inspect the recorded gaps and source errors"))
				result.AsError(err).Type = "trace_incomplete"
			}
			return session.Result(data), err
		})
	}}
	cmd.Flags().StringVar(&bot, "bot", "", i18n.T("Bot ID"))
	cmd.Flags().StringVar(&chat, "session", "", i18n.T("Session ID"))
	cmd.Flags().StringVar(&since, "since", "", i18n.T("Look back by a duration such as 2h"))
	cmd.Example = "  lbctl monitoring trace --bot BOT_UUID --session SESSION_ID\n  lbctl monitoring trace --bot BOT_UUID --session SESSION_ID --since 2h -o json"
	commandGroup(root, []string{"monitoring"}).AddCommand(cmd)
}

func (trace *monitoringTrace) collect() error {
	for _, kind := range []string{"messages", "llm_calls", "tool_calls", "errors"} {
		if err := trace.collectList(kind); err != nil {
			trace.issue("source_unavailable", kind, err)
			return err
		}
	}
	if err := trace.collectExecutionList(); err != nil {
		trace.issue("source_unavailable", "monitoring.executions", err)
		return err
	}
	for index := 0; index < len(trace.queue); index++ {
		if err := trace.collectExecution(trace.queue[index]); err != nil {
			return err
		}
	}
	for _, record := range trace.records {
		if record["stage"] != "inputs" && len(record["executions"].([]map[string]any)) == 0 {
			trace.issue("unlinked_record", fmt.Sprint(record["stage"], "/", record["id"]), nil)
		}
	}
	if len(trace.records) == 0 && len(trace.executions) == 0 {
		trace.issue("no_matching_records", "session", nil)
	}
	return nil
}

func (trace *monitoringTrace) collectList(kind string) error {
	query := url.Values{"botId": {trace.bot}, "endTime": {trace.end.Format(time.RFC3339Nano)}, "limit": {"100"}}
	if !trace.start.IsZero() {
		query.Set("startTime", trace.start.Format(time.RFC3339Nano))
	}
	if kind == "messages" || kind == "tool_calls" {
		query.Set("sessionId", trace.chat)
	}
	previousTotal := int64(-1)
	seen := map[string]bool{}
	for offset := int64(0); ; {
		query.Set("offset", strconv.FormatInt(offset, 10))
		path := "/api/v1/monitoring/" + strings.ReplaceAll(kind, "_", "-") + "?" + query.Encode()
		operation, ok := api.ResolveOperation(http.MethodGet, path)
		if !ok {
			return invalidMonitoringTrace()
		}
		value, err := trace.session.Client.APIRequest(trace.ctx, trace.session.Target, operation, nil)
		if err != nil {
			return err
		}
		page, ok := value.(map[string]any)
		if !ok {
			return invalidMonitoringTrace()
		}
		items, ok := page[kind].([]any)
		total, valid := monitoringTraceNumber(page["total"])
		if !ok || !valid || total < 0 {
			return invalidMonitoringTrace()
		}
		if reported, exists := monitoringTraceNumber(page["offset"]); exists && reported != offset {
			trace.issue("source_offset_truncated", kind, nil)
			return nil
		}
		if previousTotal >= 0 && previousTotal != total {
			trace.issue("source_changed_during_query", kind, nil)
		}
		previousTotal = total
		progress := false
		for _, value := range items {
			item, ok := value.(map[string]any)
			if !ok || monitoringTraceID(item["id"]) == "" {
				return invalidMonitoringTrace()
			}
			id := monitoringTraceID(item["id"])
			if seen[id] {
				trace.issue("source_changed_during_query", kind, nil)
			}
			progress = progress || !seen[id]
			seen[id] = true
			if item["bot_id"] != trace.bot || item["session_id"] != trace.chat {
				continue
			}
			stage := kind
			if kind == "messages" {
				stage = "inputs"
				if item["role"] == "assistant" {
					stage = "deliveries"
					if monitoringTraceID(item["run_id"]) == "" && monitoringTraceID(item["event_id"]) == "" && monitoringTraceID(item["parent_message_id"]) == "" {
						stage = "session_context"
					}
				}
				for _, ref := range []monitoringTraceRef{{"pipeline", id}, {"pipeline", monitoringTraceID(item["parent_message_id"])}, {"event", monitoringTraceID(item["event_id"])}, {"agent", monitoringTraceID(item["run_id"])}} {
					if ref.id != "" {
						trace.identities[ref] = true
					}
				}
				root := monitoringTraceID(item["parent_message_id"])
				source := "pipeline"
				if root == "" {
					root = monitoringTraceID(item["id"])
					if root == monitoringTraceID(item["event_id"]) {
						source = "event"
					}
				}
				if stage != "session_context" {
					trace.enqueue(source, root)
				}
			} else if id := monitoringTraceID(item["message_id"]); id != "" {
				trace.enqueue("auto", id)
			}
			trace.record(stage, item, monitoringTraceRef{}, map[string]any{"method": "GET", "path": path, "record_id": item["id"]})
		}
		if offset+int64(len(items)) >= total {
			if offset+int64(len(items)) > total {
				trace.issue("source_changed_during_query", kind, nil)
			}
			return nil
		}
		if len(items) == 0 || !progress {
			trace.issue("source_page_missing", kind, nil)
			return nil
		}
		offset += int64(len(items))
	}
}

func (trace *monitoringTrace) collectExecutionList() error {
	previousTotal := int64(-1)
	seen := map[monitoringTraceRef]bool{}
	for offset := int64(0); ; {
		value, err := trace.session.Client.OperationCall(trace.ctx, trace.session.Target, "monitoring.executions", map[string]any{"bot_ids": []string{trace.bot}, "include_summary": false, "limit": 100, "offset": offset})
		if err != nil {
			return err
		}
		page, ok := value.(map[string]any)
		if !ok {
			return invalidMonitoringTrace()
		}
		items, ok := page["items"].([]any)
		total, valid := monitoringTraceNumber(page["total"])
		more, hasMore := page["has_more"].(bool)
		if !ok || !valid || total < 0 || !hasMore {
			return invalidMonitoringTrace()
		}
		if previousTotal >= 0 && total != previousTotal {
			trace.issue("source_changed_during_query", "monitoring.executions", nil)
		}
		previousTotal = total
		older := len(items) > 0 && !trace.start.IsZero()
		progress := false
		for _, value := range items {
			item, ok := value.(map[string]any)
			if !ok {
				return invalidMonitoringTrace()
			}
			stamp, timed := monitoringTraceTime(item)
			if !timed || stamp >= trace.start.UnixMilli() {
				older = false
			}
			id, source := monitoringTraceID(item["id"]), monitoringTraceID(item["source"])
			if id == "" || source != "agent" && source != "pipeline" && source != "event" {
				return invalidMonitoringTrace()
			}
			ref := monitoringTraceRef{source, id}
			if seen[ref] {
				trace.issue("source_changed_during_query", "monitoring.executions", nil)
			}
			progress = progress || !seen[ref]
			seen[ref] = true
			if item["bot_id"] != trace.bot || timed && (stamp > trace.end.UnixMilli() || !trace.start.IsZero() && stamp < trace.start.UnixMilli()) {
				continue
			}
			chat := monitoringTraceID(item["session_id"])
			if chat == trace.chat || chat == "" && (trace.identities[ref] || trace.identities[monitoringTraceRef{"event", monitoringTraceID(item["event_id"])}]) {
				if !timed {
					trace.issue("execution_time_missing", source+"/"+id, nil)
				}
				trace.enqueue(source, id)
			}
		}
		if !more || older {
			if !more && offset+int64(len(items)) < total {
				trace.issue("source_page_missing", "monitoring.executions", nil)
			}
			return nil
		}
		if len(items) == 0 || !progress {
			trace.issue("source_page_missing", "monitoring.executions", nil)
			return nil
		}
		offset += int64(len(items))
	}
}

func (trace *monitoringTrace) enqueue(source, id string) {
	ref := monitoringTraceRef{source, id}
	if id != "" && !trace.queued[ref] {
		trace.queued[ref] = true
		trace.queue = append(trace.queue, ref)
	}
}

func (trace *monitoringTrace) collectExecution(request monitoringTraceRef) error {
	if trace.done[request] {
		return nil
	}
	ref := request
	for _, section := range []string{"inputs", "outputs", "deliveries", "events", "llm_calls", "tool_calls", "errors", "related"} {
		seen := map[string]bool{}
		for offset := int64(0); ; {
			body := map[string]any{"source": request.source, "execution_id": request.id, "section": section, "limit": 100, "offset": offset}
			value, err := trace.session.Client.OperationCall(trace.ctx, trace.session.Target, "monitoring.execution.detail", body)
			if err != nil {
				trace.issue("execution_section_unavailable", request.source+"/"+request.id+"/"+section, err)
				kind := result.AsError(err).Kind
				if kind != "not_found" && kind != "incompatible" {
					return err
				}
				return nil
			}
			detail, ok := value.(map[string]any)
			if !ok {
				trace.issue("invalid_execution_detail", request.source+"/"+request.id, nil)
				return nil
			}
			row, ok := detail["row"].(map[string]any)
			if !ok || row["bot_id"] != trace.bot {
				trace.issue("execution_scope_unconfirmed", request.source+"/"+request.id, nil)
				return nil
			}
			if status := row["status"]; status != nil {
				if _, ok := status.(string); !ok {
					trace.issue("invalid_execution_detail", request.source+"/"+request.id, nil)
					return nil
				}
			}
			current := monitoringTraceRef{monitoringTraceID(row["source"]), monitoringTraceID(row["id"])}
			if current.id == "" || current.source != "agent" && current.source != "pipeline" && current.source != "event" {
				trace.issue("invalid_execution_identity", request.source+"/"+request.id, nil)
				return nil
			}
			if chat := monitoringTraceID(row["session_id"]); chat != trace.chat {
				if chat != "" || !trace.identities[current] && !trace.identities[monitoringTraceRef{"event", monitoringTraceID(row["event_id"])}] {
					trace.issue("execution_scope_unconfirmed", current.source+"/"+current.id, nil)
					return nil
				}
				trace.issue("execution_session_missing", current.source+"/"+current.id, nil)
			}
			if section == "inputs" && offset == 0 {
				if trace.done[current] {
					return nil
				}
				ref = current
				trace.done[ref], trace.done[request] = true, true
				execution := map[string]any{"source": ref.source, "id": ref.id, "status": row["status"], "legacy_context": detail["legacy_context"] == true, "status_reason": row["status_reason"]}
				if stamp, ok := monitoringTraceNumber(row["finished_at_ms"]); ok {
					execution["timestamp_ms"] = stamp
				}
				trace.executions = append(trace.executions, execution)
				trace.record("execution", execution, ref, map[string]any{"operation": "monitoring.execution.detail", "input": body})
				if detail["legacy_context"] == true {
					trace.issue("legacy_context", ref.source+"/"+ref.id, nil)
				}
			}
			if current != ref {
				trace.issue("execution_identity_changed", ref.source+"/"+ref.id, nil)
				return nil
			}
			for _, execution := range trace.executions {
				if execution["source"] == ref.source && execution["id"] == ref.id && execution["status"] != row["status"] {
					trace.issue("execution_changed_during_query", ref.source+"/"+ref.id, nil)
					execution["status"], execution["status_reason"] = row["status"], row["status_reason"]
					if stamp, ok := monitoringTraceNumber(row["finished_at_ms"]); ok {
						execution["timestamp_ms"] = stamp
					}
					trace.record("execution", execution, ref, map[string]any{"operation": "monitoring.execution.detail", "input": body})
				}
			}
			pages, ok := detail["pages"].(map[string]any)
			page, pageOK := pages[section].(map[string]any)
			if !ok || !pageOK {
				trace.issue("execution_section_missing", ref.source+"/"+ref.id+"/"+section, nil)
				break
			}
			items, ok := page["items"].([]any)
			more, valid := page["has_more"].(bool)
			next, cursor := monitoringTraceNumber(page["next_offset"])
			if !ok || !valid || more && (!cursor || next <= offset) {
				trace.issue("execution_page_invalid", ref.source+"/"+ref.id+"/"+section, nil)
				break
			}
			progress := false
			for _, raw := range items {
				item, ok := raw.(map[string]any)
				if !ok || monitoringTraceID(item["id"]) == "" {
					trace.issue("execution_record_invalid", ref.source+"/"+ref.id+"/"+section, nil)
					continue
				}
				identity := monitoringTraceID(item["id"])
				if section == "related" {
					identity = monitoringTraceID(item["source"]) + "/" + identity
				}
				progress = progress || !seen[identity]
				seen[identity] = true
				if section == "related" {
					if item["bot_id"] == trace.bot {
						source := monitoringTraceID(item["source"])
						if source == "agent" || source == "pipeline" || source == "event" {
							trace.enqueue(source, monitoringTraceID(item["id"]))
						} else {
							trace.issue("invalid_execution_identity", identity, nil)
						}
					}
					continue
				}
				if bot, exists := item["bot_id"]; exists && bot != trace.bot {
					trace.issue("record_scope_unconfirmed", ref.source+"/"+ref.id+"/"+section, nil)
					continue
				}
				if session, exists := item["session_id"]; exists && session != nil && session != "" && session != trace.chat {
					trace.issue("record_scope_unconfirmed", ref.source+"/"+ref.id+"/"+section, nil)
					continue
				}
				if section == "inputs" {
					source := "event"
					if _, exists := item["role"]; exists {
						source = "pipeline"
					}
					trace.identities[monitoringTraceRef{source, monitoringTraceID(item["id"])}] = true
				}
				stage := section
				if section == "inputs" && item["role"] == "assistant" {
					stage = "deliveries"
					trace.issue("delivery_parent_unconfirmed", ref.source+"/"+ref.id, nil)
				}
				trace.record(stage, item, ref, map[string]any{"operation": "monitoring.execution.detail", "input": body, "record_id": item["id"]})
			}
			if !more {
				if section == "inputs" && offset == 0 && len(items) == 0 {
					trace.issue("input_evidence_missing", ref.source+"/"+ref.id, nil)
				}
				break
			}
			if len(items) == 0 || !progress {
				trace.issue("execution_records_missing", ref.source+"/"+ref.id+"/"+section, nil)
				break
			}
			offset = next
		}
	}
	return nil
}

func (trace *monitoringTrace) record(stage string, item map[string]any, ref monitoringTraceRef, locator map[string]any) {
	id := monitoringTraceID(item["id"])
	if status := item["status"]; status != nil {
		if _, ok := status.(string); !ok {
			trace.issue("execution_record_invalid", stage+"/"+id, nil)
			return
		}
	}
	recordSource := stage
	switch stage {
	case "inputs":
		recordSource = "event"
		if _, exists := item["role"]; exists {
			recordSource = "message_view"
		}
	case "deliveries", "session_context":
		recordSource = "message_view"
	case "execution":
		recordSource = ref.source
	}
	key := stage + "\x00" + id + "\x00" + recordSource
	record := trace.records[key]
	if record == nil {
		record = map[string]any{"stage": stage, "id": id, "record_source": recordSource, "executions": []map[string]any{}, "locator": locator}
		for _, name := range []string{"source", "event_id", "run_id", "message_id", "parent_message_id", "sequence", "type", "role", "status", "error_type", "model_name", "tool_name", "duration", "origin", "binding_id", "target_type", "target_uuid", "failure_code"} {
			switch value := item[name].(type) {
			case string, json.Number, int64:
				record[name] = value
			}
		}
		if stamp, ok := monitoringTraceTime(item); ok {
			record["timestamp_ms"] = stamp
			record["time"] = time.UnixMilli(stamp).UTC().Format(time.RFC3339Nano)
		}
		if stage != "execution" {
			if failure := monitoringTraceFailure(stage, item); failure != "" {
				record["error"] = failure
			}
		}
		trace.records[key] = record
	} else if status, exists := item["status"]; exists && record["status"] != status {
		trace.issue("record_changed_during_query", stage+"/"+id, nil)
		record["status"] = status
		if stamp, ok := monitoringTraceTime(item); ok {
			record["timestamp_ms"] = stamp
			record["time"] = time.UnixMilli(stamp).UTC().Format(time.RFC3339Nano)
		}
		if failure := monitoringTraceFailure(stage, item); stage != "execution" && failure != "" {
			record["error"] = failure
		}
	}
	if ref.id != "" {
		links := record["executions"].([]map[string]any)
		linked := false
		for _, link := range links {
			if link["source"] == ref.source && link["id"] == ref.id {
				linked = true
			}
		}
		if !linked {
			record["executions"] = append(links, map[string]any{"source": ref.source, "id": ref.id})
		}
		record["locator"] = locator
	}
	if stage == "inputs" {
		metadata, _ := item["metadata"].(map[string]any)
		routes, _ := metadata["routes"].([]any)
		for index, raw := range routes {
			route, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			entry := map[string]any{"id": id + "/route/" + strconv.Itoa(index), "status": route["status"], "error_message": route["reason"]}
			for _, field := range []string{"binding_id", "target_type", "target_uuid", "run_id", "failure_code"} {
				entry[field] = route[field]
			}
			if entry["error_message"] == nil || entry["error_message"] == "" {
				entry["error_message"] = route["failure_code"]
			}
			if route["status"] != "failed" {
				delete(entry, "error_message")
			}
			trace.record("routing", entry, ref, map[string]any{"parent": locator, "route_index": index})
		}
	}
}

func monitoringTraceFailure(stage string, item map[string]any) string {
	// A user message's status summarizes its execution. Its timestamp records
	// input receipt, so it cannot locate the failure stage or time.
	if stage == "inputs" {
		return ""
	}
	failed := stage == "errors" || item["status"] == "error" || item["status"] == "failed" || item["status"] == "timeout"
	data, _ := item["data"].(map[string]any)
	if kind, _ := item["type"].(string); strings.HasSuffix(kind, ".failed") || kind == "error" {
		failed = true
	}
	delivery, _ := item["delivery"].(map[string]any)
	if variables, _ := item["variables"].(string); variables != "" && delivery == nil {
		_ = json.Unmarshal([]byte(variables), &delivery)
	}
	if message, _ := delivery["delivery_error"].(string); message != "" {
		return message
	}
	if !failed {
		return ""
	}
	for _, value := range []any{item["error_message"], item["status_reason"], data["error"], data["message"], item["status"], item["type"]} {
		if text, ok := value.(string); ok && text != "" {
			return text
		}
		if object, ok := value.(map[string]any); ok {
			if text, ok := object["message"].(string); ok && text != "" {
				return text
			}
		}
	}
	return stage
}

func (trace *monitoringTrace) issue(reason, source string, err error) {
	for _, issue := range trace.issues {
		if issue["reason"] == reason && issue["source"] == source {
			return
		}
	}
	issue := map[string]any{"reason": reason, "source": source}
	if err != nil {
		issue["error"] = result.AsError(err)
	}
	trace.issues = append(trace.issues, issue)
}

func (trace *monitoringTrace) report() map[string]any {
	evidence := map[monitoringTraceRef]monitoringTraceEvidence{}
	for _, record := range trace.records {
		for _, link := range record["executions"].([]map[string]any) {
			ref := monitoringTraceRef{fmt.Sprint(link["source"]), fmt.Sprint(link["id"])}
			current := evidence[ref]
			current.failed = current.failed || record["error"] != nil
			current.generated = current.generated || record["stage"] == "outputs" && record["role"] == "assistant"
			current.delivered = current.delivered || record["stage"] == "deliveries"
			evidence[ref] = current
		}
	}
	for _, execution := range trace.executions {
		ref := monitoringTraceRef{fmt.Sprint(execution["source"]), fmt.Sprint(execution["id"])}
		current := evidence[ref]
		if current.generated && !current.delivered {
			trace.issue("delivery_evidence_missing", ref.source+"/"+ref.id, nil)
		}
		if !current.failed {
			if failure := monitoringTraceFailure("execution", execution); failure != "" {
				trace.records["execution\x00"+ref.id+"\x00"+ref.source]["error"] = failure
			}
		}
	}
	timeline := make([]map[string]any, 0, len(trace.records))
	for _, record := range trace.records {
		timeline = append(timeline, record)
	}
	sort.Slice(timeline, func(i, j int) bool {
		a, aOK := monitoringTraceNumber(timeline[i]["timestamp_ms"])
		b, bOK := monitoringTraceNumber(timeline[j]["timestamp_ms"])
		if aOK != bOK {
			return aOK
		}
		if a != b {
			return a < b
		}
		return fmt.Sprint(timeline[i]["stage"], "/", timeline[i]["id"], "/", timeline[i]["record_source"]) < fmt.Sprint(timeline[j]["stage"], "/", timeline[j]["id"], "/", timeline[j]["record_source"])
	})
	failures := []map[string]any{}
	unordered := []map[string]any{}
	for _, record := range timeline {
		if record["error"] != nil {
			failures = append(failures, record)
			if _, exists := record["timestamp_ms"]; !exists {
				unordered = append(unordered, record)
			}
		}
	}
	var first any
	ordering := "none"
	if len(failures) > 0 {
		first, ordering = failures[0], "recorded_timestamp"
		if len(unordered) > 0 {
			trace.issue("failure_time_missing", "timeline", nil)
		}
		ambiguous := len(unordered) > 0
		if len(failures) > 1 {
			firstTime, firstOK := monitoringTraceNumber(failures[0]["timestamp_ms"])
			secondTime, secondOK := monitoringTraceNumber(failures[1]["timestamp_ms"])
			ambiguous = ambiguous || firstOK && secondOK && firstTime == secondTime
		}
		if ambiguous {
			first, ordering = nil, "undetermined"
			trace.issue("failure_order_unconfirmed", "timeline", nil)
		}
	}
	sort.Slice(trace.issues, func(i, j int) bool {
		return fmt.Sprint(trace.issues[i]["reason"], trace.issues[i]["source"]) < fmt.Sprint(trace.issues[j]["reason"], trace.issues[j]["source"])
	})
	window := map[string]any{"end_time": trace.end.Format(time.RFC3339Nano)}
	if !trace.start.IsZero() {
		window["start_time"] = trace.start.Format(time.RFC3339Nano)
	}
	return map[string]any{
		"bot_id": trace.bot, "session_id": trace.chat, "window": window,
		"complete": len(trace.issues) == 0, "partial": len(trace.issues) > 0,
		"timeline": timeline, "executions": trace.executions,
		"errors": failures, "unordered_errors": unordered,
		"first_error": first, "error_ordering": ordering, "issues": trace.issues,
	}
}

func monitoringTraceID(value any) string {
	switch item := value.(type) {
	case string:
		return item
	case json.Number:
		return item.String()
	}
	return ""
}

func monitoringTraceNumber(value any) (int64, bool) {
	switch item := value.(type) {
	case json.Number:
		value, err := item.Int64()
		return value, err == nil
	case int64:
		return item, true
	}
	return 0, false
}

func monitoringTraceTime(item map[string]any) (int64, bool) {
	for _, key := range []string{"timestamp_ms", "created_at_ms"} {
		if value, ok := monitoringTraceNumber(item[key]); ok {
			return value, true
		}
	}
	if value, ok := item["timestamp"].(string); ok {
		for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05.999999999", "2006-01-02 15:04:05.999999999"} {
			if stamp, err := time.Parse(layout, value); err == nil {
				return stamp.UnixMilli(), true
			}
		}
	}
	return 0, false
}

func invalidMonitoringTrace() error {
	return result.New("incompatible", i18n.T("Invalid monitoring trace source response"))
}
