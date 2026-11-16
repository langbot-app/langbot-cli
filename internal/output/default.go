package output

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"github.com/langbot-app/langbot-cli/internal/i18n"
	"github.com/langbot-app/langbot-cli/internal/progress"
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
			if _, err := fmt.Fprintln(w, i18n.T("Configuration file: ")+displayCell(path)); err != nil {
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
		_, err := fmt.Fprintln(w, i18n.T("No data"))
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
		_, err := fmt.Fprintln(w, i18n.T("Error: operation failed"))
		return err
	}
	message := displayCell(detail["message"])
	if message == "-" {
		message = i18n.T("Operation failed")
	}
	if _, err := fmt.Fprintln(w, i18n.T("Error: ")+message); err != nil {
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
		if _, err := fmt.Fprintln(w, fieldLabel(field)+i18n.Punct("：")+displayCell(value)); err != nil {
			return err
		}
	}
	if message == i18n.T("Server has not explicitly confirmed support for this operation") || message == i18n.T("Server explicitly does not support this operation") {
		_, err := fmt.Fprintln(w, i18n.T("Suggestion: run lbctl capabilities on the same target to check whether Core declares the required capability."))
		return err
	}
	if guidance, ok := detail["guidance"].(map[string]any); ok {
		if steps, ok := guidance["steps"].([]any); ok {
			for i, step := range steps {
				if _, err := fmt.Fprintf(w, i18n.T("Suggestion %d: %s\n"), i+1, formatCell(step)); err != nil {
					return err
				}
			}
		}
		if examples, ok := guidance["examples"].(map[string]any); ok {
			for _, shell := range []string{"powershell", "bash"} {
				if example, exists := examples[shell]; exists {
					if _, err := fmt.Fprintf(w, i18n.Punct("%s：%s\n"), shell, formatCell(example)); err != nil {
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
		target += i18n.T(" (temporary)")
	}
	_, err := fmt.Fprintln(w, i18n.T("Target: ")+target)
	return err
}

func renderDefaultData(w io.Writer, value any, command string) error {
	data, ok := value.(map[string]any)
	if !ok {
		return renderValue(w, value, "")
	}
	switch command {
	case "list":
		items, _ := data["instances"].([]any)
		return renderResourceList(w, "instances", items)
	case "context.add", "context.update", "context.use":
		verb := map[string]string{"context.add": i18n.T("Environment added"), "context.update": i18n.T("Environment updated"), "context.use": i18n.T("Switched to environment")}[command]
		if _, err := fmt.Fprintf(w, i18n.Punct("%s %s（%s）\n"), verb, displayCell(data["name"]), displayCell(data["endpoint"])); err != nil {
			return err
		}
		return renderFields(w, data, []string{"credential_source", "expected_workspace_uuid"})
	case "context.remove":
		_, err := fmt.Fprintln(w, i18n.T("Environment removed ")+displayCell(data["removed"]))
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
		var panel bytes.Buffer
		if err := renderActiveEnvironment(&panel, data); err != nil {
			return err
		}
		if err := progress.Panel(w, strings.Split(strings.TrimSuffix(panel.String(), "\n"), "\n")); err != nil {
			return err
		}
		_, err := fmt.Fprintln(w, i18n.T("Full details: append -o json or -o yaml to this command"))
		return err
	case "context.check":
		if checks, ok := data["checks"].([]any); ok {
			return renderChecks(w, checks)
		}
		if err := renderFields(w, data, []string{"reachable", "diagnostic_ok", "server_version", "server_edition", "capabilities"}); err != nil {
			return err
		}
		if identity, ok := data["identity"].(map[string]any); ok {
			if _, err := fmt.Fprintln(w, i18n.T("Identity:")); err != nil {
				return err
			}
			if err := renderFields(w, identity, []string{"instance_uuid", "workspace_uuid", "api_key_id"}); err != nil {
				return err
			}
		} else if identity, exists := data["identity"]; exists {
			if err := renderValue(w, identity, i18n.T("Identity")); err != nil {
				return err
			}
		}
		if item, exists := data["error"]; exists {
			return renderValue(w, item, i18n.T("Error "))
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
			_, err := fmt.Fprintln(w, i18n.T("Full details: append -o json or -o yaml to this command"))
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
	case "autostart.status", "autostart.enable", "autostart.disable":
		if data["mechanism"] == "HKCU Run" && data["autostart"] == false {
			if _, err := fmt.Fprintln(w, i18n.T("Windows temporarily removes the startup entry while an instance is stopped. If startup is enabled in the deployment settings, starting the instance restores it.")); err != nil {
				return err
			}
		}
		return renderFields(w, data, []string{"autostart", "supported", "mechanism", "location", "instance_dir"})
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
		outcome := i18n.T("Batch preflight passed")
		if data["outcome_error"] == true {
			outcome = i18n.T("Batch preflight incomplete")
		}
		if _, err := fmt.Fprintf(w, i18n.T("%s, %s files; not uploaded, server business validation not performed.\n"), outcome, formatCell(summary["total"])); err != nil {
			return err
		}
	} else {
		if _, err := fmt.Fprintf(w, i18n.T("%s files: %s completed, %s submitted, %s failed, %s unknown, %s pending, %s not started.\n"),
			formatCell(summary["total"]), formatCell(summary["completed"]), formatCell(summary["submitted"]), formatCell(summary["failed"]), formatCell(summary["unknown"]), formatCell(summary["pending"]), formatCell(summary["not_started"])); err != nil {
			return err
		}
	}
	states := map[string]string{"planned": i18n.T("Ready to upload"), "submitted": i18n.T("Submitted"), "completed": i18n.T("Completed"), "failed": i18n.T("Failed"), "unknown": i18n.T("Result unknown"), "pending": i18n.T("Pending"), "not_started": i18n.T("Not started")}
	items, _ := data["files"].([]any)
	for _, item := range items {
		row, _ := item.(map[string]any)
		if _, err := fmt.Fprintf(w, "%s  %s", states[formatCell(row["status"])], displayCell(row["path"])); err != nil {
			return err
		}
		if id, ok := row["task_id"]; ok {
			if _, err := fmt.Fprintf(w, i18n.T("  Task %s"), displayCell(id)); err != nil {
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
		} else if data["step"] != nil {
			line = "安装失败\n失败步骤：" + displayCell(data["step"])
			for _, field := range [][2]string{{"log_path", "日志"}, {"runtime_log_path", "运行日志"}, {"retry_command", "重试"}, {"rollback_command", "回滚"}} {
				if value, ok := data[field[0]].(string); ok && value != "" {
					line += "\n" + field[1] + "：" + value
				}
			}
		} else if data["changed"] != true {
			line = i18n.T("Installation not performed: preflight failed")
		} else if state != "running" || data["step"] != nil {
			line = i18n.T("Installation ran; running state is unconfirmed: ") + endpoint
		} else {
			line = i18n.T("Installed LangBot ") + version + " → " + endpoint
		}
	case "adopt":
		line = i18n.T("Adopted local deployment: ") + endpoint
		if data["changed"] == false {
			line = i18n.T("Local deployment adopted: ") + endpoint
		}
	case "start", "stop", "restart":
		line = map[string]string{"start": i18n.T("Local deployment started"), "stop": i18n.T("Local deployment stopped"), "restart": i18n.T("Local deployment restarted")}[action]
		if data["changed"] == false {
			line = map[string]string{"start": i18n.T("Local deployment already running"), "stop": i18n.T("Local deployment already stopped"), "restart": i18n.T("Local deployment already restarted")}[action]
		} else if state != map[string]string{"start": "running", "stop": "stopped", "restart": "running"}[action] {
			line = i18n.T("Command executed; local deployment status is unconfirmed")
		}
	case "upgrade":
		from, _ := data["source_version"].(string)
		to, _ := data["target_version"].(string)
		if data["dry_run"] == true {
			line = i18n.T("Upgrade plan: LangBot ") + from + " → " + to
		} else if data["completed"] == true {
			line = i18n.T("Upgraded LangBot ") + from + " → " + to
		} else {
			line = i18n.T("Upgrade incomplete; run lbctl status and lbctl doctor to inspect recovery state")
		}
	case "uninstall":
		if data["project_removed"] == true {
			line = i18n.T("Uninstalled managed local deployment; data and deployment records preserved")
			if data["purge_data"] == true {
				line = i18n.T("Uninstalled managed local deployment and removed data; backups and deployment records preserved")
			}
		} else {
			line = i18n.T("Uninstall incomplete; run lbctl status and lbctl doctor")
		}
	}
	expected := map[string]string{"install": "running", "start": "running", "stop": "stopped", "restart": "running"}[action]
	if state != "" && state != expected && data["changed"] != false {
		label := map[string]string{"running": i18n.T("Running"), "stopped": i18n.T("Stopped"), "starting": i18n.T("Starting"), "degraded": i18n.T("Some services unhealthy"), "unknown": i18n.T("Status unknown")}[state]
		if label == "" {
			label = state
		}
		line += i18n.Punct("（") + label + i18n.Punct("）")
	}
	if line != "" {
		if _, err := fmt.Fprintln(w, line); err != nil {
			return err
		}
	}
	if action == "install" && data["step"] != nil {
		if data["preflight"] == true {
			if _, err := fmt.Fprintln(w, "预检阶段没有改动任何文件。"); err != nil {
				return err
			}
		}
		if data["step"] != "check_ports" {
			return nil
		}
	}
	if action == "install" && data["dry_run"] != true && data["changed"] == true && state == "running" && data["step"] == nil {
		lines := []string{"WebUI: " + displayCell(endpoint)}
		if data["dir"] == nil {
			data["dir"] = data["instance_dir"]
		}
		if dir, ok := data["dir"].(string); ok && dir != "" && record["driver"] == "native" {
			if data["log_path"] == nil {
				data["log_path"] = filepath.Join(dir, "logs", "install.log")
			}
			if data["runtime_log_path"] == nil {
				data["runtime_log_path"] = filepath.Join(dir, "logs", "langbot.log")
			}
		}
		for _, field := range []string{"dir", "log_path", "runtime_log_path"} {
			if value, ok := data[field]; ok && value != "" {
				lines = append(lines, fieldLabel(field)+": "+displayCell(value))
			}
		}
		if enabled, ok := data["autostart"].(bool); ok {
			message := i18n.T("Autostart disabled")
			if enabled {
				message = i18n.T("Autostart enabled")
			}
			lines = append(lines, fmt.Sprintf("%s (%v)", message, data["autostart_mechanism"]))
			if enabled {
				lines = append(lines, strings.TrimSpace(fmt.Sprintf(i18n.T("Disable: %v\n"), data["autostart_disable_command"])))
			}
		}
		language := "en"
		if data["autostart_note"] == "loginctl enable-linger" {
			lines = append(lines, i18n.T("Run loginctl enable-linger to keep LangBot running after logout."))
		}
		if i18n.Current() == i18n.Chinese {
			language = "zh"
		}
		base := "https://docs.langbot.app/" + language
		lines = append(lines, i18n.T("1. Open WebUI and create an administrator account"), base+"/deploy/settings", i18n.T("2. Add a model (LangBot Space includes free credits)"), base+"/usage/models/readme", i18n.T("3. Connect a bot platform"), base+"/usage/platforms/readme", "lbctl status · lbctl logs · lbctl stop · lbctl start", "lbctl upgrade --yes · lbctl autostart disable", i18n.T("Prefer not to self-host? https://cloud.langbot.app"))
		return progress.Panel(w, lines)
	}
	if data["dry_run"] == true || (action == "install" && data["changed"] != true) {
		if action == "install" {
			if err := renderFields(w, data, []string{"compose_project", "plugin_debug_port", "adapter_ports"}); err != nil {
				return err
			}
		}
		if dir, ok := data["dir"].(string); ok && dir != "" {
			if _, err := fmt.Fprintln(w, i18n.T("Deployment directory: ")+dir); err != nil {
				return err
			}
		}
		if checks, ok := data["checks"].([]any); ok {
			failed := 0
			for _, item := range checks {
				check, _ := item.(map[string]any)
				if check["status"] == "error" {
					failed++
					if _, err := fmt.Fprintf(w, i18n.T("Failed: %s (%s)\n"), displayCell(check["name"]), displayCell(check["reason"])); err != nil {
						return err
					}
				}
			}
			if failed == 0 {
				_, err := fmt.Fprintf(w, i18n.T("Preflight: %d checks passed\n"), len(checks))
				return err
			}
			if data["step"] == "check_ports" {
				_, err := fmt.Fprintf(w, "预检：%d 项通过，%d 项失败（端口）\n", len(checks)-failed, failed)
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
		name = i18n.T("Not configured")
	}
	if err := line(i18n.T("Current environment"), name+i18n.Punct("（")+environmentTypeLabel(formatCell(environment["type"]))+i18n.Punct("）")); err != nil {
		return err
	}
	connection, _ := environment["connection"].(map[string]any)
	if err := line(i18n.T("URL"), connection["endpoint"]); err != nil {
		return err
	}
	connectionLabel := i18n.T("Unreachable")
	if formatCell(environment["discovery"]) == "unconfigured" {
		connectionLabel = i18n.T("Not configured")
	} else if connection["reachable"] == true {
		connectionLabel = i18n.T("Connected")
	}
	if err := line(i18n.T("Connection"), connectionLabel); err != nil {
		return err
	}
	if err := line(i18n.T("Authentication"), discoveryStateLabel(formatCell(connection["authentication"]))); err != nil {
		return err
	}
	service, _ := environment["service"].(map[string]any)
	core := formatCell(service["version"])
	if versions, ok := environment["versions"].(map[string]any); ok {
		if version, ok := versions["lbctl"].(map[string]any); ok {
			value := formatCell(version["current"])
			if latest := formatCell(version["latest"]); latest != "" {
				value += " → " + latest
			}
			if err := line("lbctl", value); err != nil {
				return err
			}
		}
		if version, ok := versions["core"].(map[string]any); ok {
			if core == "" {
				core = formatCell(version["current"])
			}
			if latest := formatCell(version["latest"]); latest != "" {
				core += " → " + latest
			}
		}
	}
	if edition := formatCell(service["edition"]); core != "" && edition != "" && environment["type"] == "unknown" {
		core += " (" + edition + ")"
	}
	if err := line("Core", core); err != nil {
		return err
	}
	identity, _ := environment["identity"].(map[string]any)
	if state := formatCell(identity["status"]); state == "mismatch" || state == "failed" {
		if err := line(i18n.T("Workspace identity"), discoveryStateLabel(state)); err != nil {
			return err
		}
		if err := line(i18n.T("Actual Workspace"), identity["workspace_uuid"]); err != nil {
			return err
		}
	}
	capabilities, _ := environment["capabilities"].(map[string]any)
	capabilityState := discoveryStateLabel(formatCell(capabilities["status"]))
	if capabilities["status"] == "verified" {
		capabilityState = i18n.T("Retrieved (see lbctl capabilities for support)")
	}
	if err := line(i18n.T("Capability manifest"), capabilityState); err != nil {
		return err
	}
	runtimeInfo, _ := environment["runtime"].(map[string]any)
	if err := line(i18n.T("Directory"), runtimeInfo["dir"]); err != nil {
		return err
	}
	if err := line(i18n.T("Uptime"), environment["human_uptime"]); err != nil {
		return err
	}
	if err := line(i18n.T("Deployment"), lifecycleLabel(runtimeInfo)); err != nil {
		return err
	}
	if details, ok := runtimeInfo["details"].(map[string]any); ok {
		if record, ok := details["record"].(map[string]any); ok {
			if err := line(i18n.T("Instance"), record["name"]); err != nil {
				return err
			}
			startup := i18n.T("Not reported")
			if enabled, ok := record["autostart"].(bool); ok {
				startup = i18n.T("Disabled")
				if enabled {
					startup = i18n.T("Enabled")
				}
			}
			if err := line(i18n.T("Startup"), startup); err != nil {
				return err
			}
		}
		if upgrade, ok := details["upgrade"].(map[string]any); ok && upgrade["phase"] != "completed" {
			label := i18n.T("Interrupted (") + formatCell(upgrade["phase"]) + i18n.Punct("）")
			if upgrade["phase"] == "recovery_required" {
				label = i18n.T("Recovery required")
				if step := formatCell(upgrade["failure_step"]); step != "" && step != "-" {
					label += " (" + step + ")"
				}
			}
			if err := line(i18n.T("Upgrade"), label); err != nil {
				return err
			}
		}
	}
	if binding := formatCell(runtimeInfo["binding"]); runtimeInfo["management"] != "cloud_managed" && binding != "" && binding != "none" && binding != "verified" && binding != "missing" {
		if err := line(i18n.T("Deployment binding"), discoveryStateLabel(binding)); err != nil {
			return err
		}
	}
	return nil
}

func environmentTypeLabel(value string) string {
	switch value {
	case "cloud":
		return i18n.T("LangBot Cloud")
	case "self_hosted":
		return i18n.T("Self-hosted")
	default:
		return i18n.T("Type unknown")
	}
}

func discoveryStateLabel(value string) string {
	switch value {
	case "verified", "complete":
		return i18n.T("Verified")
	case "partial":
		return i18n.T("Partially completed")
	case "not_checked":
		return i18n.T("Not checked")
	case "failed":
		return i18n.T("Failed")
	case "mismatch":
		return i18n.T("Mismatch")
	case "corrupt":
		return i18n.T("Corrupt")
	case "unreadable":
		return i18n.T("Unable to read")
	case "unavailable":
		return i18n.T("Unavailable")
	default:
		return i18n.T("Unknown")
	}
}

func lifecycleLabel(info map[string]any) string {
	switch formatCell(info["management"]) {
	case "cloud_managed":
		return i18n.T("Hosted by LangBot Cloud")
	case "local_managed":
		driver := formatCell(info["driver"])
		if driver == "docker_compose" {
			driver = "Docker Compose"
		} else if driver == "native_process" {
			driver = i18n.T("Local process")
		} else if driver == "native" {
			driver = i18n.T("Native deployment")
		}
		if status := formatCell(info["status"]); status != "" {
			return driver + " · " + localStatusLabel(status)
		}
		if info["driver"] == "native" {
			return driver + i18n.T(" · managed by lbctl")
		}
		return driver + i18n.T(" · adopted")
	case "unbound":
		return i18n.T("No local deployment bound")
	default:
		return i18n.T("Unavailable")
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
	case "instances":
		return []string{"name", "dir", "port", "status", "autostart"}
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
		_, err := fmt.Fprintln(w, i18n.T("No records"))
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
			if field == "enable" || field == "enabled" || field == "autostart" {
				if value, ok := row[field].(bool); ok {
					if value {
						line[i] = i18n.T("Enabled")
					} else {
						line[i] = i18n.T("Disabled")
					}
				}
			}
			if field == "is_default" {
				if value, ok := row[field].(bool); ok {
					if value {
						line[i] = i18n.T("Yes")
					} else {
						line[i] = i18n.T("No")
					}
				}
			}
			if field == "binding" {
				line[i] = bindingLabel(line[i])
			}
			if key == "instances" && field == "status" {
				line[i] = localStatusLabel(line[i])
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
		value = i18n.T("Not executed")
	}
	if label == fieldLabel("validation_scope") && value == "identity_permissions_capabilities" {
		value = i18n.T("Identity, permissions and operation capabilities")
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
			_, err := fmt.Fprintln(w, prefix+i18n.T("No records"))
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
		return i18n.T("Not installed")
	case "stopped":
		return i18n.T("Stopped")
	case "starting":
		return i18n.T("Starting")
	case "running":
		return i18n.T("Running")
	case "degraded":
		return i18n.T("Error")
	case "unknown":
		return i18n.T("Unknown")
	default:
		return value
	}
}

func fieldLabel(field string) string {
	if label, exists := humanFieldLabels[field]; exists {
		return i18n.T(label)
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
		return i18n.T("Operation not fully confirmed; inspect execution state and readback results below.")
	}
	if data["dry_run"] == true {
		if data["business_validation"] == "server_preview" {
			return i18n.T("Server preview completed; installation not performed.")
		}
		return i18n.T("Preflight passed; not executed. Only listed checks confirmed, server business validation not confirmed.")
	}
	switch data["operation"] {
	case "workspace.sandbox.check":
		return i18n.T("E2B connection check passed; configuration was not saved and no sandbox was created.")
	case "workspace.sandbox.test":
		return i18n.T("Temporary E2B sandbox creation, execution and cleanup passed; configuration was not saved.")
	}
	if data["step"] == "completed" {
		return i18n.T("Task completed; server result follows.")
	}
	if data["submitted"] == true {
		return i18n.T("Request submitted; check task status to confirm completion.")
	}
	if data["verified"] == true {
		return i18n.T("Operation completed and readback verified.")
	}
	if verification, ok := data["verification"].(map[string]any); ok && verification["observable_fields_match"] == true {
		return i18n.T("Operation completed; observable fields match, hidden fields were not verified.")
	}
	if data["server_write"] == false {
		return i18n.T("Checks completed; nothing written to the server.")
	}
	return i18n.T("Operation returned success; execution and verification state follows.")
}

func credentialLabel(state string) string {
	if label, ok := map[string]string{"available": i18n.T("Available (unauthenticated)"), "missing": i18n.T("Variable is unset"), "not_configured": i18n.T("Not configured"), "invalid_configuration": i18n.T("Invalid configuration"), "stdin_required": i18n.T("Waiting for stdin")}[state]; ok {
		return label
	}
	return state
}
func bindingLabel(state string) string {
	if label, ok := map[string]string{"registered": i18n.T("Registered"), "unregistered": i18n.T("Unregistered"), "ambiguous": i18n.T("Multiple matches"), "verified": i18n.T("Verified")}[state]; ok {
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
		if _, err := fmt.Fprintf(w, i18n.T("Showing %s items (%s total, client-side filter)\n"), displayCell(selection["matched"]), displayCell(selection["total"])); err != nil {
			return err
		}
	}
	if strings.HasSuffix(command, ".list") && commandListKey(command) != "" && command != "context.list" {
		if _, err := fmt.Fprintln(w, i18n.T("Full fields and untruncated names: append -o json or -o yaml to this command.")); err != nil {
			return err
		}
	}
	if command == "context.list" {
		if items, ok := data["contexts"].([]any); ok && len(items) == 0 {
			if _, err := fmt.Fprintln(w, i18n.T("No saved environments. Use --config <path> or LANGBOT_CONFIG for existing configuration; connect with context add <name> --endpoint <url> --api-key-env <variable>.")); err != nil {
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
			if _, err := fmt.Fprintln(w, i18n.T("Next steps (")+displayCell(item["description"])+i18n.Punct("）：")+strings.Join(parts, " ")); err != nil {
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
