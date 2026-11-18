package command

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/langbot-app/langbot-cli/internal/api"
	"github.com/langbot-app/langbot-cli/internal/app"
	"github.com/langbot-app/langbot-cli/internal/i18n"
	"github.com/langbot-app/langbot-cli/internal/result"
)

type debugAttachment struct {
	Name       string `json:"name"`
	Status     string `json:"status"`
	ArtifactID string `json:"artifact_id,omitempty"`
	ExpiresAt  any    `json:"expires_at,omitempty"`
	file       *os.File
}

type debugAttachments []debugAttachment

func (files debugAttachments) close() {
	for i := range files {
		if files[i].file != nil {
			files[i].file.Close()
			files[i].file = nil
		}
	}
}

// Validate the whole batch before uploading anything. Keep the opened files so
// replacing a path during confirmation cannot silently change the selected file.
func prepareDebugAttachments(body map[string]any, paths []string) (debugAttachments, error) {
	if len(paths) == 0 {
		return nil, nil
	}
	payload, ok := body["payload"].(map[string]any)
	if !ok {
		if _, exists := body["payload"]; exists {
			return nil, result.New("input", i18n.T("Debug payload must be an object"))
		}
		payload = map[string]any{}
		body["payload"] = payload
	}
	var ids []any
	if value, exists := payload["artifact_ids"]; exists {
		ids, ok = value.([]any)
		if !ok {
			return nil, result.New("input", i18n.T("artifact_ids must be a list of distinct non-empty strings"))
		}
	}
	if len(ids)+len(paths) > 20 {
		return nil, result.New("input", i18n.T("Agent debug accepts at most 20 attachment references"))
	}
	seen := make(map[string]bool, len(ids))
	for _, value := range ids {
		id, ok := value.(string)
		if !ok || strings.TrimSpace(id) == "" || seen[id] {
			return nil, result.New("input", i18n.T("artifact_ids must be a list of distinct non-empty strings"))
		}
		seen[id] = true
	}
	files := make(debugAttachments, 0, len(paths))
	for _, path := range paths {
		file, err := openDebugAttachment(path)
		if err != nil {
			files.close()
			return nil, err
		}
		files = append(files, debugAttachment{Name: filepath.Base(path), Status: "pending", file: file})
	}
	return files, nil
}

func openDebugAttachment(path string) (*os.File, error) {
	name := filepath.Base(path)
	if path == "-" || strings.Contains(name, "\\") || strings.IndexFunc(name, unicode.IsControl) >= 0 || !utf8.ValidString(name) || utf8.RuneCountInString(name) > 255 {
		return nil, result.New("input", i18n.T("Invalid attachment filename"))
	}
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return nil, result.New("input", i18n.T("Attachment must be a readable regular file: ")+name)
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, result.New("input", i18n.T("Attachment must be a readable regular file: ")+name)
	}
	info, err = file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > api.MaxArtifactUploadBytes {
		file.Close()
		return nil, result.New("input", i18n.T("Attachment must be a regular file of at most 10 MiB: ")+name)
	}
	return file, nil
}

func (files debugAttachments) upload(ctx context.Context, session *app.OperationSession, payload map[string]any) error {
	defer files.close()
	ids, _ := payload["artifact_ids"].([]any)
	for i := range files {
		if ctx.Err() != nil {
			return result.New("network", i18n.T("Request cancelled"))
		}
		file := &files[i]
		metadata, err := session.Client.UploadArtifact(ctx, session.Target, file.Name, file.file)
		if err != nil {
			file.Status = debugFailureStatus(err)
			return err
		}
		file.Status = "uploaded"
		file.ArtifactID = metadata["id"].(string)
		file.ExpiresAt = metadata["expires_at"]
		for _, id := range ids {
			if id == file.ArtifactID {
				return result.New("incompatible", i18n.T("Artifact upload returned a duplicate ID"))
			}
		}
		ids = append(ids, file.ArtifactID)
	}
	payload["artifact_ids"] = ids
	return nil
}

func debugFailureStatus(err error) string {
	failure := result.AsError(err)
	if failure.Kind == "input" || failure.Kind == "auth" || failure.Kind == "permission" ||
		(failure.HTTPStatus >= 400 && failure.HTTPStatus < 500 && failure.HTTPStatus != 408) ||
		(failure.Kind == "server" && failure.HTTPStatus < 300) {
		return "failed"
	}
	return "unknown"
}

func (files debugAttachments) report(status string) map[string]any {
	return map[string]any{"attachments": files, "debug_status": status, "completed": status == "completed"}
}
