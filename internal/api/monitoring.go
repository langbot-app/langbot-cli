package api

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

func resolveMonitoringOperation(path string) (Operation, bool) {
	id := ""
	for _, kind := range []string{"messages", "llm-calls", "tool-calls", "embedding-calls", "sessions", "errors"} {
		if path == "/api/v1/monitoring/"+kind {
			id = "monitoring." + strings.ReplaceAll(kind, "-", "_")
		}
	}
	if nestedPath(path, "/api/v1/monitoring/messages", 1, "details") {
		id = "monitoring.message_details"
	}
	if nestedPath(path, "/api/v1/monitoring/sessions", 1, "analysis") {
		id = "monitoring.session_analysis"
	}
	return Operation{ID: id, Method: http.MethodGet, Path: path, ReadOnly: true, Permission: "resource.view"}, id != ""
}
func validMonitoringQuery(operation string, query url.Values) bool {
	allowed := map[string]bool{"startTime": true, "endTime": true, "limit": true, "offset": true}
	if operation != "monitoring.embedding_calls" {
		allowed["botId"], allowed["pipelineId"] = true, true
	}
	switch operation {
	case "monitoring.messages", "monitoring.tool_calls":
		allowed["sessionId"] = true
	case "monitoring.embedding_calls":
		allowed["knowledgeBaseId"] = true
	case "monitoring.sessions":
		allowed["userQuery"], allowed["isActive"] = true, true
	case "monitoring.session_analysis":
		allowed = map[string]bool{"startTime": true, "endTime": true, "botId": true}
	case "monitoring.message_details":
		return len(query) == 0
	}
	for key, values := range query {
		repeated := key == "botId" || key == "pipelineId" || key == "sessionId"
		if !allowed[key] || (len(values) != 1 && (!repeated || operation == "monitoring.session_analysis")) {
			return false
		}
		for _, value := range values {
			if strings.TrimSpace(value) == "" {
				return false
			}
		}
	}
	for _, key := range []string{"limit", "offset"} {
		if value, ok := query[key]; ok {
			n, err := strconv.Atoi(value[0])
			if err != nil || n < 0 || (key == "limit" && (n < 1 || n > 500)) {
				return false
			}
		}
	}
	if value, ok := query["isActive"]; ok && value[0] != "true" && value[0] != "false" {
		return false
	}
	return true
}
