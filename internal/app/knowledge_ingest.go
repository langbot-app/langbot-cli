package app

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"

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
			return Result{}, result.New("input", "批量 --wait 无法确认 ZIP 内全部任务；请先解压，再通过 --dir 导入")
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
				firstError = result.New("network", "批量入库已取消，已提交的服务端任务未取消")
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
		failure := result.New(firstError.Kind, "批量入库未全部成功，请查看逐文件结果；已提交或结果未知的文件不要直接重复提交")
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
		return nil, result.New("input", "至少提供一个 --file 或 --dir；--file 可重复指定")
	}
	files := []ingestFile{}
	seen := map[string]bool{}
	add := func(path string) error {
		if strings.TrimSpace(path) == "" || path == "-" {
			return result.New("input", "--file 必须指定本地文件，不能使用 stdin")
		}
		info, err := os.Stat(path)
		if err != nil || !info.Mode().IsRegular() {
			return result.New("input", "无法读取上传文件，或所选路径不是普通文件")
		}
		if info.Size() > maxExtensionUploadBytes {
			return result.New("input", "上传文件超过 10MB 限制，请拆分后再提交；本批次未上传")
		}
		absolute, err := filepath.Abs(path)
		if err != nil {
			return result.New("input", "无法解析上传文件路径")
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
			return nil, result.New("input", "--dir 必须指定可读取的目录，不能是符号链接")
		}
		err = filepath.WalkDir(directory, func(path string, entry fs.DirEntry, walkErr error) error {
			if ctx.Err() != nil {
				return result.New("network", "目录扫描已取消")
			}
			if walkErr != nil {
				return result.New("input", "无法遍历上传目录；本批次未上传")
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
		return nil, result.New("input", "目录中没有可上传的普通文件（点开头项和符号链接已跳过）")
	}
	return files, nil
}
