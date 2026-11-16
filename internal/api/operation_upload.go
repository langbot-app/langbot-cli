package api

import (
	"bytes"
	"context"
	"io"
	"net/http"

	"github.com/langbot-app/langbot-cli/internal/result"
)

func (c Client) OperationUpload(ctx context.Context, target Target, operation, filename string, file io.Reader) (any, error) {
	path := map[string]string{
		"plugin.config_file.upload": "/api/v1/plugins/config-files",
		"file.image.upload":         "/api/v1/files/images",
		"skill.zip.preview":         "/api/v1/skills/install/upload/preview",
	}[operation]
	if path == "" {
		return nil, result.New("input", "未知上传操作")
	}
	body, contentType, err := multipartBody(filename, file, nil, maxUploadBytes)
	if err != nil {
		return nil, err
	}
	response, cancel, err := c.applicationRequest(ctx, target, http.MethodPost, path, bytes.NewReader(body), contentType)
	if err != nil {
		return nil, err
	}
	defer cancel()
	defer response.Body.Close()
	return applicationResponse(response, target, false)
}

func (c Client) KnowledgeMigration(ctx context.Context, target Target, execute bool, body map[string]any) (any, error) {
	method, path := http.MethodGet, "/api/v1/knowledge/migration/status"
	if execute {
		method, path = http.MethodPost, "/api/v1/knowledge/migration/execute"
	}
	return c.applicationJSON(ctx, target, method, path, body, false)
}
