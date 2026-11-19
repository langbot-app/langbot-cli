package app

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/langbot-app/langbot-cli/internal/api"
	"github.com/langbot-app/langbot-cli/internal/i18n"
	"github.com/langbot-app/langbot-cli/internal/result"
	"go.yaml.in/yaml/v3"
)

func (session *OperationSession) PluginApprovalHint() map[string]any {
	return map[string]any{
		"url":            strings.TrimRight(session.Target.Endpoint, "/") + "/home/extensions",
		"workspace_uuid": session.preflight.Identity.WorkspaceUUID,
		"message":        i18n.T("Review permissions in this Workspace's WebUI, or use lbctl plugin permissions confirm. Run this command again to check the resulting state."),
	}
}

func (session *OperationSession) ConfirmPlugin(ctx context.Context, author, name string, capabilities, operations []string) (any, error) {
	if session.Target.UserToken == "" && !session.preflight.Capabilities.Operations["plugin.permissions.confirm_api_key"] {
		return nil, result.New("incompatible", i18n.T("Core does not yet support confirming plugin permissions with an API Key"))
	}
	if err := session.Authorize(map[string]any{"side_effect": true, "permission": "resource.manage"}); err != nil {
		return nil, err
	}
	body := map[string]any{"author": author, "plugin_name": name}
	value, err := session.Client.OperationCall(ctx, session.Target, "plugin.permissions.get", body)
	if err != nil {
		return nil, err
	}
	permissions, ok := value.(map[string]any)
	if !ok {
		return nil, result.New("incompatible", i18n.T("Invalid plugin permission response format"))
	}
	compat, _ := permissions["compatibility_mode"].(bool)
	if compat || capabilities == nil && operations == nil {
		capabilities, operations = []string{}, []string{}
		rows, ok := permissions["permissions"].([]any)
		if !ok {
			return nil, result.New("incompatible", i18n.T("Plugin permission manifest is missing"))
		}
		for _, item := range rows {
			row, ok := item.(map[string]any)
			if !ok {
				return nil, result.New("incompatible", i18n.T("Invalid plugin permission entry"))
			}
			identifier, ok := row["id"].(string)
			if !ok || identifier == "" {
				return nil, result.New("incompatible", i18n.T("Plugin permission ID is missing"))
			}
			switch row["category"] {
			case "capabilities":
				capabilities = append(capabilities, identifier)
			case "workspace_operations":
				operations = append(operations, identifier)
			default:
				return nil, result.New("incompatible", i18n.T("Unsupported plugin permission category"))
			}
		}
	}
	if capabilities == nil {
		capabilities = []string{}
	}
	if operations == nil {
		operations = []string{}
	}
	body["capabilities"], body["workspace_operations"], body["confirm"] = capabilities, operations, true
	value, err = session.Client.OperationCall(ctx, session.Target, "plugin.permissions.update", body)
	if err != nil {
		return nil, err
	}
	verified, ok := value.(map[string]any)
	if !ok || verified["enabled"] != true || verified["pending_confirmation"] != false {
		return value, result.New("server", i18n.T("Plugin not enabled after confirming permissions; inspect plugin permission status"))
	}
	return value, nil
}

func (session *OperationSession) verifyUpgradedPlugin(ctx context.Context, author, name string, options WaitOptions, data map[string]any) error {
	client, target := session.Client, session.Target
	data["readiness"] = "unknown"
	plugin, err := client.PluginGet(ctx, target, author, name)
	if err != nil {
		return err
	}
	data["plugin"] = plugin
	if plugin["pending_confirmation"] == true || plugin["status"] == "pending_confirmation" {
		data["readiness"] = "pending_confirmation"
		if !options.ConfirmPermissions {
			permissions, err := client.OperationCall(ctx, target, "plugin.permissions.get", map[string]any{"author": author, "plugin_name": name})
			if err != nil {
				return err
			}
			data["permissions"] = permissions
			return result.New("precondition", i18n.T("Upgrade installed. Review pending plugin permissions"))
		}
		permissions, err := session.ConfirmPlugin(ctx, author, name, nil, nil)
		data["permissions"] = permissions
		data["readiness"] = "unknown"
		if err != nil {
			// Confirmation can take effect before the response is lost. Only read back.
			if latest, readErr := client.PluginGet(ctx, target, author, name); readErr == nil {
				data["plugin"] = latest
				if latest["pending_confirmation"] == true || latest["status"] == "pending_confirmation" {
					data["readiness"] = "pending_confirmation"
				}
			}
			return err
		}
		plugin, err = client.PluginGet(ctx, target, author, name)
		if err != nil {
			return err
		}
		data["plugin"] = plugin
	}
	interval := options.PollInterval
	if interval <= 0 {
		interval = time.Second
	}
	for plugin["enabled"] == true && (plugin["status"] == "mounted" || plugin["status"] == "unmounted") {
		select {
		case <-ctx.Done():
			return result.New("network", i18n.T("Stopped waiting for plugin initialization"))
		case <-time.After(interval):
		}
		plugin, err = client.PluginGet(ctx, target, author, name)
		if err != nil {
			return err
		}
		data["plugin"] = plugin
	}
	version, _ := plugin["version"].(string)
	installedVersion, _ := plugin["installed_version"].(string)
	if version == "" || installedVersion != "" && installedVersion != version || plugin["enabled"] != true || plugin["status"] != "initialized" || plugin["pending_confirmation"] == true || plugin["component_count"] == nil {
		return result.New("precondition", i18n.T("Upgrade installed. Runtime readiness could not be verified"))
	}
	data["readiness"] = "ready"
	if previous, ok := data["previous_version"].(string); ok && previous != "" {
		data["version_changed"] = previous != version
	}
	return nil
}

func pluginArchiveIdentity(content []byte) (string, string, error) {
	archive, err := zip.NewReader(bytes.NewReader(content), int64(len(content)))
	if err != nil {
		return "", "", result.New("input", i18n.T("Plugin package is not a valid ZIP"))
	}
	for _, entry := range archive.File {
		if entry.Name != "manifest.yaml" {
			continue
		}
		reader, err := entry.Open()
		if err != nil {
			return "", "", result.New("input", i18n.T("Unable to read plugin manifest"))
		}
		var manifest struct {
			Metadata struct {
				Author string `yaml:"author"`
				Name   string `yaml:"name"`
			} `yaml:"metadata"`
		}
		err = yaml.NewDecoder(io.LimitReader(reader, 1<<20)).Decode(&manifest)
		reader.Close()
		if err != nil || manifest.Metadata.Author == "" || manifest.Metadata.Name == "" {
			break
		}
		return manifest.Metadata.Author, manifest.Metadata.Name, nil
	}
	return "", "", result.New("input", i18n.T("Plugin package lacks a valid manifest.yaml identity"))
}

func (s *Service) pluginInstallIdentity(ctx context.Context, operation string, body map[string]any, filename string) (string, string, error) {
	if operation == "plugin.install.marketplace" {
		author, _ := body["plugin_author"].(string)
		name, _ := body["plugin_name"].(string)
		if author == "" || name == "" {
			return "", "", result.New("input", i18n.T("plugin_author and plugin_name are required"))
		}
		return author, name, nil
	}
	if filename != "" {
		content, err := os.ReadFile(filename)
		if err != nil {
			return "", "", result.New("input", i18n.T("Unable to read plugin package"))
		}
		return pluginArchiveIdentity(content)
	}
	owner, _ := body["owner"].(string)
	repo, _ := body["repo"].(string)
	asset := fmt.Sprint(body["asset_id"])
	if owner == "" || repo == "" || asset == "<nil>" {
		return "", "", result.New("input", i18n.T("GitHub automatic confirmation requires an explicit release asset"))
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.github.com/repos/"+url.PathEscape(owner)+"/"+url.PathEscape(repo)+"/releases/assets/"+url.PathEscape(asset), nil)
	if err != nil {
		return "", "", result.New("input", i18n.T("Invalid GitHub asset"))
	}
	request.Header.Set("Accept", "application/octet-stream")
	client := &http.Client{Transport: s.deps.Transport, Timeout: time.Minute, CheckRedirect: func(request *http.Request, previous []*http.Request) error {
		host := request.URL.Hostname()
		if len(previous) >= 5 || request.URL.Scheme != "https" || !(host == "github.com" || host == "api.github.com" || strings.HasSuffix(host, ".githubusercontent.com")) {
			return http.ErrUseLastResponse
		}
		return nil
	}}
	response, err := client.Do(request)
	if err != nil {
		return "", "", result.New("network", i18n.T("Unable to download GitHub plugin package to determine permission confirmation target"))
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return "", "", result.New("server", i18n.T("GitHub plugin package download failed"))
	}
	content, err := io.ReadAll(io.LimitReader(response.Body, maxExtensionUploadBytes+1))
	if err != nil || int64(len(content)) > maxExtensionUploadBytes {
		return "", "", result.New("input", i18n.T("GitHub plugin package is unreadable or too large"))
	}
	return pluginArchiveIdentity(content)
}

func operationSession(preflight WritePreflight, client api.Client, target api.Target) *OperationSession {
	return &OperationSession{Client: client, Target: target, preflight: preflight}
}
