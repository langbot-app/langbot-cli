package api

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// Operation 是 CLI 可通过 api 子命令调用的受控 HTTP 操作。
type Operation struct {
	ID         string
	Method     string
	Path       string
	ReadOnly   bool
	Permission string
}

// ResolveOperation 只接受已登记的路径，避免 api 子命令退化为任意 HTTP 代理。
func ResolveOperation(method, requestPath string) (Operation, bool) {
	parsed, err := url.Parse(requestPath)
	escapedPath := strings.ToLower(parsed.EscapedPath())
	if err != nil || parsed.IsAbs() || parsed.Host != "" || parsed.Fragment != "" ||
		strings.Contains(escapedPath, "%2f") || strings.Contains(escapedPath, "%5c") {
		return Operation{}, false
	}
	path := parsed.Path
	if !strings.HasPrefix(path, "/api/v1/") || strings.Contains(path, "//") || strings.Contains(path, "\\") ||
		strings.IndexFunc(path, func(r rune) bool { return r < 0x20 || r == 0x7f }) >= 0 {
		return Operation{}, false
	}
	method = strings.ToUpper(strings.TrimSpace(method))
	var operation Operation
	var ok bool
	if method == http.MethodGet {
		operation, ok = resolveGetOperation(path)
	} else if method == http.MethodPost {
		operation, ok = resolvePostOperation(path)
	} else {
		return Operation{}, false
	}
	query, queryErr := url.ParseQuery(parsed.RawQuery)
	if !ok || queryErr != nil || !validOperationQuery(operation.ID, query) {
		return Operation{}, false
	}
	operation.Path = requestPath
	return operation, true
}

func validOperationQuery(operation string, query url.Values) bool {
	allowed := map[string]bool{}
	switch operation {
	case "task.list":
		allowed = map[string]bool{"type": true, "kind": true}
	case "plugin.logs", "mcp_server.logs":
		allowed = map[string]bool{"level": true, "limit": true}
	case "skill.files.list":
		allowed = map[string]bool{"path": true, "include_hidden": true}
	default:
		if strings.HasPrefix(operation, "monitoring.") {
			return validMonitoringQuery(operation, query)
		}
		return len(query) == 0
	}
	for key, values := range query {
		if !allowed[key] || len(values) != 1 {
			return false
		}
	}
	if values, ok := query["limit"]; ok {
		limit, err := strconv.Atoi(values[0])
		if err != nil || limit < 1 || limit > 500 {
			return false
		}
	}
	if values, ok := query["include_hidden"]; ok && values[0] != "true" && values[0] != "false" {
		return false
	}
	if values, ok := query["path"]; ok {
		if _, err := safeFilePath(values[0]); err != nil {
			return false
		}
	}
	return true
}

func resolveGetOperation(path string) (Operation, bool) {
	if op, ok := resolveMonitoringOperation(path); ok {
		return op, true
	}
	switch path {
	case tasksPath:
		return Operation{ID: "task.list", Method: http.MethodGet, Path: path, ReadOnly: true, Permission: "resource.view"}, true
	case pluginsPath:
		return Operation{ID: "plugin.list", Method: http.MethodGet, Path: path, ReadOnly: true, Permission: "resource.view"}, true
	case skillsPath:
		return Operation{ID: "skill.list", Method: http.MethodGet, Path: path, ReadOnly: true, Permission: "resource.view"}, true
	case mcpServersPath:
		return Operation{ID: "mcp_server.list", Method: http.MethodGet, Path: path, ReadOnly: true, Permission: "resource.view"}, true
	case knowledgeBasesPath:
		return Operation{ID: "knowledge_base.list", Method: http.MethodGet, Path: path, ReadOnly: true, Permission: "resource.view"}, true
	}
	if identifierPath(path, tasksPath) {
		return Operation{ID: "task.get", Method: http.MethodGet, Path: path, ReadOnly: true, Permission: "resource.view"}, true
	}
	if identifierPath(path, knowledgeBasesPath) {
		return Operation{ID: "knowledge_base.get", Method: http.MethodGet, Path: path, ReadOnly: true, Permission: "resource.view"}, true
	}
	if nestedPath(path, knowledgeBasesPath, 1, "files") {
		return Operation{ID: "knowledge_base.file.list", Method: http.MethodGet, Path: path, ReadOnly: true, Permission: "resource.view"}, true
	}
	if multiSegmentPath(path, pluginsPath, 2) {
		return Operation{ID: "plugin.get", Method: http.MethodGet, Path: path, ReadOnly: true, Permission: "resource.view"}, true
	}
	if nestedPath(path, pluginsPath, 2, "config") {
		return Operation{ID: "plugin.config.get", Method: http.MethodGet, Path: path, ReadOnly: true, Permission: "resource.view"}, true
	}
	if nestedPath(path, pluginsPath, 2, "logs") {
		return Operation{ID: "plugin.logs", Method: http.MethodGet, Path: path, ReadOnly: true, Permission: "audit.view"}, true
	}
	if identifierPath(path, skillsPath) {
		return Operation{ID: "skill.get", Method: http.MethodGet, Path: path, ReadOnly: true, Permission: "resource.view"}, true
	}
	if nestedPath(path, skillsPath, 1, "files") {
		return Operation{ID: "skill.files.list", Method: http.MethodGet, Path: path, ReadOnly: true, Permission: "resource.view"}, true
	}
	if skillFilePath(path) {
		return Operation{ID: "skill.files.read", Method: http.MethodGet, Path: path, ReadOnly: true, Permission: "resource.view"}, true
	}
	if nestedPath(path, skillsPath, 1, "preview") {
		return Operation{ID: "skill.preview", Method: http.MethodGet, Path: path, ReadOnly: true, Permission: "resource.view"}, true
	}
	if nestedPath(path, mcpServersPath, 1, "resources") {
		return Operation{ID: "mcp_server.resources", Method: http.MethodGet, Path: path, ReadOnly: true, Permission: "resource.view"}, true
	}
	if nestedPath(path, mcpServersPath, 1, "resource-templates") {
		return Operation{ID: "mcp_server.resource_templates", Method: http.MethodGet, Path: path, ReadOnly: true, Permission: "resource.view"}, true
	}
	if nestedPath(path, mcpServersPath, 1, "logs") {
		return Operation{ID: "mcp_server.logs", Method: http.MethodGet, Path: path, ReadOnly: true, Permission: "audit.view"}, true
	}
	if identifierPath(path, mcpServersPath) {
		return Operation{ID: "mcp_server.get", Method: http.MethodGet, Path: path, ReadOnly: true, Permission: "resource.view"}, true
	}
	return Operation{}, false
}

func resolvePostOperation(path string) (Operation, bool) {
	if nestedPath(path, knowledgeBasesPath, 1, "retrieve") {
		return Operation{ID: "knowledge_base.retrieve", Method: http.MethodPost, Path: path, ReadOnly: true, Permission: "resource.view"}, true
	}
	if nestedPathParts(path, mcpServersPath, 1, []string{"resources", "read"}) {
		return Operation{ID: "mcp_server.resource_read", Method: http.MethodPost, Path: path, ReadOnly: true, Permission: "resource.view"}, true
	}
	return Operation{}, false
}

func identifierPath(path, base string) bool {
	value := strings.TrimPrefix(path, base+"/")
	return value != path && safeSegments(value, 1, false)
}

func multiSegmentPath(path, base string, segments int) bool {
	value := strings.TrimPrefix(path, base+"/")
	if value == path {
		return false
	}
	return safeSegments(value, segments, false)
}

func skillFilePath(path string) bool {
	value := strings.TrimPrefix(path, skillsPath+"/")
	if value == path {
		return false
	}
	parts := strings.Split(value, "/")
	if len(parts) < 3 || parts[1] != "files" || !safeSegments(value, 0, true) {
		return false
	}
	return true
}

func nestedPath(path, base string, segments int, suffix string) bool {
	value := strings.TrimPrefix(path, base+"/")
	if value == path {
		return false
	}
	parts := strings.Split(value, "/")
	return len(parts) == segments+1 && parts[len(parts)-1] == suffix && safeSegments(value, segments+1, false)
}

func nestedPathParts(path, base string, segments int, suffix []string) bool {
	value := strings.TrimPrefix(path, base+"/")
	if value == path {
		return false
	}
	parts := strings.Split(value, "/")
	if len(parts) != segments+len(suffix) || !safeSegments(value, segments+len(suffix), false) {
		return false
	}
	for index, expected := range suffix {
		if parts[segments+index] != expected {
			return false
		}
	}
	return true
}

func safeSegments(value string, count int, allowAnyCount bool) bool {
	parts := strings.Split(value, "/")
	if (!allowAnyCount && len(parts) != count) || (allowAnyCount && len(parts) == 0) {
		return false
	}
	for _, part := range parts {
		if part == "" || part == "." || part == ".." || strings.Contains(part, "\\") ||
			strings.IndexFunc(part, func(r rune) bool { return r < 0x20 || r == 0x7f }) >= 0 {
			return false
		}
	}
	return true
}
