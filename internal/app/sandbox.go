package app

import (
	"context"
	"encoding/json"
	"math"
	"strings"

	"github.com/langbot-app/langbot-cli/internal/result"
)

// SandboxConfig delegates backend lifecycle and credential retention to Core.
func (s *Service) SandboxConfig(ctx context.Context, action string, body map[string]any, refresh, dryRun, confirmed bool, options CheckOptions) (Result, error) {
	operation := "workspace.sandbox." + action
	if action == "get" {
		preflight, err := s.readPreflight(ctx, operation, "workspace.view", options)
		if err != nil {
			return Result{Meta: preflight.Meta()}, err
		}
		client, target := s.writeClient(preflight)
		settings, err := client.WorkspaceSandbox(ctx, target, action, refresh, nil)
		return Result{Data: settings, Meta: preflight.Meta()}, err
	}
	if action != "update" && action != "reset" && action != "check" && action != "test" {
		return Result{}, result.New("input", "未知的沙箱配置操作")
	}
	if !dryRun && !confirmed && action != "check" {
		message := "保存工作区沙箱配置需要 --yes；仅影响新任务，已有任务保留原配置快照"
		if action == "test" {
			message = "测试会创建、执行并删除临时 E2B 沙箱，消耗配额并可能计费；请显式提供 --yes"
		}
		if action == "reset" {
			message = "恢复部署默认配置将清除工作区的后端、E2B 凭据和资源限制覆盖，需要 --yes"
		}
		return Result{}, result.New("input", message)
	}
	if err := validateSandboxBody(action, body); err != nil {
		return Result{}, err
	}
	preflight, err := s.writePreflight(ctx, operation, "workspace.update", options)
	if err != nil {
		return Result{Meta: preflight.Meta()}, err
	}
	client, target := s.writeClient(preflight)
	if action == "update" {
		settings, getErr := client.WorkspaceSandbox(ctx, target, "get", true, body)
		if getErr != nil {
			return Result{Meta: preflight.Meta()}, getErr
		}
		if err := validateSandboxLimits(body, settings); err != nil {
			return Result{Data: map[string]any{"settings": settings}, Meta: preflight.Meta()}, err
		}
	}
	if dryRun {
		return writePlan(preflight, operation, ""), nil
	}
	data := map[string]any{"operation": operation, "server_write": false, "saved": false}
	settings, err := client.WorkspaceSandbox(ctx, target, action, false, body)
	if err != nil {
		if result.AsError(err).Type == "result_unknown" {
			data["server_write"] = "unknown"
			if action == "update" || action == "reset" {
				data["saved"] = "unknown"
			} else if action == "test" {
				data["executed"] = "unknown"
			}
		}
		return Result{Data: data, Meta: preflight.Meta()}, err
	}
	data["settings"] = settings
	if action == "update" || action == "reset" {
		data["server_write"], data["saved"] = true, true
		readback, readErr := client.WorkspaceSandbox(ctx, target, "get", false, body)
		if readErr != nil {
			return Result{Data: data, Meta: preflight.Meta()}, readbackError(readErr)
		}
		data["settings"] = readback
		expected, actual := sandboxVerification(body, readback)
		if action == "reset" {
			e2b, _ := readback["e2b"].(map[string]any)
			expected = map[string]any{"backend": nil, "limits": map[string]any{}, "e2b_source": "deployment"}
			actual = map[string]any{"backend": readback["backend"], "limits": readback["limits"], "e2b_source": e2b["source"]}
		}
		return verifyResourceWrite(preflight, data, expected, actual)
	}
	if action == "test" {
		data["executed"] = "unknown"
	}
	for _, value := range settings["backends"].([]any) {
		backend, ok := value.(map[string]any)
		if !ok || backend["name"] != "e2b" {
			continue
		}
		cleaned := backend["cleanup_reason"] == nil || backend["cleanup_reason"] == ""
		if backend["available"] == true && (action != "test" || backend["template_validated"] == true && backend["template_validation"] == "ready" && cleaned) {
			if action == "test" {
				data["executed"] = true
			}
			return Result{Data: data, Meta: preflight.Meta()}, nil
		}
		failure := result.New("precondition", "E2B 连接检查未通过，请查看后端诊断")
		if action == "test" {
			failure = result.New("server", "E2B 执行或清理未确认成功，请查看 template_validation、reason 和 cleanup_reason")
			if backend["template_validation"] == "unsupported" {
				failure = result.New("incompatible", "Core 或 Box SDK/Runtime 未确认支持临时 E2B 执行测试；请查看后端诊断")
			}
		}
		failure.Type = "sandbox_" + action + "_failed"
		return Result{Data: data, Meta: preflight.Meta()}, failure
	}
	return Result{Data: data, Meta: preflight.Meta()}, result.New("incompatible", "服务端未返回 E2B 后端诊断；请检查 Core、Box SDK 和 Runtime 支持")
}

func validateSandboxBody(action string, body map[string]any) error {
	if action == "reset" {
		return validateBodyFields(body)
	}
	fields := []string{"e2b"}
	if action == "update" {
		fields = append(fields, "backend", "limits")
		backend, _ := body["backend"].(string)
		if backend != "docker" && backend != "e2b" && backend != "nsjail" {
			return result.New("input", "backend 必须为 docker、e2b 或 nsjail")
		}
	}
	if err := validateBodyFields(body, fields...); err != nil {
		return err
	}
	if raw := body["e2b"]; raw != nil {
		e2b, ok := raw.(map[string]any)
		if !ok {
			return result.New("input", "e2b 必须是对象")
		}
		if err := validateBodyFields(e2b, "api_url", "sandbox_url", "api_key", "template"); err != nil {
			return err
		}
		for _, value := range e2b {
			if _, ok := value.(string); !ok {
				return result.New("input", "E2B 配置字段必须是字符串")
			}
		}
	}
	if raw := body["limits"]; raw != nil {
		limits, ok := raw.(map[string]any)
		if !ok {
			return result.New("input", "limits 必须是对象")
		}
		if err := validateBodyFields(limits, "timeout_sec", "idle_ttl_sec", "max_sessions", "cpus", "memory_mb", "pids_limit", "workspace_quota_mb"); err != nil {
			return err
		}
		for name, value := range limits {
			if value == nil {
				continue
			}
			n, ok := sandboxNumber(value)
			if !ok || n <= 0 || name != "cpus" && math.Trunc(n) != n {
				return result.New("input", "limits 必须使用正数；除 cpus 外只接受整数")
			}
		}
	}
	return nil
}

func sandboxNumber(value any) (float64, bool) {
	if value == nil {
		return 0, false
	}
	encoded, err := json.Marshal(value)
	var n float64
	if err != nil || json.Unmarshal(encoded, &n) != nil || math.IsNaN(n) || math.IsInf(n, 0) {
		return 0, false
	}
	return n, true
}

func validateSandboxLimits(body, settings map[string]any) error {
	limits, _ := body["limits"].(map[string]any)
	caps, _ := settings["limit_caps"].(map[string]any)
	var capabilities map[string]any
	backends, _ := settings["backends"].([]any)
	for _, raw := range backends {
		if backend, ok := raw.(map[string]any); ok && backend["name"] == body["backend"] {
			capabilities, _ = backend["limit_capabilities"].(map[string]any)
		}
	}
	for name, value := range limits {
		if value == nil {
			continue
		}
		if capabilities[name] != true {
			return result.New("precondition", name+" 不受所选后端或当前平台策略支持")
		}
		n, _ := sandboxNumber(value)
		cap, ok := sandboxNumber(caps[name])
		if !ok {
			return result.New("incompatible", "服务端未返回 "+name+" 的有效上限")
		}
		if n > cap {
			return result.New("input", name+" 超过服务端上限")
		}
	}
	return nil
}

func sandboxVerification(body, readback map[string]any) (map[string]any, map[string]any) {
	expected := map[string]any{"backend": body["backend"]}
	actual := map[string]any{"backend": readback["backend"]}
	if limits, ok := body["limits"].(map[string]any); ok {
		values := map[string]any{}
		for key, value := range limits {
			if value != nil {
				values[key] = value
			}
		}
		expected["limits"], actual["limits"] = values, readback["limits"]
	}
	if e2b, ok := body["e2b"].(map[string]any); ok {
		want, got := map[string]any{}, map[string]any{}
		public, _ := readback["e2b"].(map[string]any)
		for key, value := range e2b {
			if key == "api_key" {
				if value == "" {
					continue
				}
				want[key] = value
				if public["api_key_configured"] == true {
					got[key] = "***"
				}
				continue
			}
			if existing, exists := public[key]; exists {
				got[key] = existing
			}
			want[key] = value
			if key == "api_url" || key == "sandbox_url" {
				want[key] = strings.TrimRight(value.(string), "/")
				if text, ok := got[key].(string); ok {
					got[key] = strings.TrimRight(text, "/")
				}
			}
		}
		expected["e2b"], actual["e2b"] = want, got
	}
	return expected, actual
}
