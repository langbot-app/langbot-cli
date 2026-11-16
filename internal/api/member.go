package api

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/langbot-app/langbot-cli/internal/i18n"
	"github.com/langbot-app/langbot-cli/internal/result"
)

func (target Target) secret() string {
	if target.UserToken != "" {
		return target.UserToken
	}
	return target.APIKey
}

func (c Client) MemberCapabilities(ctx context.Context, target Target) (Capabilities, error) {
	value, err := c.OperationList(ctx, target, "", "")
	if err != nil {
		return Capabilities{}, err
	}
	data, _ := value.(map[string]any)
	operations, ok := data["operations"].([]any)
	if !ok {
		return Capabilities{}, result.New("incompatible", i18n.T("Invalid member operation catalog response"))
	}
	capabilities := Capabilities{SchemaVersion: 1, Operations: map[string]bool{"operation.invoke": true}}
	for _, value := range operations {
		operation, _ := value.(map[string]any)
		identifier, _ := operation["id"].(string)
		if identifier != "" {
			capabilities.Operations[identifier] = true
		}
	}
	return capabilities, nil
}

func (c Client) MemberContext(ctx context.Context, target Target) (Context, error) {
	value, err := c.applicationJSON(ctx, target, http.MethodGet, "/api/v1/workspaces/current", nil, false)
	if err != nil {
		return Context{}, err
	}
	data, _ := value.(map[string]any)
	workspace, _ := data["workspace"].(map[string]any)
	identifier, _ := workspace["uuid"].(string)
	permissions, ok := data["permissions"].([]any)
	if identifier == "" || !ok {
		return Context{}, result.New("incompatible", i18n.T("Invalid member Workspace response"))
	}
	identity := Context{WorkspaceUUID: identifier}
	for _, value := range permissions {
		permission, ok := value.(string)
		if !ok {
			return Context{}, result.New("incompatible", i18n.T("Invalid member permission response"))
		}
		identity.Permissions = append(identity.Permissions, permission)
	}
	return identity, nil
}

func (c Client) Login(ctx context.Context, target Target, email, password string, prompt func() (string, error)) (string, error) {
	path := "/api/v1/user/auth"
	body := map[string]any{"user": email, "password": password}
	for attempt := 0; attempt < 2; attempt++ {
		reader, err := applicationBody(body)
		if err != nil {
			return "", err
		}
		response, cancel, err := c.applicationRequest(ctx, target, http.MethodPost, path, reader, "application/json")
		if err != nil {
			return "", err
		}
		data, large, readErr := readLimited(response.Body)
		response.Body.Close()
		cancel()
		if readErr != nil || large {
			return "", result.New("network", i18n.T("Failed to read login response"))
		}
		var parsed struct {
			Code json.RawMessage `json:"code"`
			Data struct {
				Token     string `json:"token"`
				Challenge string `json:"challenge_token"`
			} `json:"data"`
		}
		if json.Unmarshal(data, &parsed) != nil {
			return "", result.New("incompatible", i18n.T("Invalid login response"))
		}
		if string(parsed.Code) == `"password_login_disabled"` {
			failure := result.New("auth", i18n.T("Cloud does not support password login; use lbctl auth set-token --stdin"))
			failure.Type = "password_login_disabled"
			return "", failure
		}
		if attempt == 0 && string(parsed.Code) == `"totp_required"` && parsed.Data.Challenge != "" {
			code, err := prompt()
			if err != nil {
				return "", err
			}
			path = "/api/v1/user/totp/verify"
			body = map[string]any{"user": email, "code": code, "challenge_token": parsed.Data.Challenge}
			continue
		}
		if response.StatusCode >= 200 && response.StatusCode < 300 && isSuccessCode(parsed.Code) && parsed.Data.Token != "" {
			return parsed.Data.Token, nil
		}
		return "", result.New("auth", i18n.T("Login failed; check account, password or TOTP / recovery code"))
	}
	return "", result.New("auth", i18n.T("Login failed"))
}
