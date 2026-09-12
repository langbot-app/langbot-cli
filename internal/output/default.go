package output

import (
	"fmt"
	"io"
	"reflect"
	"sort"
	"strings"
	"unicode"
)

func renderDefault(w io.Writer, value any, command string) error {
	envelope, ok := value.(map[string]any)
	if !ok {
		return renderValue(w, value, "")
	}
	if _, exists := envelope["ok"]; !exists {
		return renderValue(w, value, "")
	}
	if envelope["ok"] == false {
		if err := renderDefaultError(w, envelope["error"]); err != nil {
			return err
		}
	}
	data := envelope["data"]
	if command == "version" && envelope["ok"] == true {
		if info, ok := data.(map[string]any); ok {
			if _, server := info["server_version"]; !server {
				_, err := fmt.Fprintln(w, formatCell(info["version"]))
				return err
			}
			if err := renderValue(w, info["version"], "CLI"); err != nil {
				return err
			}
			if err := renderFields(w, info, []string{"server_version", "server_edition"}); err != nil {
				return err
			}
		}
		return renderTarget(w, envelope["meta"])
	}
	if command != "status" && command != "doctor" {
		if err := renderTarget(w, envelope["meta"]); err != nil {
			return err
		}
	}
	if data == nil {
		if envelope["ok"] == false {
			return nil
		}
		_, err := fmt.Fprintln(w, "无数据")
		return err
	}
	if object, ok := data.(map[string]any); ok {
		view := make(map[string]any, len(object))
		meta, _ := envelope["meta"].(map[string]any)
		for key, item := range object {
			if key == "error" && envelope["ok"] == false && reflect.DeepEqual(item, envelope["error"]) {
				continue
			}
			if (key == "context" || key == "endpoint" || key == "workspace_uuid") && item != nil && item != "" && reflect.DeepEqual(item, meta[key]) {
				continue
			}
			view[key] = item
		}
		data = view
	}
	return renderDefaultData(w, data, command)
}

func renderDefaultError(w io.Writer, value any) error {
	detail, ok := value.(map[string]any)
	if !ok {
		_, err := fmt.Fprintln(w, "错误：操作失败")
		return err
	}
	message := displayCell(detail["message"])
	if message == "-" {
		message = "操作失败"
	}
	if _, err := fmt.Fprintln(w, "错误："+message); err != nil {
		return err
	}
	for _, field := range []string{"http_status", "server_code", "request_id"} {
		value, exists := detail[field]
		if !exists || value == nil || value == "" {
			continue
		}
		if field == "http_status" && strings.Contains(message, "HTTP "+formatCell(value)) {
			continue
		}
		if _, err := fmt.Fprintln(w, fieldLabel(field)+"："+displayCell(value)); err != nil {
			return err
		}
	}
	return nil
}

func renderTarget(w io.Writer, value any) error {
	meta, ok := value.(map[string]any)
	if !ok {
		return nil
	}
	contextName := displayCell(meta["context"])
	endpoint := displayCell(meta["endpoint"])
	if contextName == "-" && endpoint == "-" {
		return nil
	}
	target := contextName
	if contextName == "-" {
		target = endpoint
	} else if endpoint != "-" {
		target += " (" + endpoint + ")"
	}
	if meta["temporary"] == true {
		target += "（临时）"
	}
	_, err := fmt.Fprintln(w, "目标："+target)
	return err
}

func renderDefaultData(w io.Writer, value any, command string) error {
	data, ok := value.(map[string]any)
	if !ok {
		return renderValue(w, value, "")
	}
	switch command {
	case "context.show":
		for _, key := range []string{"saved", "effective"} {
			if item, exists := data[key]; exists {
				if err := renderValue(w, item, fieldLabel(key)); err != nil {
					return err
				}
			}
		}
		return nil
	case "status":
		return renderActiveEnvironment(w, data)
	case "context.check":
		if checks, ok := data["checks"].([]any); ok {
			return renderChecks(w, checks)
		}
		if err := renderFields(w, data, []string{"reachable", "diagnostic_ok", "server_version", "server_edition", "capabilities"}); err != nil {
			return err
		}
		if identity, ok := data["identity"].(map[string]any); ok {
			if _, err := fmt.Fprintln(w, "Identity:"); err != nil {
				return err
			}
			if err := renderFields(w, identity, []string{"instance_uuid", "workspace_uuid", "api_key_id"}); err != nil {
				return err
			}
		} else if identity, exists := data["identity"]; exists {
			if err := renderValue(w, identity, "Identity"); err != nil {
				return err
			}
		}
		if item, exists := data["error"]; exists {
			return renderValue(w, item, "Error")
		}
		return nil
	case "doctor":
		if environment, ok := data["environment"].(map[string]any); ok {
			if err := renderActiveEnvironment(w, environment); err != nil {
				return err
			}
		}
		if checks, ok := data["checks"].([]any); ok {
			return renderDoctorChecks(w, checks)
		}
		return renderValue(w, data, "")
	case "logs":
		if lines, ok := data["lines"].([]any); ok {
			for _, line := range lines {
				if _, err := fmt.Fprintln(w, formatCell(line)); err != nil {
					return err
				}
			}
			return nil
		}
	case "install", "adopt", "start", "stop", "restart":
		return renderLocalAction(w, data)
	case "whoami":
		return renderValue(w, data, "")
	case "capabilities":
		if err := renderFields(w, data, []string{"schema_version", "instance_uuid", "workspace_uuid", "api_key_id", "permissions", "capabilities"}); err != nil {
			return err
		}
		if operations, ok := data["operations"].(map[string]any); ok {
			rows := make([]any, 0, len(operations))
			for _, name := range sortedKeys(operations) {
				rows = append(rows, map[string]any{"operation": name, "status": operations[name]})
			}
			return renderResourceList(w, "operations", rows)
		}
		return nil
	}
	// 多步操作保留实际步骤和部分结果，不从退出码推断服务端状态。
	if isAction(data) {
		return renderAction(w, data)
	}
	if command == "model.list" {
		if groups, ok := data["models"].(map[string]any); ok {
			if len(groups) == 0 {
				return renderValue(w, groups, "models")
			}
			for _, kind := range []string{"llm", "embedding", "rerank"} {
				if items, exists := groups[kind]; exists {
					if _, err := fmt.Fprintln(w, kind); err != nil {
						return err
					}
					if rows, ok := items.([]any); ok {
						if err := renderResourceList(w, "models", rows); err != nil {
							return err
						}
					}
				}
			}
			return nil
		}
	}
	if listKey := commandListKey(command); listKey != "" {
		if items, ok := data[listKey].([]any); ok {
			if err := renderFields(w, data, []string{"model_type"}); err != nil {
				return err
			}
			return renderResourceList(w, listKey, items)
		}
	}
	// 文件正文、日志、检索和未专门编排的响应使用完整的安全详情。
	return renderValue(w, data, "")
}

func renderLocalAction(w io.Writer, data map[string]any) error {
	action, _ := data["action"].(string)
	record, _ := data["record"].(map[string]any)
	status, _ := data["status"].(map[string]any)
	version, _ := data["version"].(string)
	if version == "" {
		version, _ = record["core_version"].(string)
	}
	endpoint, _ := data["endpoint"].(string)
	if endpoint == "" {
		endpoint, _ = record["endpoint"].(string)
	}
	state, _ := status["status"].(string)
	line := ""
	switch action {
	case "install":
		if data["dry_run"] == true {
			line = "安装计划：LangBot " + version + " → " + endpoint
		} else if data["changed"] != true {
			line = "安装未执行：前置检查未通过"
		} else if state != "running" || data["step"] != nil {
			line = "安装已执行，运行状态待确认：" + endpoint
		} else {
			line = "已安装 LangBot " + version + "：" + endpoint
		}
	case "adopt":
		line = "已接管本机部署：" + endpoint
		if data["changed"] == false {
			line = "本机部署已接管：" + endpoint
		}
	case "start", "stop", "restart":
		line = map[string]string{"start": "已启动本机部署", "stop": "已停止本机部署", "restart": "已重启本机部署"}[action]
		if data["changed"] == false {
			line = map[string]string{"start": "本机部署已在运行", "stop": "本机部署已停止", "restart": "本机部署已重启"}[action]
		} else if state != map[string]string{"start": "running", "stop": "stopped", "restart": "running"}[action] {
			line = "命令已执行，本机部署状态待确认"
		}
	}
	expected := map[string]string{"install": "running", "start": "running", "stop": "stopped", "restart": "running"}[action]
	if state != "" && state != expected && data["changed"] != false {
		label := map[string]string{"running": "运行中", "stopped": "已停止", "starting": "启动中", "degraded": "部分服务异常", "unknown": "状态未知"}[state]
		if label == "" {
			label = state
		}
		line += "（" + label + "）"
	}
	if line != "" {
		if _, err := fmt.Fprintln(w, line); err != nil {
			return err
		}
	}
	if data["dry_run"] == true || (action == "install" && data["changed"] != true) {
		if dir, ok := data["dir"].(string); ok && dir != "" {
			if _, err := fmt.Fprintln(w, "部署目录："+dir); err != nil {
				return err
			}
		}
		if checks, ok := data["checks"].([]any); ok {
			failed := 0
			for _, item := range checks {
				check, _ := item.(map[string]any)
				if check["status"] == "error" {
					failed++
					if _, err := fmt.Fprintf(w, "未通过：%s（%s）\n", displayCell(check["name"]), displayCell(check["reason"])); err != nil {
						return err
					}
				}
			}
			if failed == 0 {
				_, err := fmt.Fprintf(w, "预检：%d 项通过\n", len(checks))
				return err
			}
		}
	}
	return nil
}

func renderActiveEnvironment(w io.Writer, environment map[string]any) error {
	line := func(label string, value any) error {
		if value == nil || value == "" {
			return nil
		}
		_, err := fmt.Fprintf(w, "%s: %s\n", label, displayCell(value))
		return err
	}
	if err := line("Active Environment", environment["name"]); err != nil {
		return err
	}
	if err := line("Type", environmentTypeLabel(formatCell(environment["type"]))); err != nil {
		return err
	}
	connection, _ := environment["connection"].(map[string]any)
	if err := line("Endpoint", connection["endpoint"]); err != nil {
		return err
	}
	connectionLabel := "Unreachable"
	if formatCell(environment["discovery"]) == "unconfigured" {
		connectionLabel = "Not configured"
	} else if connection["reachable"] == true {
		connectionLabel = "Connected"
	}
	if err := line("Connection", connectionLabel); err != nil {
		return err
	}
	if err := line("Authentication", discoveryStateLabel(formatCell(connection["authentication"]))); err != nil {
		return err
	}
	service, _ := environment["service"].(map[string]any)
	core := formatCell(service["version"])
	if edition := formatCell(service["edition"]); core != "" && edition != "" {
		core += " (" + edition + ")"
	}
	if err := line("Core", core); err != nil {
		return err
	}
	identity, _ := environment["identity"].(map[string]any)
	if err := line("Workspace", identity["workspace_uuid"]); err != nil {
		return err
	}
	if err := line("Instance", identity["instance_uuid"]); err != nil {
		return err
	}
	capabilities, _ := environment["capabilities"].(map[string]any)
	if err := line("Capabilities", discoveryStateLabel(formatCell(capabilities["status"]))); err != nil {
		return err
	}
	runtimeInfo, _ := environment["runtime"].(map[string]any)
	if err := line("Lifecycle", lifecycleLabel(runtimeInfo)); err != nil {
		return err
	}
	if binding := formatCell(runtimeInfo["binding"]); binding != "" && binding != "none" && binding != "verified" && binding != "missing" {
		if err := line("Binding", discoveryStateLabel(binding)); err != nil {
			return err
		}
	}
	return line("Directory", runtimeInfo["dir"])
}

func environmentTypeLabel(value string) string {
	switch value {
	case "cloud":
		return "LangBot Cloud"
	case "self_hosted":
		return "Self-hosted"
	default:
		return "Unknown"
	}
}

func discoveryStateLabel(value string) string {
	switch value {
	case "verified", "complete":
		return "Verified"
	case "partial":
		return "Partial"
	case "not_checked":
		return "Not checked"
	case "failed":
		return "Failed"
	case "mismatch":
		return "Mismatch"
	case "corrupt":
		return "Corrupt"
	case "unreadable":
		return "Unreadable"
	case "unavailable":
		return "Unavailable"
	default:
		return "Unknown"
	}
}

func lifecycleLabel(info map[string]any) string {
	switch formatCell(info["management"]) {
	case "cloud_managed":
		return "Managed by LangBot Cloud"
	case "local_managed":
		driver := formatCell(info["driver"])
		if driver == "docker_compose" {
			driver = "Docker Compose"
		} else if driver == "native_process" {
			driver = "Native process"
		}
		if status := formatCell(info["status"]); status != "" {
			return driver + " · " + localStatusLabel(status)
		}
		return driver + " · Managed"
	case "unbound":
		return "Not bound"
	default:
		return "Unavailable"
	}
}

func isAction(data map[string]any) bool {
	for _, key := range []string{"operation", "action", "step", "task_id", "submitted", "verified", "dry_run"} {
		if _, exists := data[key]; exists {
			return true
		}
	}
	return false
}

func renderAction(w io.Writer, data map[string]any) error {
	keys := []string{"operation", "action", "changed", "version", "profile", "endpoint", "dir", "image", "uuid", "model_uuid", "provider_uuid", "skill_name", "name", "author", "plugin_name", "server_name", "path", "file_id", "task_id", "model_type", "dry_run", "executed", "step", "submitted", "submission_result", "server_write", "verified", "server_cancelled", "delete_data", "business_validation"}
	if err := renderFields(w, data, keys); err != nil {
		return err
	}
	handled := make(map[string]bool, len(keys)+2)
	for _, key := range keys {
		handled[key] = true
	}
	handled["preconditions_confirmed"], handled["preflight_confirmed"] = true, true
	for _, key := range sortedKeys(data) {
		if handled[key] {
			continue
		}
		item := data[key]
		// 回读对象只展示标识和名称；执行结果、安装清单和任务数据保留详情。
		switch key {
		case "bot", "pipeline", "base", "knowledge_base", "provider", "model", "server", "plugin", "skill":
			if resource, ok := item.(map[string]any); ok {
				if _, err := fmt.Fprintln(w, fieldLabel(key)+":"); err != nil {
					return err
				}
				if err := renderFields(w, resource, []string{"uuid", "author", "name"}); err != nil {
					return err
				}
				continue
			}
		}
		if err := renderValue(w, item, fieldLabel(key)); err != nil {
			return err
		}
	}
	return nil
}

func renderChecks(w io.Writer, checks []any) error {
	rows := make([]any, 0, len(checks))
	for _, item := range checks {
		check, ok := item.(map[string]any)
		if !ok {
			return renderValue(w, checks, "checks")
		}
		row := make(map[string]any)
		for _, key := range []string{"context", "endpoint", "reachable", "diagnostic_ok", "capabilities", "server_version"} {
			if value, exists := check[key]; exists {
				row[key] = value
			}
		}
		row["identity"] = check["identity"]
		if identity, ok := check["identity"].(map[string]any); ok {
			row["identity"] = identity["workspace_uuid"]
		}
		if e, ok := check["error"].(map[string]any); ok {
			row["error"] = formatCell(e["type"]) + ": " + formatCell(e["message"])
		}
		rows = append(rows, row)
	}
	if err := renderResourceList(w, "checks", rows); err != nil {
		return err
	}
	for _, item := range checks {
		check := item.(map[string]any)
		if e, ok := check["error"].(map[string]any); ok {
			if err := renderValue(w, e, "Error ["+formatCell(check["context"])+"]"); err != nil {
				return err
			}
		}
	}
	return nil
}

func renderDoctorChecks(w io.Writer, checks []any) error {
	rows := make([]any, 0, len(checks))
	for _, item := range checks {
		check, ok := item.(map[string]any)
		if !ok {
			continue
		}
		rows = append(rows, map[string]any{
			"name": check["name"], "status": check["status"], "reason": check["reason"],
		})
	}
	return renderResourceList(w, "checks", rows)
}

func commandListKey(command string) string {
	switch command {
	case "context.list":
		return "contexts"
	case "bot.list":
		return "bots"
	case "pipeline.list":
		return "pipelines"
	case "provider.list":
		return "providers"
	case "model.list":
		return "models"
	case "knowledge-base.list":
		return "bases"
	case "knowledge-base.file.list":
		return "files"
	case "mcp-server.list":
		return "servers"
	case "plugin.list":
		return "plugins"
	case "skill.list":
		return "skills"
	case "task.list":
		return "tasks"
	}
	return ""
}

func resourceFields(key string) []string {
	switch key {
	case "contexts":
		return []string{"current", "name", "endpoint", "expected_workspace_uuid"}
	case "checks":
		return []string{"name", "status", "reason", "context", "endpoint", "reachable", "diagnostic_ok", "identity", "capabilities", "server_version", "error"}
	case "containers":
		return []string{"service", "name", "state", "health"}
	case "bots":
		return []string{"uuid", "name", "adapter", "enable", "use_pipeline_uuid"}
	case "pipelines":
		return []string{"uuid", "name", "is_default", "for_version"}
	case "providers":
		return []string{"uuid", "name", "requester", "llm_count", "embedding_count", "rerank_count"}
	case "models":
		return []string{"uuid", "name", "provider_uuid", "context_length"}
	case "tasks":
		return []string{"id", "task_type", "kind", "status", "created_at"}
	case "bases":
		return []string{"uuid", "name", "knowledge_engine_plugin_id"}
	case "files":
		return []string{"uuid", "file_name", "extension", "status"}
	case "servers":
		return []string{"uuid", "name", "enable", "mode"}
	case "plugins":
		return []string{"author", "name", "version", "enabled", "status"}
	case "skills":
		return []string{"name", "display_name", "description"}
	case "operations":
		return []string{"operation", "status"}
	}
	return nil
}

func renderResourceList(w io.Writer, key string, items []any) error {
	if _, err := fmt.Fprintln(w, fieldLabel(key)+":"); err != nil {
		return err
	}
	if len(items) == 0 {
		_, err := fmt.Fprintln(w, "无记录")
		return err
	}
	fields := presentResourceFields(resourceFields(key), items)
	if len(fields) == 0 {
		return renderValue(w, items, "")
	}
	cells := make([][]string, 1, len(items)+1)
	for _, field := range fields {
		cells[0] = append(cells[0], strings.ToUpper(fieldLabel(field)))
	}
	for _, item := range items {
		row, ok := item.(map[string]any)
		if !ok {
			return renderValue(w, items, "")
		}
		line := make([]string, len(fields))
		for i, field := range fields {
			line[i] = displayCell(row[field])
			if key == "contexts" && field == "current" {
				line[i] = "-"
				if row[field] == true {
					line[i] = "*"
				}
			}
		}
		cells = append(cells, line)
	}
	return renderCells(w, cells)
}

func presentResourceFields(fields []string, items []any) []string {
	selected := make([]string, 0, len(fields))
	for _, field := range fields {
		for _, item := range items {
			row, ok := item.(map[string]any)
			if !ok {
				continue
			}
			if _, exists := row[field]; exists {
				selected = append(selected, field)
				break
			}
		}
	}
	return selected
}

func renderFields(w io.Writer, data map[string]any, fields []string) error {
	for _, field := range fields {
		if value, exists := data[field]; exists {
			if err := renderValue(w, value, fieldLabel(field)); err != nil {
				return err
			}
		}
	}
	return nil
}

func renderValue(w io.Writer, value any, label string) error {
	return renderValueAt(w, value, label, "")
}

func renderValueAt(w io.Writer, value any, label, indent string) error {
	prefix := indent
	if label != "" {
		prefix += label + ": "
	}
	switch v := value.(type) {
	case map[string]any:
		if len(v) == 0 {
			_, err := fmt.Fprintln(w, prefix+"{}")
			return err
		}
		if label != "" {
			if _, err := fmt.Fprintln(w, indent+label+":"); err != nil {
				return err
			}
		}
		for _, key := range sortedKeys(v) {
			if err := renderValueAt(w, v[key], fieldLabel(key), childIndent(indent, label)); err != nil {
				return err
			}
		}
		return nil
	case []any:
		if len(v) == 0 {
			_, err := fmt.Fprintln(w, prefix+"无记录")
			return err
		}
		scalar := true
		for _, item := range v {
			switch item.(type) {
			case map[string]any, []any:
				scalar = false
			}
		}
		if scalar {
			parts := make([]string, len(v))
			for i, item := range v {
				parts[i] = displayCell(item)
			}
			_, err := fmt.Fprintln(w, prefix+strings.Join(parts, ", "))
			return err
		}
		if label != "" {
			if _, err := fmt.Fprintln(w, indent+label+":"); err != nil {
				return err
			}
		}
		for i, item := range v {
			if err := renderValueAt(w, item, fmt.Sprintf("[%d]", i+1), childIndent(indent, label)); err != nil {
				return err
			}
		}
		return nil
	case string:
		// 正文保留换行，其他控制字符仍按终端安全规则转义。
		if strings.Contains(v, "\n") {
			if label != "" {
				if _, err := fmt.Fprintln(w, indent+label+":"); err != nil {
					return err
				}
			}
			for _, line := range strings.Split(strings.TrimSuffix(v, "\n"), "\n") {
				if _, err := fmt.Fprintln(w, indent+"  "+formatCell(line)); err != nil {
					return err
				}
			}
			return nil
		}
	}
	_, err := fmt.Fprintln(w, prefix+displayCell(value))
	return err
}

func childIndent(indent, label string) string {
	if label != "" {
		return indent + "  "
	}
	return indent
}

func sortedKeys(value map[string]any) []string {
	keys := make([]string, 0, len(value))
	for key := range value {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func displayCell(value any) string {
	if value == nil || value == "" {
		return "-"
	}
	return formatCell(value)
}

func localStatusLabel(value string) string {
	switch value {
	case "not_installed":
		return "Not installed"
	case "stopped":
		return "Stopped"
	case "starting":
		return "Starting"
	case "running":
		return "Running"
	case "degraded":
		return "Degraded"
	case "unknown":
		return "Unknown"
	default:
		return value
	}
}

func fieldLabel(field string) string {
	switch field {
	case "uuid", "id":
		return "ID"
	case "context":
		return "Context"
	case "workspace_uuid":
		return "Workspace"
	case "expected_workspace_uuid":
		return "Expected Workspace"
	case "instance_uuid":
		return "Instance"
	case "api_key_id":
		return "API Key ID"
	case "credential_source":
		return "Credential Source"
	case "dir":
		return "Directory"
	case "server_version":
		return "Server Version"
	case "server_edition":
		return "Edition"
	case "http_status":
		return "HTTP 状态"
	case "server_code":
		return "服务端错误码"
	case "request_id":
		return "请求 ID"
	case "enable", "enabled":
		return "Enabled"
	}
	words := strings.Split(formatCell(field), "_")
	for i, word := range words {
		if len(word) > 0 {
			runes := []rune(word)
			runes[0] = unicode.ToUpper(runes[0])
			words[i] = string(runes)
		}
	}
	return strings.Join(words, " ")
}

func renderCells(w io.Writer, rows [][]string) error {
	widths := make([]int, len(rows[0]))
	for _, row := range rows {
		for i, cell := range row {
			if width := cellWidth(cell); width > widths[i] {
				widths[i] = width
			}
		}
	}
	for _, row := range rows {
		for i, cell := range row {
			if _, err := io.WriteString(w, cell); err != nil {
				return err
			}
			if i < len(row)-1 {
				if _, err := io.WriteString(w, strings.Repeat(" ", widths[i]-cellWidth(cell)+2)); err != nil {
					return err
				}
			}
		}
		if _, err := io.WriteString(w, "\n"); err != nil {
			return err
		}
	}
	return nil
}

func cellWidth(value string) int {
	width := 0
	for _, r := range value {
		switch {
		case unicode.Is(unicode.Mn, r), unicode.Is(unicode.Me, r):
		case unicode.In(r, unicode.Han, unicode.Hangul, unicode.Hiragana, unicode.Katakana), r >= 0xff01 && r <= 0xff60:
			width += 2
		default:
			width++
		}
	}
	return width
}
