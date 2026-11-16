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
	"github.com/langbot-app/langbot-cli/internal/result"
	"go.yaml.in/yaml/v3"
)

func (session *OperationSession) ConfirmPlugin(ctx context.Context, author, name string, capabilities, operations []string) (any, error) {
	if !session.preflight.Capabilities.Operations["plugin.permissions.confirm_api_key"] {
		return nil, result.New("incompatible", "Core 尚不支持 API Key 确认插件权限")
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
		return nil, result.New("incompatible", "插件权限响应格式无效")
	}
	compat, _ := permissions["compatibility_mode"].(bool)
	if compat || capabilities == nil && operations == nil {
		capabilities, operations = []string{}, []string{}
		rows, ok := permissions["permissions"].([]any)
		if !ok {
			return nil, result.New("incompatible", "插件权限清单缺失")
		}
		for _, item := range rows {
			row, ok := item.(map[string]any)
			if !ok {
				return nil, result.New("incompatible", "插件权限条目无效")
			}
			identifier, ok := row["id"].(string)
			if !ok || identifier == "" {
				return nil, result.New("incompatible", "插件权限 ID 缺失")
			}
			switch row["category"] {
			case "capabilities":
				capabilities = append(capabilities, identifier)
			case "workspace_operations":
				operations = append(operations, identifier)
			default:
				return nil, result.New("incompatible", "插件权限类别不支持")
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
		return value, result.New("server", "权限确认后插件未启用，请检查插件权限状态")
	}
	return value, nil
}

func pluginArchiveIdentity(content []byte) (string, string, error) {
	archive, err := zip.NewReader(bytes.NewReader(content), int64(len(content)))
	if err != nil {
		return "", "", result.New("input", "插件包不是有效 ZIP")
	}
	for _, entry := range archive.File {
		if entry.Name != "manifest.yaml" {
			continue
		}
		reader, err := entry.Open()
		if err != nil {
			return "", "", result.New("input", "无法读取插件 manifest")
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
	return "", "", result.New("input", "插件包缺少有效的 manifest.yaml 身份")
}

func (s *Service) pluginInstallIdentity(ctx context.Context, operation string, body map[string]any, filename string) (string, string, error) {
	if operation == "plugin.install.marketplace" {
		author, _ := body["plugin_author"].(string)
		name, _ := body["plugin_name"].(string)
		if author == "" || name == "" {
			return "", "", result.New("input", "必须提供 plugin_author 和 plugin_name")
		}
		return author, name, nil
	}
	if filename != "" {
		content, err := os.ReadFile(filename)
		if err != nil {
			return "", "", result.New("input", "无法读取插件包")
		}
		return pluginArchiveIdentity(content)
	}
	owner, _ := body["owner"].(string)
	repo, _ := body["repo"].(string)
	asset := fmt.Sprint(body["asset_id"])
	if owner == "" || repo == "" || asset == "<nil>" {
		return "", "", result.New("input", "GitHub 自动确认需要明确的 release asset")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.github.com/repos/"+url.PathEscape(owner)+"/"+url.PathEscape(repo)+"/releases/assets/"+url.PathEscape(asset), nil)
	if err != nil {
		return "", "", result.New("input", "GitHub asset 无效")
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
		return "", "", result.New("network", "无法下载 GitHub 插件包以确定权限确认对象")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return "", "", result.New("server", "GitHub 插件包下载失败")
	}
	content, err := io.ReadAll(io.LimitReader(response.Body, maxExtensionUploadBytes+1))
	if err != nil || int64(len(content)) > maxExtensionUploadBytes {
		return "", "", result.New("input", "GitHub 插件包不可读或过大")
	}
	return pluginArchiveIdentity(content)
}

func operationSession(preflight WritePreflight, client api.Client, target api.Target) *OperationSession {
	return &OperationSession{Client: client, Target: target, preflight: preflight}
}
