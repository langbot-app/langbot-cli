package app

import (
	"context"
	"encoding/json"
	"math"
	"strings"

	"github.com/langbot-app/langbot-cli/internal/i18n"
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
	if action != "update" && action != "check" && action != "test" {
		return Result{}, result.New("input", i18n.T("Unknown sandbox configuration operation"))
	}
	if !dryRun && !confirmed && action != "check" {
		message := i18n.T("Saving workspace sandbox settings requires --yes; only new tasks are affected, existing tasks retain their snapshots")
		if action == "test" {
			message = i18n.T("Test creates, executes and deletes a temporary E2B sandbox, consuming quota and potentially incurring charges; explicitly pass --yes")
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
			if action == "update" {
				data["saved"] = "unknown"
			} else if action == "test" {
				data["executed"] = "unknown"
			}
		}
		return Result{Data: data, Meta: preflight.Meta()}, err
	}
	data["settings"] = settings
	if action == "update" {
		data["server_write"], data["saved"] = true, true
		readback, readErr := client.WorkspaceSandbox(ctx, target, "get", false, body)
		if readErr != nil {
			return Result{Data: data, Meta: preflight.Meta()}, readbackError(readErr)
		}
		data["settings"] = readback
		expected, actual := sandboxVerification(body, readback)
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
		failure := result.New("precondition", i18n.T("E2B connection check failed; see backend diagnostics"))
		if action == "test" {
			failure = result.New("server", i18n.T("E2B execution or cleanup was not confirmed; inspect template_validation, reason and cleanup_reason"))
			if backend["template_validation"] == "unsupported" {
				failure = result.New("incompatible", i18n.T("Core or Box SDK/Runtime has not confirmed temporary E2B test support; see backend diagnostics"))
			}
		}
		failure.Type = "sandbox_" + action + "_failed"
		return Result{Data: data, Meta: preflight.Meta()}, failure
	}
	return Result{Data: data, Meta: preflight.Meta()}, result.New("incompatible", i18n.T("Server did not return E2B backend diagnostics; check Core, Box SDK and Runtime support"))
}

func validateSandboxBody(action string, body map[string]any) error {
	fields := []string{"e2b"}
	if action == "update" {
		fields = append(fields, "backend", "limits")
		backend, _ := body["backend"].(string)
		if backend != "docker" && backend != "e2b" && backend != "nsjail" {
			return result.New("input", i18n.T("backend must be docker, e2b or nsjail"))
		}
	}
	if err := validateBodyFields(body, fields...); err != nil {
		return err
	}
	if raw := body["e2b"]; raw != nil {
		e2b, ok := raw.(map[string]any)
		if !ok {
			return result.New("input", i18n.T("e2b must be an object"))
		}
		if err := validateBodyFields(e2b, "api_url", "sandbox_url", "api_key", "template"); err != nil {
			return err
		}
		for _, value := range e2b {
			if _, ok := value.(string); !ok {
				return result.New("input", i18n.T("E2B configuration fields must be strings"))
			}
		}
	}
	if raw := body["limits"]; raw != nil {
		limits, ok := raw.(map[string]any)
		if !ok {
			return result.New("input", i18n.T("limits must be an object"))
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
				return result.New("input", i18n.T("limits must be positive; only cpus accepts fractions"))
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
			return result.New("precondition", name+i18n.T(" is not supported by the selected backend or platform policy"))
		}
		n, _ := sandboxNumber(value)
		cap, ok := sandboxNumber(caps[name])
		if !ok {
			return result.New("incompatible", i18n.T("Server did not return ")+name+i18n.T(" effective limit"))
		}
		if n > cap {
			return result.New("input", name+i18n.T(" exceeds the server limit"))
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
