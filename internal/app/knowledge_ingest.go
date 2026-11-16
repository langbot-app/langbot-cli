package app

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/langbot-app/langbot-cli/internal/i18n"
	"github.com/langbot-app/langbot-cli/internal/result"
)

type ingestFile struct {
	path string
	size int64
}

// KnowledgeBaseIngestFiles preserves the single-file result and aggregates batch inputs.
func (s *Service) KnowledgeBaseIngestFiles(ctx context.Context, identifier string, filenames []string, directory, parser string, dryRun bool, wait WaitOptions, options CheckOptions) (Result, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	files, err := collectIngestFiles(ctx, filenames, directory)
	if err != nil {
		return Result{}, err
	}
	if directory == "" && len(files) == 1 {
		return s.KnowledgeBaseIngest(ctx, identifier, files[0].path, parser, dryRun, wait, options)
	}
	for _, file := range files {
		if wait.Wait && strings.EqualFold(filepath.Ext(file.path), ".zip") {
			return Result{}, result.New("input", i18n.T("Batch --wait cannot confirm every task inside ZIP; extract it first and import with --dir"))
		}
	}
	preflight, base, err := s.prepareKnowledgeBaseIngest(ctx, identifier, wait.Wait && !dryRun, options)
	if err != nil {
		return Result{Meta: preflight.Meta()}, err
	}
	counts := map[string]int{"total": len(files), "planned": 0, "submitted": 0, "completed": 0, "failed": 0, "unknown": 0, "pending": 0, "not_started": 0}
	rows := make([]map[string]any, 0, len(files))
	var firstError *result.Error
	stopped, wrote, unknownWrite := false, false, false
	for _, file := range files {
		row := map[string]any{"path": file.path, "name": filepath.Base(file.path), "size_bytes": file.size}
		state := "not_started"
		if ctx.Err() != nil {
			stopped = true
			if firstError == nil {
				firstError = result.New("network", i18n.T("Batch ingestion cancelled; submitted server tasks were not cancelled"))
			}
		}
		if !stopped {
			value, callErr := s.ingestKnowledgeBaseFile(ctx, identifier, file.path, file.size, parser, dryRun, wait, preflight, base)
			data := value.Data.(map[string]any)
			for key, item := range data {
				if key != "knowledge_base" && key != "file" && key != "operation" {
					row[key] = item
				}
			}
			wrote = wrote || data["server_write"] == true
			switch {
			case callErr == nil && dryRun:
				state = "planned"
			case callErr == nil && data["step"] == "completed":
				state = "completed"
			case callErr == nil:
				state = "submitted"
			default:
				failure := result.AsError(callErr)
				row["error"] = failure
				if firstError == nil {
					firstError = failure
				}
				state = "failed"
				if failure.Type == "result_unknown" || data["submitted"] == "unknown" {
					state, unknownWrite = "unknown", true
					firstError = failure
				} else if data["submitted"] == true && failure.Type != "task_failed" && failure.Type != "task_cancelled" {
					state = "pending"
					firstError = failure
				}
				// Stop on uncertain writes or target-wide failures; never repeat a submission.
				stopped = state == "unknown" || state == "pending" || failure.Kind == "auth" || failure.Kind == "permission" || failure.Kind == "incompatible" || ctx.Err() != nil
			}
		}
		row["status"] = state
		counts[state]++
		rows = append(rows, row)
	}
	var serverWrite any = wrote
	if !wrote && unknownWrite {
		serverWrite = "unknown"
	}
	value := Result{Data: map[string]any{
		"operation": "knowledge_base.ingest", "batch": true, "dry_run": dryRun,
		"knowledge_base": base,
		"server_write":   serverWrite, "files": rows, "summary": counts,
	}, Meta: preflight.Meta()}
	if dryRun {
		value.Data.(map[string]any)["business_validation"] = "not_run"
	}
	if firstError != nil {
		failure := result.New(firstError.Kind, i18n.T("Batch ingestion partially failed; inspect per-file results; do not resubmit files already submitted or with unknown results"))
		failure.Type = "batch_incomplete"
		return value, failure
	}
	return value, nil
}

func (s *Service) prepareKnowledgeBaseIngest(ctx context.Context, identifier string, wait bool, options CheckOptions) (WritePreflight, map[string]any, error) {
	preflight, err := s.WritePreflight(ctx, "knowledge_base.file.store", options)
	if err != nil {
		return preflight, nil, err
	}
	for _, capability := range []string{"file.document.upload", "knowledge_base.get"} {
		if err := requireCapability(preflight, capability); err != nil {
			return preflight, nil, err
		}
	}
	if wait {
		if err := requireCapability(preflight, "task.get"); err != nil {
			return preflight, nil, err
		}
	}
	client, target := s.writeClient(preflight)
	base, err := client.KnowledgeBase(ctx, target, identifier)
	return preflight, base, err
}

func collectIngestFiles(ctx context.Context, filenames []string, directory string) ([]ingestFile, error) {
	if len(filenames) == 0 && directory == "" {
		return nil, result.New("input", i18n.T("Provide at least one --file or --dir; --file is repeatable"))
	}
	files := []ingestFile{}
	seen := map[string]bool{}
	add := func(path string) error {
		if strings.TrimSpace(path) == "" || path == "-" {
			return result.New("input", i18n.T("--file must specify a local file, not stdin"))
		}
		info, err := os.Stat(path)
		if err != nil || !info.Mode().IsRegular() {
			return result.New("input", i18n.T("Unable to read upload file, or selected path is not a regular file"))
		}
		if info.Size() > maxExtensionUploadBytes {
			return result.New("input", i18n.T("Upload file exceeds the 10MB limit; split it before submitting; this batch was not uploaded"))
		}
		absolute, err := filepath.Abs(path)
		if err != nil {
			return result.New("input", i18n.T("Unable to resolve upload file path"))
		}
		if runtime.GOOS == "windows" {
			absolute = strings.ToLower(absolute)
		}
		if !seen[absolute] {
			seen[absolute] = true
			files = append(files, ingestFile{path: filepath.Clean(path), size: info.Size()})
		}
		return nil
	}
	for _, path := range filenames {
		if err := add(path); err != nil {
			return nil, err
		}
	}
	if directory != "" {
		directory = filepath.Clean(directory)
		info, err := os.Lstat(directory)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return nil, result.New("input", i18n.T("--dir must be a readable directory, not a symbolic link"))
		}
		err = filepath.WalkDir(directory, func(path string, entry fs.DirEntry, walkErr error) error {
			if ctx.Err() != nil {
				return result.New("network", i18n.T("Directory scan cancelled"))
			}
			if walkErr != nil {
				return result.New("input", i18n.T("Unable to traverse upload directory; batch not uploaded"))
			}
			if path != directory && strings.HasPrefix(entry.Name(), ".") {
				if entry.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			if entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
				return nil
			}
			return add(path)
		})
		if err != nil {
			return nil, err
		}
	}
	if len(files) == 0 {
		return nil, result.New("input", i18n.T("No uploadable regular files in directory (dotfiles and symbolic links skipped)"))
	}
	return files, nil
}
