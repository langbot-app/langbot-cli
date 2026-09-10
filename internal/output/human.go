package output

import (
	"fmt"
	"io"
	"reflect"
	"sort"
	"strings"
	"unicode"
)

func renderHuman(w io.Writer, value any, command string) error {
	envelope, ok := value.(map[string]any)
	if !ok {
		return renderValue(w, value, "")
	}
	if _, exists := envelope["ok"]; !exists {
		return renderValue(w, value, "")
	}
	if envelope["ok"] == false {
		if err := renderHumanError(w, envelope["error"]); err != nil {
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
	if err := renderTarget(w, envelope["meta"]); err != nil {
		return err
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
	return renderHumanData(w, data, command)
}

func renderHumanError(w io.Writer, value any) error {
	detail, ok := value.(map[string]any)
	if !ok {
		_, err := fmt.Fprintln(w, "错误：操作失败")
		return err
	}
	message := humanCell(detail["message"])
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
		if _, err := fmt.Fprintln(w, fieldLabel(field)+"："+humanCell(value)); err != nil {
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
	contextName := humanCell(meta["context"])
	endpoint := humanCell(meta["endpoint"])
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

func renderHumanData(w io.Writer, value any, command string) error {
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
	case "context.check", "status":
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

func isAction(data map[string]any) bool {
	for _, key := range []string{"operation", "step", "task_id", "submitted", "verified", "dry_run"} {
		if _, exists := data[key]; exists {
			return true
		}
	}
	return false
}

func renderAction(w io.Writer, data map[string]any) error {
	keys := []string{"operation", "action", "uuid", "model_uuid", "provider_uuid", "skill_name", "name", "author", "plugin_name", "server_name", "path", "file_id", "task_id", "model_type", "dry_run", "executed", "step", "submitted", "submission_result", "server_write", "verified", "server_cancelled", "delete_data", "business_validation"}
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
		return []string{"context", "endpoint", "reachable", "diagnostic_ok", "identity", "capabilities", "server_version", "error"}
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
			line[i] = humanCell(row[field])
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
				parts[i] = humanCell(item)
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
	_, err := fmt.Fprintln(w, prefix+humanCell(value))
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

func humanCell(value any) string {
	if value == nil || value == "" {
		return "-"
	}
	return formatCell(value)
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
