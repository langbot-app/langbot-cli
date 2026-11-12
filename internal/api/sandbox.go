package api

import (
	"context"
	"net/http"
	"strings"

	"github.com/langbot-app/langbot-cli/internal/redact"
	"github.com/langbot-app/langbot-cli/internal/result"
)

const workspaceSandboxPath = "/api/v1/workspaces/current/sandbox"

func (c Client) WorkspaceSandbox(ctx context.Context, target Target, action string, refresh bool, body map[string]any) (map[string]any, error) {
	var resp response
	var err error
	switch action {
	case "get":
		path := workspaceSandboxPath
		if refresh {
			path += "?refresh=true"
		}
		resp, err = c.do(ctx, target, http.MethodGet, path, nil, false)
	case "update":
		resp, err = c.write(ctx, target, http.MethodPut, workspaceSandboxPath, body)
	case "check":
		resp, err = c.readPost(ctx, target, workspaceSandboxPath+"/check", body)
	case "test":
		resp, err = c.write(ctx, target, http.MethodPost, workspaceSandboxPath+"/test", bodyOrEmpty(body))
	default:
		return nil, result.New("input", "未知的沙箱配置操作")
	}
	secret := ""
	if e2b, ok := body["e2b"].(map[string]any); ok {
		secret, _ = e2b["api_key"].(string)
		secret = strings.TrimSpace(secret)
	}
	if err != nil {
		failure := *result.AsError(err)
		if target.APIKey != "" {
			failure.Message = strings.ReplaceAll(failure.Message, target.APIKey, "***")
		}
		if secret != "" {
			failure.Message = strings.ReplaceAll(failure.Message, secret, "***")
			failure.RequestID = strings.ReplaceAll(failure.RequestID, secret, "***")
			failure.ServerCode = redactConnectionSecret(failure.ServerCode, secret)
		}
		return nil, &failure
	}
	if resp.Data == nil {
		err = protocolError("服务端未返回沙箱配置，操作结果需核查", resp.StatusCode, "")
	} else if _, ok := resp.Data["backends"].([]any); !ok {
		err = protocolError("服务端沙箱配置缺少后端诊断，无法确认结果", resp.StatusCode, "")
	} else {
		for _, field := range []string{"e2b", "limits", "limit_caps"} {
			if _, ok := resp.Data[field].(map[string]any); !ok {
				err = protocolError("服务端沙箱配置格式不完整，无法确认结果", resp.StatusCode, "")
			}
		}
		if _, exists := resp.Data["backend"]; !exists {
			err = protocolError("服务端未返回已保存的沙箱后端", resp.StatusCode, "")
		}
	}
	if err != nil {
		if action == "update" || action == "test" {
			err = classifyWriteError(err)
		}
		return nil, err
	}
	data := redactConnectionSecret(resp.Data, target.APIKey)
	data = redactConnectionSecret(data, secret)
	safe := redact.Configuration(data, "").(map[string]any)
	if e2b, ok := safe["e2b"].(map[string]any); ok {
		delete(e2b, "api_key")
	}
	return safe, nil
}

func workspaceSandboxErrorMessage(message, fallback string) string {
	switch message {
	case "Workspace sandbox settings require an upgraded Box SDK and Runtime", "Workspace sandbox limits require an upgraded Box SDK and Runtime":
		return "服务端需要升级 Box SDK 和 Runtime 才能配置工作区沙箱"
	case "Custom E2B configuration is restricted by platform policy":
		return "当前平台策略不允许自定义 E2B 连接"
	case "A new API key is required when E2B addresses change":
		return "更换 E2B 地址时必须提供新的 E2B API Key"
	case "The selected sandbox backend is unavailable":
		return "所选沙箱后端不可用；请查询配置或检查连接"
	}
	return fallback
}

func resolveSandboxOperation(path string) (Operation, bool) {
	id, permission := "", "resource.view"
	switch path {
	case "/api/v1/box/status":
		id = "sandbox.status"
	case "/api/v1/box/sessions":
		id, permission = "sandbox.sessions", "audit.view"
	case "/api/v1/box/errors":
		id, permission = "sandbox.errors", "audit.view"
	}
	return Operation{ID: id, Method: http.MethodGet, Path: path, ReadOnly: true, Permission: permission}, id != ""
}
