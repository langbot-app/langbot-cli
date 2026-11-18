package api

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"path/filepath"
	"strings"

	"github.com/langbot-app/langbot-cli/internal/i18n"
	"github.com/langbot-app/langbot-cli/internal/result"
)

const MaxArtifactUploadBytes = maxUploadBytes

// UploadArtifact preserves the file's media type so Core can distinguish images from documents.
func (c Client) UploadArtifact(ctx context.Context, target Target, filename string, file io.Reader) (map[string]any, error) {
	reader := bufio.NewReader(file)
	mediaType := mime.TypeByExtension(strings.ToLower(filepath.Ext(filename)))
	if mediaType == "" {
		prefix, err := reader.Peek(512)
		if err != nil && err != io.EOF {
			return nil, result.New("input", i18n.T("Failed to read upload file"))
		}
		mediaType = http.DetectContentType(prefix)
	}
	mediaType, _, _ = strings.Cut(mediaType, ";")
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	header := textproto.MIMEHeader{}
	header.Set("Content-Disposition", multipart.FileContentDisposition("file", filename))
	header.Set("Content-Type", mediaType)
	part, err := writer.CreatePart(header)
	if err != nil {
		return nil, result.New("input", i18n.T("Unable to construct upload request"))
	}
	n, err := io.Copy(part, io.LimitReader(reader, MaxArtifactUploadBytes+1))
	if err != nil {
		return nil, result.New("input", i18n.T("Failed to read upload file"))
	}
	if n > MaxArtifactUploadBytes {
		return nil, result.New("input", i18n.T("Upload file is too large"))
	}
	if err := writer.Close(); err != nil {
		return nil, result.New("input", i18n.T("Unable to construct upload request"))
	}
	value, err := c.postUpload(ctx, target, "/api/v1/artifacts", body.Bytes(), writer.FormDataContentType())
	if err != nil {
		return nil, err
	}
	metadata, _ := value.(map[string]any)
	id, _ := metadata["id"].(string)
	if strings.TrimSpace(id) == "" {
		return nil, result.New("incompatible", i18n.T("Artifact upload returned no ID"))
	}
	return metadata, nil
}

func (c Client) OperationUpload(ctx context.Context, target Target, operation, filename string, file io.Reader) (any, error) {
	path := map[string]string{
		"plugin.config_file.upload": "/api/v1/plugins/config-files",
		"file.image.upload":         "/api/v1/files/images",
		"skill.zip.preview":         "/api/v1/skills/install/upload/preview",
	}[operation]
	if path == "" {
		return nil, result.New("input", i18n.T("Unknown upload operation"))
	}
	body, contentType, err := multipartBody(filename, file, nil, maxUploadBytes)
	if err != nil {
		return nil, err
	}
	return c.postUpload(ctx, target, path, body, contentType)
}

func (c Client) postUpload(ctx context.Context, target Target, path string, body []byte, contentType string) (any, error) {
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
