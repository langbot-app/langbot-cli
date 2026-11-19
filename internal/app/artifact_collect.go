package app

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/langbot-app/langbot-cli/internal/api"
	"github.com/langbot-app/langbot-cli/internal/i18n"
	"github.com/langbot-app/langbot-cli/internal/result"
)

type artifactCollectItem struct {
	ID   string `json:"id"`
	Name string `json:"name,omitempty"`
}

type artifactCollectRow struct {
	ID             string        `json:"id"`
	File           string        `json:"file"`
	Occurrences    int           `json:"occurrences"`
	Status         string        `json:"status"`
	Stage          string        `json:"stage,omitempty"`
	Name           string        `json:"name,omitempty"`
	MetadataID     string        `json:"metadata_id,omitempty"`
	Size           int64         `json:"size"`
	Digest         string        `json:"digest,omitempty"`
	ReceivedBytes  int64         `json:"received_bytes"`
	ReceivedSHA256 string        `json:"received_sha256,omitempty"`
	Verified       bool          `json:"verified"`
	Error          *result.Error `json:"error,omitempty"`
}

// ArtifactCollect delivers only manifest IDs through one captured identity.
func (s *Service) ArtifactCollect(ctx context.Context, manifest map[string]any, directory string, options CheckOptions) (Result, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	rows, count, err := parseArtifactManifest(manifest)
	if err != nil {
		return Result{}, err
	}
	if strings.TrimSpace(directory) == "" {
		return Result{}, result.New("input", i18n.T("An Artifact output directory is required"))
	}
	directory, err = filepath.Abs(directory)
	if err != nil {
		return Result{}, result.New("input", i18n.T("Unable to open Artifact output directory"))
	}
	info, statErr := os.Stat(directory)
	if statErr == nil && !info.IsDir() || statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
		return Result{}, result.New("input", i18n.T("Unable to open Artifact output directory"))
	}
	for index := range rows {
		rows[index].File = filepath.Join(directory, rows[index].File)
	}
	data := map[string]any{"operation": "artifact.collect", "directory": directory, "files": rows, "summary": artifactCollectCounts(rows, count)}
	session, err := s.OpenOperations(ctx, options, false)
	if err != nil {
		return Result{Data: data}, err
	}
	if err := session.Authorize(map[string]any{"permission": "resource.view", "side_effect": false}); err != nil {
		return session.Result(data), err
	}
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return session.Result(data), result.New("input", i18n.T("Unable to open Artifact output directory"))
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		return session.Result(data), result.New("input", i18n.T("Unable to open Artifact output directory"))
	}
	defer root.Close()
	var firstError error
	for index := range rows {
		row := &rows[index]
		if ctx.Err() != nil {
			firstError = result.New("network", i18n.T("Artifact collection cancelled"))
			break
		}
		callErr := collectArtifact(ctx, session, root, row)
		if callErr == nil {
			continue
		}
		row.Status, row.Error = "error", result.AsError(callErr)
		if firstError == nil {
			firstError = callErr
		}
		if ctx.Err() != nil {
			row.Status = "cancelled"
			break
		}
		if row.Error.HTTPStatus == 401 {
			break
		}
	}
	data["summary"] = artifactCollectCounts(rows, count)
	return session.Result(data), firstError
}

func parseArtifactManifest(manifest map[string]any) ([]artifactCollectRow, int, error) {
	var document struct {
		Artifacts []artifactCollectItem `json:"artifacts"`
	}
	encoded, err := json.Marshal(manifest)
	if err != nil {
		return nil, 0, result.New("input", i18n.T("Invalid Artifact manifest"))
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&document) != nil || len(document.Artifacts) == 0 {
		return nil, 0, result.New("input", i18n.T("Invalid Artifact manifest"))
	}
	rows := []artifactCollectRow{}
	ids, names := map[string]int{}, map[string]bool{}
	for _, item := range document.Artifacts {
		item.ID = strings.TrimSpace(item.ID)
		if len([]rune(item.ID)) > 64 || api.ValidateResourceID(item.ID) != nil {
			return nil, 0, result.New("input", i18n.T("Artifact manifest IDs must be non-empty resource IDs of at most 64 characters"))
		}
		if item.Name == "" {
			item.Name = item.ID + ".bin"
			if !safeArtifactFilename(item.Name) {
				item.Name = fmt.Sprintf("%x.bin", sha256.Sum256([]byte(item.ID)))
			}
		}
		if !safeArtifactFilename(item.Name) {
			return nil, 0, result.New("input", i18n.T("Artifact manifest names must be safe single filenames"))
		}
		if index, exists := ids[item.ID]; exists {
			if rows[index].File != item.Name {
				return nil, 0, result.New("input", i18n.T("Duplicate Artifact IDs must use the same filename"))
			}
			rows[index].Occurrences++
			continue
		}
		key := strings.ToLower(item.Name)
		if names[key] {
			return nil, 0, result.New("input", i18n.T("Artifact manifest filenames must be unique"))
		}
		ids[item.ID], names[key] = len(rows), true
		rows = append(rows, artifactCollectRow{ID: item.ID, File: item.Name, Occurrences: 1, Status: "not_started"})
	}
	return rows, len(document.Artifacts), nil
}

func safeArtifactFilename(name string) bool {
	if !filepath.IsLocal(name) || name == "." || name == ".." || len(name) > 255 || strings.TrimRight(name, " .") != name || strings.ContainsAny(name, `/\:*?"<>|`) || strings.IndexFunc(name, func(value rune) bool { return value < 32 || value == 127 }) >= 0 {
		return false
	}
	stem := strings.ToUpper(strings.SplitN(name, ".", 2)[0])
	if stem == "CON" || stem == "PRN" || stem == "AUX" || stem == "NUL" || len(stem) == 4 && (strings.HasPrefix(stem, "COM") || strings.HasPrefix(stem, "LPT")) && stem[3] >= '0' && stem[3] <= '9' {
		return false
	}
	return true
}

func collectArtifact(ctx context.Context, session *OperationSession, root *os.Root, row *artifactCollectRow) error {
	row.Stage = "metadata"
	value, err := session.Client.OperationCall(ctx, session.Target, "artifact.get", map[string]any{"artifact_id": row.ID})
	if err != nil {
		return err
	}
	metadata, _ := value.(map[string]any)
	row.MetadataID, _ = metadata["id"].(string)
	row.Name, _ = metadata["name"].(string)
	row.Digest, _ = metadata["digest"].(string)
	number, hasSize := metadata["size"].(json.Number)
	size, sizeErr := number.Int64()
	if hasSize && sizeErr == nil {
		row.Size = size
	}
	decoded, digestErr := hex.DecodeString(row.Digest)
	if row.MetadataID != row.ID || !hasSize || sizeErr != nil || size < 0 || digestErr != nil || len(decoded) != sha256.Size {
		failure := result.New("incompatible", i18n.T("Invalid Artifact metadata identity, size or SHA-256"))
		failure.Type = "artifact_metadata_invalid"
		return failure
	}
	row.Size, row.Digest = size, strings.ToLower(row.Digest)
	if size > api.MaxArtifactBytes {
		failure := result.New("precondition", i18n.T("Artifact exceeds the 10MiB download limit"))
		failure.Type = "artifact_too_large"
		return failure
	}
	row.Stage = "download"
	temporary := ".lbctl-artifact-" + rand.Text()
	file, err := root.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return result.New("input", i18n.T("Unable to save Artifact content"))
	}
	defer root.Remove(temporary)
	defer file.Close()
	hash := sha256.New()
	limit := size
	if limit == 0 {
		limit = 1
	}
	row.ReceivedBytes, err = session.Client.ArtifactDownload(ctx, session.Target, row.ID, limit, io.MultiWriter(file, hash))
	row.ReceivedSHA256 = hex.EncodeToString(hash.Sum(nil))
	if err != nil {
		return err
	}
	row.Stage = "verify"
	if row.ReceivedBytes != size || row.ReceivedSHA256 != row.Digest {
		failure := result.New("incompatible", i18n.T("Artifact content size or SHA-256 does not match metadata"))
		failure.Type = "artifact_integrity_failed"
		return failure
	}
	if file.Sync() != nil || file.Close() != nil {
		return result.New("input", i18n.T("Unable to save Artifact content"))
	}
	if ctx.Err() != nil {
		return result.New("network", i18n.T("Artifact collection cancelled"))
	}
	row.Stage = "publish"
	name := filepath.Base(row.File)
	if err := root.Link(temporary, name); err != nil {
		if !errors.Is(err, os.ErrExist) || !existingArtifactMatches(root, name, size, row.Digest) {
			failure := result.New("input", i18n.T("Artifact destination exists with different content or cannot be published atomically"))
			failure.Type = "artifact_destination_conflict"
			return failure
		}
		row.Status = "already_present"
	} else {
		row.Status = "saved"
	}
	row.Verified, row.Stage = true, "completed"
	return nil
}

func existingArtifactMatches(root *os.Root, name string, size int64, digest string) bool {
	info, err := root.Lstat(name)
	if err != nil || !info.Mode().IsRegular() || info.Size() != size {
		return false
	}
	file, err := root.Open(name)
	if err != nil {
		return false
	}
	defer file.Close()
	hash := sha256.New()
	written, err := io.Copy(hash, io.LimitReader(file, size+1))
	return err == nil && written == size && hex.EncodeToString(hash.Sum(nil)) == digest
}

func artifactCollectCounts(rows []artifactCollectRow, input int) map[string]int {
	counts := map[string]int{"input_total": input, "total": len(rows), "duplicates": input - len(rows), "saved": 0, "already_present": 0, "error": 0, "cancelled": 0, "not_started": 0}
	for _, row := range rows {
		counts[row.Status]++
	}
	return counts
}
