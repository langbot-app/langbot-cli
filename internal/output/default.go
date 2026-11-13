package output

import (
	"fmt"
	"io"
	"os"
	"reflect"
	"runtime"
	"sort"
	"strconv"
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
	if meta, ok := envelope["meta"].(map[string]any); ok {
		if path, ok := meta["config_file"].(string); ok && path != "" {
			if _, err := fmt.Fprintln(w, "配置文件："+displayCell(path)); err != nil {
				return err
			}
		}
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
		if envelope["ok"] == false && isAction(view) {
			view["outcome_error"] = true
		}
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
	if message == "服务端未明确支持该操作" || message == "服务端明确不支持该操作" {
		_, err := fmt.Fprintln(w, "建议：对同一目标运行 lbctl capabilities，确认 Core 是否声明了所需能力。")
		return err
	}
	if guidance, ok := detail["guidance"].(map[string]any); ok {
		if steps, ok := guidance["steps"].([]any); ok {
			for i, step := range steps {
				if _, err := fmt.Fprintf(w, "建议 %d：%s\n", i+1, formatCell(step)); err != nil {
					return err
				}
			}
		}
		if examples, ok := guidance["examples"].(map[string]any); ok {
			for _, shell := range []string{"powershell", "bash"} {
				if example, exists := examples[shell]; exists {
					if _, err := fmt.Fprintf(w, "%s：%s\n", shell, formatCell(example)); err != nil {
						return err
					}
				}
			}
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
	case "plugin.permissions.get":
		if err := renderFields(w, data, []string{"author", "plugin_name", "enabled", "pending_confirmation", "compatibility_mode", "can_confirm"}); err != nil {
			return err
		}
		if permissions, ok := data["permissions"].([]any); ok {
			if err := renderResourceList(w, "permissions", permissions); err != nil {
				return err
			}
		}
		if approval, exists := data["approval"]; exists {
			return renderValue(w, approval, "审批")
		}
		return nil
	case "context.add", "context.update", "context.use":
		verb := map[string]string{"context.add": "已添加环境", "context.update": "已更新环境", "context.use": "已切换到环境"}[command]
		if _, err := fmt.Fprintf(w, "%s %s（%s）\n", verb, displayCell(data["name"]), displayCell(data["endpoint"])); err != nil {
			return err
		}
		return renderFields(w, data, []string{"credential_source", "expected_workspace_uuid"})
	case "context.remove":
		_, err := fmt.Fprintln(w, "已移除环境 "+displayCell(data["removed"]))
		return err
	case "context.list":
		bound := map[string]bool{}
		if items, ok := data["contexts"].([]any); ok {
			for _, item := range items {
				if row, ok := item.(map[string]any); ok {
					if id, ok := row["deployment_id"].(string); ok && id != "" {
						bound[id] = true
					}
				}
			}
			if err := renderResourceList(w, "contexts", items); err != nil {
				return err
			}
		}
		if items, ok := data["deployments"].([]any); ok {
			unconfigured := []any{}
			for _, item := range items {
				row, ok := item.(map[string]any)
				if !ok || !bound[formatCell(row["deployment_id"])] {
					unconfigured = append(unconfigured, item)
				}
			}
			if len(unconfigured) > 0 {
				if err := renderResourceList(w, "unconfigured_deployments", unconfigured); err != nil {
					return err
				}
			}
		}
		if items, ok := data["discovered_deployments"].([]any); ok && len(items) > 0 {
			if err := renderResourceList(w, "discovered_deployments", items); err != nil {
				return err
			}
		}
		return renderFields(w, data, []string{"skipped_projects", "local_error"})
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
		if err := renderActiveEnvironment(w, data); err != nil {
			return err
		}
		_, err := fmt.Fprintln(w, "完整信息：在当前命令后加 -o json 或 -o yaml")
		return err
	case "context.check":
		if checks, ok := data["checks"].([]any); ok {
			return renderChecks(w, checks)
		}
		if err := renderFields(w, data, []string{"reachable", "diagnostic_ok", "server_version", "server_edition", "capabilities"}); err != nil {
			return err
		}
		if identity, ok := data["identity"].(map[string]any); ok {
			if _, err := fmt.Fprintln(w, "身份:"); err != nil {
				return err
			}
			if err := renderFields(w, identity, []string{"instance_uuid", "workspace_uuid", "api_key_id"}); err != nil {
				return err
			}
		} else if identity, exists := data["identity"]; exists {
			if err := renderValue(w, identity, "身份"); err != nil {
				return err
			}
		}
		if item, exists := data["error"]; exists {
			return renderValue(w, item, "错误")
		}
		return nil
	case "doctor":
		if environment, ok := data["environment"].(map[string]any); ok {
			if err := renderActiveEnvironment(w, environment); err != nil {
				return err
			}
		}
		if checks, ok := data["checks"].([]any); ok {
			if err := renderDoctorChecks(w, checks); err != nil {
				return err
			}
			_, err := fmt.Fprintln(w, "完整信息：在当前命令后加 -o json 或 -o yaml")
			return err
		}
		return renderValue(w, data, "")
	case "knowledge-base.ingest":
		if data["batch"] == true {
			return renderIngestBatch(w, data)
		}
	case "logs":
		if lines, ok := data["lines"].([]any); ok {
			for _, line := range lines {
				if _, err := fmt.Fprintln(w, formatCell(line)); err != nil {
					return err
				}
			}
			return nil
		}
	case "install", "adopt", "start", "stop", "restart", "upgrade", "uninstall":
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

func renderIngestBatch(w io.Writer, data map[string]any) error {
	summary, _ := data["summary"].(map[string]any)
	if data["dry_run"] == true {
		outcome := "批量预检通过"
		if data["outcome_error"] == true {
			outcome = "批量预检未完成"
		}
		if _, err := fmt.Fprintf(w, "%s，共 %s 个文件；未上传，未执行服务端业务校验。\n", outcome, formatCell(summary["total"])); err != nil {
			return err
		}
	} else {
		if _, err := fmt.Fprintf(w, "共 %s 个文件：已完成 %s，已提交 %s，失败 %s，结果未知 %s，等待未完成 %s，未开始 %s。\n",
			formatCell(summary["total"]), formatCell(summary["completed"]), formatCell(summary["submitted"]), formatCell(summary["failed"]), formatCell(summary["unknown"]), formatCell(summary["pending"]), formatCell(summary["not_started"])); err != nil {
			return err
		}
	}
	states := map[string]string{"planned": "待上传", "submitted": "已提交", "completed": "已完成", "failed": "失败", "unknown": "结果未知", "pending": "等待未完成", "not_started": "未开始"}
	items, _ := data["files"].([]any)
	for _, item := range items {
		row, _ := item.(map[string]any)
		if _, err := fmt.Fprintf(w, "%s  %s", states[formatCell(row["status"])], displayCell(row["path"])); err != nil {
			return err
		}
		if id, ok := row["task_id"]; ok {
			if _, err := fmt.Fprintf(w, "  任务 %s", displayCell(id)); err != nil {
				return err
			}
		}
		if detail, ok := row["error"].(map[string]any); ok {
			if _, err := fmt.Fprintf(w, "  %s", displayCell(detail["message"])); err != nil {
				return err
			}
		}
		if _, err := fmt.Fprintln(w); err != nil {
			return err
		}
	}
	return nil
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
	case "upgrade":
		from, _ := data["source_version"].(string)
		to, _ := data["target_version"].(string)
		if data["dry_run"] == true {
			line = "升级计划：LangBot " + from + " → " + to
		} else if data["completed"] == true {
			line = "已升级 LangBot " + from + " → " + to
		} else {
			line = "升级未完成；请运行 lbctl status 和 lbctl doctor 检查恢复现场"
		}
	case "uninstall":
		if data["project_removed"] == true {
			line = "已卸载本机受管部署；数据和部署记录已保留"
			if data["purge_data"] == true {
				line = "已卸载本机受管部署并清理数据；备份和部署记录已保留"
			}
		} else {
			line = "卸载未完成；请运行 lbctl status 和 lbctl doctor 检查"
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
		if action == "install" {
			if err := renderFields(w, data, []string{"compose_project", "plugin_debug_port", "adapter_ports"}); err != nil {
				return err
			}
		}
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
	name := formatCell(environment["name"])
	if environment["discovery"] == "unconfigured" {
		name = "未配置"
	}
	if err := line("当前环境", name+"（"+environmentTypeLabel(formatCell(environment["type"]))+"）"); err != nil {
		return err
	}
	connection, _ := environment["connection"].(map[string]any)
	if err := line("地址", connection["endpoint"]); err != nil {
		return err
	}
	connectionLabel := "不可达"
	if formatCell(environment["discovery"]) == "unconfigured" {
		connectionLabel = "未配置"
	} else if connection["reachable"] == true {
		connectionLabel = "已连接"
	}
	if err := line("连接", connectionLabel); err != nil {
		return err
	}
	if err := line("认证", discoveryStateLabel(formatCell(connection["authentication"]))); err != nil {
		return err
	}
	service, _ := environment["service"].(map[string]any)
	core := formatCell(service["version"])
	if edition := formatCell(service["edition"]); core != "" && edition != "" && environment["type"] == "unknown" {
		core += " (" + edition + ")"
	}
	if err := line("Core", core); err != nil {
		return err
	}
	identity, _ := environment["identity"].(map[string]any)
	if state := formatCell(identity["status"]); state == "mismatch" || state == "failed" {
		if err := line("Workspace 身份", discoveryStateLabel(state)); err != nil {
			return err
		}
		if err := line("实际 Workspace", identity["workspace_uuid"]); err != nil {
			return err
		}
	}
	capabilities, _ := environment["capabilities"].(map[string]any)
	capabilityState := discoveryStateLabel(formatCell(capabilities["status"]))
	if capabilities["status"] == "verified" {
		capabilityState = "已获取（支持情况见 lbctl capabilities）"
	}
	if err := line("能力清单", capabilityState); err != nil {
		return err
	}
	runtimeInfo, _ := environment["runtime"].(map[string]any)
	if err := line("部署", lifecycleLabel(runtimeInfo)); err != nil {
		return err
	}
	if details, ok := runtimeInfo["details"].(map[string]any); ok {
		if upgrade, ok := details["upgrade"].(map[string]any); ok && upgrade["phase"] != "completed" {
			label := "已中断（" + formatCell(upgrade["phase"]) + "）"
			if upgrade["phase"] == "recovery_required" {
				label = "需要恢复"
				if step := formatCell(upgrade["failure_step"]); step != "" && step != "-" {
					label += " (" + step + ")"
				}
			}
			if err := line("升级", label); err != nil {
				return err
			}
		}
	}
	if binding := formatCell(runtimeInfo["binding"]); runtimeInfo["management"] != "cloud_managed" && binding != "" && binding != "none" && binding != "verified" && binding != "missing" {
		if err := line("部署绑定", discoveryStateLabel(binding)); err != nil {
			return err
		}
	}
	return nil
}

func environmentTypeLabel(value string) string {
	switch value {
	case "cloud":
		return "LangBot 云端"
	case "self_hosted":
		return "自部署"
	default:
		return "类型未知"
	}
}

func discoveryStateLabel(value string) string {
	switch value {
	case "verified", "complete":
		return "已验证"
	case "partial":
		return "部分完成"
	case "not_checked":
		return "未检查"
	case "failed":
		return "失败"
	case "mismatch":
		return "不一致"
	case "corrupt":
		return "已损坏"
	case "unreadable":
		return "无法读取"
	case "unavailable":
		return "不可用"
	default:
		return "未知"
	}
}

func lifecycleLabel(info map[string]any) string {
	switch formatCell(info["management"]) {
	case "cloud_managed":
		return "LangBot 云端托管"
	case "local_managed":
		driver := formatCell(info["driver"])
		if driver == "docker_compose" {
			driver = "Docker Compose"
		} else if driver == "native_process" {
			driver = "本机进程"
		}
		if status := formatCell(info["status"]); status != "" {
			return driver + " · " + localStatusLabel(status)
		}
		return driver + " · 已接管"
	case "unbound":
		return "未绑定本机部署"
	default:
		return "不可用"
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
	if _, err := fmt.Fprintln(w, actionSummary(data)); err != nil {
		return err
	}
	keys := []string{"operation", "action", "changed", "version", "profile", "endpoint", "dir", "image", "uuid", "model_uuid", "provider_uuid", "skill_name", "name", "author", "plugin_name", "server_name", "path", "file_id", "task_id", "model_type", "dry_run", "executed", "step", "submitted", "submission_result", "server_write", "verified", "server_cancelled", "delete_data", "business_validation"}
	if err := renderFields(w, data, keys); err != nil {
		return err
	}
	handled := make(map[string]bool, len(keys)+2)
	for _, key := range keys {
		handled[key] = true
	}
	handled["preconditions_confirmed"], handled["preflight_confirmed"] = true, true
	handled["outcome_error"] = true
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
	case "deployments":
		return []string{"deployment_id", "endpoint", "project", "binding", "dir"}
	case "unconfigured_deployments":
		return []string{"endpoint", "project", "binding", "dir"}
	case "discovered_deployments":
		return []string{"project", "endpoint", "binding", "compose_file"}
	case "contexts":
		return []string{"current", "name", "endpoint", "credential_state", "project", "binding"}
	case "checks":
		return []string{"name", "status", "reason", "context", "endpoint", "reachable", "diagnostic_ok", "identity", "capabilities", "server_version", "error"}
	case "containers":
		return []string{"service", "name", "state", "health"}
	case "bots":
		return []string{"name", "enable", "adapter", "use_pipeline_name", "uuid"}
	case "pipelines":
		return []string{"name", "is_default", "for_version", "uuid"}
	case "providers":
		return []string{"name", "requester", "llm_count", "embedding_count", "rerank_count", "uuid"}
	case "models":
		return []string{"name", "provider_name", "context_length", "uuid"}
	case "tasks":
		return []string{"id", "task_type", "kind", "status", "created_at"}
	case "bases":
		return []string{"name", "knowledge_engine_plugin_id", "uuid"}
	case "files":
		return []string{"uuid", "file_name", "extension", "status"}
	case "servers":
		return []string{"name", "enable", "mode", "uuid"}
	case "plugins":
		return []string{"author", "name", "version", "enabled", "status"}
	case "permissions":
		return []string{"id", "category", "risk", "granted", "grantable"}
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
	if key == "bots" && len(presentResourceFields([]string{"use_pipeline_name"}, items)) == 0 {
		fields = presentResourceFields([]string{"name", "enable", "adapter", "use_pipeline_uuid", "uuid"}, items)
	}
	if key == "models" && len(presentResourceFields([]string{"provider_name"}, items)) == 0 {
		fields = presentResourceFields([]string{"name", "provider_uuid", "context_length", "uuid"}, items)
	}
	if len(fields) == 0 {
		return renderValue(w, items, "")
	}
	cells := make([][]string, 1, len(items)+1)
	for _, field := range fields {
		cells[0] = append(cells[0], fieldLabel(field))
	}
	for _, item := range items {
		row, ok := item.(map[string]any)
		if !ok {
			return renderValue(w, items, "")
		}
		line := make([]string, len(fields))
		for i, field := range fields {
			line[i] = displayCell(row[field])
			if field == "enable" || field == "enabled" {
				if value, ok := row[field].(bool); ok {
					if value {
						line[i] = "已启用"
					} else {
						line[i] = "已停用"
					}
				}
			}
			if field == "is_default" {
				if value, ok := row[field].(bool); ok {
					if value {
						line[i] = "是"
					} else {
						line[i] = "否"
					}
				}
			}
			if field == "binding" {
				line[i] = bindingLabel(line[i])
			}
			if field == "credential_state" {
				line[i] = credentialLabel(line[i])
			}
			if field == "name" || field == "description" || field == "display_name" || field == "requester" {
				line[i] = truncateCell(line[i], 32)
			}
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
	if label == fieldLabel("credential_state") {
		if state, ok := value.(string); ok {
			value = credentialLabel(state)
		}
	}
	if label == fieldLabel("business_validation") && value == "not_run" {
		value = "未执行"
	}
	if label == fieldLabel("validation_scope") && value == "identity_permissions_capabilities" {
		value = "身份、权限和操作能力"
	}
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
		return "未安装"
	case "stopped":
		return "已停止"
	case "starting":
		return "启动中"
	case "running":
		return "运行中"
	case "degraded":
		return "异常"
	case "unknown":
		return "未知"
	default:
		return value
	}
}

func fieldLabel(field string) string {
	if label, exists := humanFieldLabels[field]; exists {
		return label
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
	width := 120
	if configured, err := strconv.Atoi(os.Getenv("COLUMNS")); err == nil && configured >= 40 {
		width = configured
	}
	total := 2 * (len(widths) - 1)
	for _, size := range widths {
		total += size
	}
	if total > width {
		for index, row := range rows[1:] {
			if index > 0 {
				if _, err := fmt.Fprintln(w); err != nil {
					return err
				}
			}
			for i, cell := range row {
				if _, err := fmt.Fprintf(w, "%s: %s\n", rows[0][i], cell); err != nil {
					return err
				}
			}
		}
		return nil
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

func actionSummary(data map[string]any) string {
	if data["outcome_error"] == true {
		return "操作未完整确认，请依据下方执行状态和回读结果处理。"
	}
	if data["dry_run"] == true {
		if data["business_validation"] == "server_preview" {
			return "服务端预览已完成，尚未执行安装。"
		}
		return "预检通过，尚未执行；仅确认所列检查，未确认服务端业务校验。"
	}
	switch data["operation"] {
	case "workspace.sandbox.check":
		return "E2B 连接检查通过；未保存配置，未创建沙箱。"
	case "workspace.sandbox.test":
		return "临时 E2B 沙箱创建、执行和清理已通过；未保存配置。"
	}
	if data["step"] == "completed" {
		return "任务已完成，服务端结果如下。"
	}
	if data["submitted"] == true {
		return "请求已提交；请查看任务状态确认是否完成。"
	}
	if data["verified"] == true {
		return "操作已完成，回读验证通过。"
	}
	if verification, ok := data["verification"].(map[string]any); ok && verification["observable_fields_match"] == true {
		return "操作已完成，可观察字段一致；隐藏字段未验证。"
	}
	if data["server_write"] == false {
		return "检查已完成，未写入服务端。"
	}
	return "操作返回成功，具体执行与验证状态如下。"
}

func credentialLabel(state string) string {
	if label, ok := map[string]string{"available": "可用（未鉴权）", "missing": "变量未设置", "not_configured": "未配置", "invalid_configuration": "配置无效", "stdin_required": "等待 stdin"}[state]; ok {
		return label
	}
	return state
}
func bindingLabel(state string) string {
	if label, ok := map[string]string{"registered": "已登记", "unregistered": "未登记", "ambiguous": "存在多个匹配", "verified": "已验证"}[state]; ok {
		return label
	}
	return state
}
func truncateCell(text string, width int) string {
	if cellWidth(text) <= width {
		return text
	}
	out := ""
	for _, r := range text {
		next := out + string(r)
		if cellWidth(next) > width-1 {
			break
		}
		out = next
	}
	return out + "…"
}

func renderExperienceFooter(w io.Writer, value any, command string) error {
	envelope, ok := value.(map[string]any)
	if !ok {
		return nil
	}
	data, _ := envelope["data"].(map[string]any)
	meta, _ := envelope["meta"].(map[string]any)
	if selection, ok := meta["list_selection"].(map[string]any); ok {
		if _, err := fmt.Fprintf(w, "显示 %s 条（共 %s 条，客户端筛选）\n", displayCell(selection["matched"]), displayCell(selection["total"])); err != nil {
			return err
		}
	}
	if strings.HasSuffix(command, ".list") && commandListKey(command) != "" && command != "context.list" {
		if _, err := fmt.Fprintln(w, "完整字段与未截断名称：在当前命令后加 -o json 或 -o yaml。"); err != nil {
			return err
		}
	}
	if command == "context.list" {
		if items, ok := data["contexts"].([]any); ok && len(items) == 0 {
			if _, err := fmt.Fprintln(w, "尚未保存环境。已有配置请用 --config <path> 或 LANGBOT_CONFIG；连接已有实例用 context add <name> --endpoint <url> --api-key-env <变量名>。"); err != nil {
				return err
			}
		}
	}
	if steps, ok := meta["next_steps"].([]any); ok {
		for _, step := range steps {
			item, _ := step.(map[string]any)
			argv, _ := item["argv"].([]any)
			parts := []string{}
			for _, arg := range argv {
				parts = append(parts, quoteCommandArg(formatCell(arg)))
			}
			if _, err := fmt.Fprintln(w, "下一步（"+displayCell(item["description"])+"）："+strings.Join(parts, " ")); err != nil {
				return err
			}
		}
	}
	return nil
}

func quoteCommandArg(value string) string {
	if value != "" && strings.IndexFunc(value, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("-_=./:\\", r))
	}) < 0 {
		return value
	}
	if runtime.GOOS == "windows" {
		return "'" + strings.ReplaceAll(value, "'", "''") + "'"
	}
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}
