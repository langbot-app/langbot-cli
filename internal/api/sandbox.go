package api

import (
	"context"
	"net/http"
	"strings"

	"github.com/langbot-app/langbot-cli/internal/i18n"
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
		return nil, result.New("input", i18n.T("Unknown sandbox configuration operation"))
	}
	secret := ""
	if e2b, ok := body["e2b"].(map[string]any); ok {
		secret, _ = e2b["api_key"].(string)
		secret = strings.TrimSpace(secret)
	}
	if err != nil {
		failure := *result.AsError(err)
		if target.secret() != "" {
			failure.Message = strings.ReplaceAll(failure.Message, target.secret(), "***")
		}
		if secret != "" {
			failure.Message = strings.ReplaceAll(failure.Message, secret, "***")
			failure.RequestID = strings.ReplaceAll(failure.RequestID, secret, "***")
			failure.ServerCode = redactConnectionSecret(failure.ServerCode, secret)
		}
		return nil, &failure
	}
	if resp.Data == nil {
		err = protocolError(i18n.T("Server did not return sandbox configuration; verify operation result"), resp.StatusCode, "")
	} else if _, ok := resp.Data["backends"].([]any); !ok {
		err = protocolError(i18n.T("Server sandbox configuration lacks backend diagnostics; cannot confirm result"), resp.StatusCode, "")
	} else {
		for _, field := range []string{"e2b", "limits", "limit_caps"} {
			if _, ok := resp.Data[field].(map[string]any); !ok {
				err = protocolError(i18n.T("Server sandbox configuration is incomplete; cannot confirm result"), resp.StatusCode, "")
			}
		}
		if _, exists := resp.Data["backend"]; !exists {
			err = protocolError(i18n.T("Server did not return the saved sandbox backend"), resp.StatusCode, "")
		}
	}
	if err != nil {
		if action == "update" || action == "test" {
			err = classifyWriteError(err)
		}
		return nil, err
	}
	data := redactConnectionSecret(resp.Data, target.secret())
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
		return i18n.T("Server requires a Box SDK and Runtime upgrade to configure workspace sandbox")
	case "Custom E2B configuration is restricted by platform policy":
		return i18n.T("Current platform policy disallows custom E2B connections")
	case "A new API key is required when E2B addresses change":
		return i18n.T("Changing E2B URLs requires a new E2B API Key")
	case "The selected sandbox backend is unavailable":
		return i18n.T("Selected sandbox backend unavailable; inspect configuration or check connectivity")
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
