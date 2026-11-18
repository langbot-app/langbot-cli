package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/gofrs/flock"
	"github.com/langbot-app/langbot-cli/internal/api"
	"github.com/langbot-app/langbot-cli/internal/i18n"
	"github.com/langbot-app/langbot-cli/internal/result"
)

type ingestFile struct {
	path string
	size int64
}

type ingestJournal struct {
	Version       int                 `json:"version"`
	InstanceUUID  string              `json:"instance_uuid"`
	WorkspaceUUID string              `json:"workspace_uuid"`
	KnowledgeBase string              `json:"knowledge_base_id"`
	Parser        string              `json:"parser_plugin_id"`
	Files         []ingestJournalFile `json:"files"`
}

type ingestJournalFile struct {
	Path         string `json:"path" yaml:"path"`
	Size         int64  `json:"size_bytes" yaml:"size_bytes"`
	SHA256       string `json:"sha256" yaml:"sha256"`
	FileID       string `json:"file_id,omitempty" yaml:"file_id,omitempty"`
	DocumentUUID string `json:"document_uuid,omitempty" yaml:"document_uuid,omitempty"`
	TaskID       string `json:"task_id,omitempty" yaml:"task_id,omitempty"`
	Phase        string `json:"phase" yaml:"phase"`
	Problem      string `json:"problem,omitempty" yaml:"problem,omitempty"`
}

type ingestJournalRun struct {
	journal      ingestJournal
	path         string
	client       api.Client
	target       api.Target
	wait         WaitOptions
	wrote        bool
	unknownWrite bool
}

// KnowledgeBaseIngestJournal uses durable document identity to resume one recorded input group.
func (s *Service) KnowledgeBaseIngestJournal(ctx context.Context, identifier string, filenames []string, directory, parser, journalPath string, resume, dryRun bool, wait WaitOptions, options CheckOptions) (Result, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if strings.TrimSpace(journalPath) == "" {
		return Result{}, result.New("input", i18n.T("Knowledge ingestion resume requires --journal PATH"))
	}
	path, err := filepath.Abs(journalPath)
	if err != nil {
		return Result{}, result.New("input", i18n.T("Unable to resolve ingestion journal path"))
	}
	files, err := collectIngestFiles(ctx, filenames, directory)
	if err != nil {
		return Result{}, err
	}
	inputs := make(map[string]ingestFile, len(files))
	expected := ingestJournal{Version: 1, KnowledgeBase: identifier, Parser: parser}
	for _, file := range files {
		if ctx.Err() != nil {
			return Result{}, result.New("network", i18n.T("Ingestion journal preparation cancelled"))
		}
		absolute, err := filepath.Abs(file.path)
		if err != nil {
			return Result{}, result.New("input", i18n.T("Unable to resolve upload file path"))
		}
		if ingestPathKey(absolute) == ingestPathKey(path) || ingestPathKey(absolute) == ingestPathKey(path+".lock") {
			continue
		}
		if strings.EqualFold(filepath.Ext(file.path), ".zip") {
			return Result{}, result.New("input", i18n.T("Journal ingestion requires individual documents. Extract ZIP files and use --dir"))
		}
		relative, err := filepath.Rel(filepath.Dir(path), absolute)
		if err != nil {
			return Result{}, result.New("input", i18n.T("Input files and ingestion journal must share a filesystem volume"))
		}
		row := ingestJournalFile{Path: filepath.ToSlash(relative), Size: file.size, Phase: "planned"}
		row.SHA256, err = hashIngestFile(file.path)
		if err != nil {
			return Result{}, err
		}
		expected.Files = append(expected.Files, row)
		inputs[ingestPathKey(row.Path)] = file
	}
	if len(inputs) == 0 {
		return Result{}, result.New("input", i18n.T("No document inputs remain after excluding the ingestion journal and lock"))
	}
	if err := checkIngestJournalPath(path); err != nil {
		return Result{}, err
	}
	if err := checkIngestJournalPath(path + ".lock"); err != nil {
		return Result{}, err
	}
	if !dryRun {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return Result{}, result.New("input", i18n.T("Unable to create ingestion journal directory"))
		}
		lock := flock.New(path + ".lock")
		locked, err := lock.TryLock()
		if err != nil {
			return Result{}, result.New("input", i18n.T("Unable to acquire ingestion journal lock"))
		}
		if !locked {
			return Result{}, result.New("precondition", i18n.T("Another process is using this ingestion journal"))
		}
		defer func() { _ = lock.Unlock() }()
		if err := checkIngestJournalPath(path); err != nil {
			return Result{}, err
		}
	}
	journal, err := readIngestJournal(path, resume)
	if err != nil {
		return Result{}, err
	}
	preflight, base, err := s.prepareKnowledgeBaseIngest(ctx, identifier, false, options)
	if err != nil {
		return Result{Meta: preflight.Meta()}, err
	}
	if err := requireCapability(preflight, "knowledge_base.file.list"); err != nil {
		return Result{Meta: preflight.Meta()}, err
	}
	expected.InstanceUUID, expected.WorkspaceUUID = preflight.Identity.InstanceUUID, preflight.Identity.WorkspaceUUID
	if expected.InstanceUUID == "" || expected.WorkspaceUUID == "" || base["uuid"] != identifier {
		return Result{Meta: preflight.Meta()}, result.New("incompatible", i18n.T("Ingestion journal requires a confirmed instance, Workspace and knowledge-base identity"))
	}
	if resume {
		if err := validateIngestJournal(journal, expected); err != nil {
			return Result{Meta: preflight.Meta()}, err
		}
	} else {
		journal = expected
	}
	client, target := s.writeClient(preflight)
	run := ingestJournalRun{journal: journal, path: path, client: client, target: target, wait: wait}
	if !dryRun && !resume {
		if err := run.save(); err != nil {
			return Result{Meta: preflight.Meta()}, err
		}
	}
	var firstError error
	for index := range run.journal.Files {
		row := &run.journal.Files[index]
		if dryRun {
			continue
		}
		if ctx.Err() != nil {
			firstError = taskStatusError("wait_cancelled", i18n.T("Ingestion journal wait cancelled. Server tasks continue"))
			break
		}
		if err := run.advance(ctx, row, inputs[ingestPathKey(row.Path)]); err != nil {
			if firstError == nil {
				firstError = err
			}
			if result.AsError(err).Type == "document_failed" {
				continue
			}
			break
		}
	}
	data := map[string]any{
		"operation": "knowledge_base.ingest", "batch": true, "dry_run": dryRun,
		"journal": path, "resumed": resume, "knowledge_base": base,
		"server_write": run.wrote, "files": run.journal.Files,
	}
	if !run.wrote && run.unknownWrite {
		data["server_write"] = "unknown"
	}
	counts := map[string]int{"total": len(run.journal.Files), "planned": 0, "uploaded": 0, "submitted": 0, "completed": 0, "failed": 0, "unresolved": 0}
	for _, row := range run.journal.Files {
		state := row.Phase
		if row.Problem != "" || state == "upload_intent" || state == "store_intent" {
			state = "unresolved"
		}
		counts[state]++
	}
	data["summary"] = counts
	if dryRun {
		data["business_validation"] = "not_run"
	}
	return Result{Data: data, Meta: preflight.Meta()}, firstError
}

func (run *ingestJournalRun) advance(ctx context.Context, row *ingestJournalFile, file ingestFile) error {
	if row.Phase == "upload_intent" {
		return journalUnresolved(i18n.T("A previous upload has no confirmed file ID. Preserve this journal for reconciliation"))
	}
	if row.Phase == "planned" {
		content, err := readIngestContent(file.path)
		if err != nil || int64(len(content)) != row.Size || ingestContentHash(content) != row.SHA256 {
			return result.New("precondition", i18n.T("An ingestion input changed before upload"))
		}
		if ctx.Err() != nil {
			return taskStatusError("wait_cancelled", i18n.T("Ingestion journal wait cancelled. Server tasks continue"))
		}
		row.Phase, row.Problem = "upload_intent", ""
		if err := run.save(); err != nil {
			return err
		}
		fileID, err := run.client.UploadDocument(ctx, run.target, filepath.Base(file.path), bytes.NewReader(content))
		if err != nil {
			row.Problem = "upload_unresolved"
			if knownIngestRejection(err) {
				row.Phase, row.Problem = "planned", ""
			} else {
				run.unknownWrite = true
			}
			if saveErr := run.save(); saveErr != nil {
				return saveErr
			}
			return err
		}
		run.wrote = true
		row.FileID, row.Phase = fileID, "uploaded"
		if err := run.save(); err != nil {
			return err
		}
	}
	if row.Phase == "uploaded" {
		if ctx.Err() != nil {
			return taskStatusError("wait_cancelled", i18n.T("Ingestion journal wait cancelled. Server tasks continue"))
		}
		row.Phase, row.Problem = "store_intent", ""
		if err := run.save(); err != nil {
			return err
		}
		taskID, err := run.client.KnowledgeBaseStoreFile(ctx, run.target, run.journal.KnowledgeBase, row.FileID, run.journal.Parser)
		if err != nil {
			row.Problem = "submission_unresolved"
			if knownIngestRejection(err) {
				row.Phase, row.Problem = "uploaded", ""
			} else {
				run.unknownWrite = true
			}
			if saveErr := run.save(); saveErr != nil {
				return saveErr
			}
			return err
		}
		run.wrote = true
		row.TaskID, row.Phase = taskID, "submitted"
		if err := run.save(); err != nil {
			return err
		}
	}
	waitCtx := ctx
	cancel := func() {}
	if run.wait.Wait {
		timeout := run.wait.WaitTimeout
		if timeout <= 0 {
			timeout = 5 * time.Minute
		}
		waitCtx, cancel = context.WithTimeout(ctx, timeout)
	}
	defer cancel()
	for {
		previous := *row
		err := run.verify(waitCtx, row)
		if *row != previous {
			if saveErr := run.save(); saveErr != nil {
				return saveErr
			}
		}
		if waitCtx.Err() != nil {
			if ctx.Err() != nil {
				return taskStatusError("wait_cancelled", i18n.T("Ingestion journal wait cancelled. Server tasks continue"))
			}
			return taskStatusError("wait_timeout", i18n.T("Ingestion wait ended. The journal preserves the submitted document"))
		}
		if err != nil || row.Phase == "completed" || !run.wait.Wait {
			return err
		}
		interval := run.wait.PollInterval
		if interval <= 0 {
			interval = time.Second
		}
		timer := time.NewTimer(interval)
		select {
		case <-waitCtx.Done():
			timer.Stop()
			if ctx.Err() != nil {
				return taskStatusError("wait_cancelled", i18n.T("Ingestion journal wait cancelled. Server tasks continue"))
			}
			return taskStatusError("wait_timeout", i18n.T("Ingestion wait ended. The journal preserves the submitted document"))
		case <-timer.C:
		}
	}
}

func (run *ingestJournalRun) verify(ctx context.Context, row *ingestJournalFile) error {
	files, err := run.client.KnowledgeBaseFiles(ctx, run.target, run.journal.KnowledgeBase)
	if err != nil {
		row.Problem = "verification_unavailable"
		return err
	}
	var document map[string]any
	for _, item := range files {
		entry, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if row.DocumentUUID != "" && entry["uuid"] != row.DocumentUUID {
			continue
		}
		if entry["file_name"] != row.FileID {
			continue
		}
		if kb, exists := entry["kb_id"]; exists && kb != run.journal.KnowledgeBase {
			continue
		}
		if workspace, exists := entry["workspace_uuid"]; exists && workspace != run.journal.WorkspaceUUID {
			continue
		}
		if document != nil {
			row.Problem = "document_ambiguous"
			return journalUnresolved(i18n.T("Several knowledge-base documents match this upload. Preserve the journal for reconciliation"))
		}
		document = entry
	}
	if document == nil {
		row.Problem = "document_unresolved"
		return journalUnresolved(i18n.T("The recorded knowledge-base document could not be confirmed. Preserve the journal for reconciliation"))
	}
	identifier, _ := document["uuid"].(string)
	if identifier == "" || api.ValidateResourceID(identifier) != nil {
		row.Problem = "document_unresolved"
		return journalUnresolved(i18n.T("The knowledge-base document has an invalid stable identity"))
	}
	row.DocumentUUID, row.Problem = identifier, ""
	switch document["status"] {
	case "completed":
		row.Phase = "completed"
	case "pending", "processing":
		row.Phase = "submitted"
	case "failed", "interrupted":
		row.Phase = "failed"
		return taskStatusError("document_failed", i18n.T("The recorded knowledge-base document failed ingestion"))
	default:
		row.Problem = "document_status_unresolved"
		return journalUnresolved(i18n.T("The knowledge-base document has an unknown ingestion status"))
	}
	return nil
}

func knownIngestRejection(err error) bool {
	failure := result.AsError(err)
	return failure.Type != "result_unknown" && (failure.Kind == "input" || failure.HTTPStatus >= 400 && failure.HTTPStatus < 500)
}

func journalUnresolved(message string) *result.Error {
	failure := result.New("result_unknown", message)
	failure.Type = "journal_unresolved"
	return failure
}

func ingestPathKey(path string) string {
	if runtime.GOOS == "windows" {
		return strings.ToLower(path)
	}
	return path
}

func hashIngestFile(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", result.New("input", i18n.T("Unable to hash ingestion input"))
	}
	defer file.Close()
	hash := sha256.New()
	if written, err := io.Copy(hash, io.LimitReader(file, maxExtensionUploadBytes+1)); err != nil || written > maxExtensionUploadBytes {
		return "", result.New("input", i18n.T("Unable to hash ingestion input"))
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func readIngestContent(path string) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	return io.ReadAll(io.LimitReader(file, maxExtensionUploadBytes+1))
}

func ingestContentHash(content []byte) string {
	hash := sha256.Sum256(content)
	return hex.EncodeToString(hash[:])
}

func checkIngestJournalPath(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil || !info.Mode().IsRegular() {
		return result.New("input", i18n.T("Ingestion journal and lock must be regular files"))
	}
	return nil
}

func readIngestJournal(path string, resume bool) (ingestJournal, error) {
	file, err := os.Open(path)
	if !resume {
		if err == nil {
			_ = file.Close()
			return ingestJournal{}, result.New("precondition", i18n.T("Ingestion journal already exists. Use --resume to continue it"))
		}
		if errors.Is(err, os.ErrNotExist) {
			return ingestJournal{}, nil
		}
	}
	if err != nil {
		return ingestJournal{}, result.New("input", i18n.T("Unable to read ingestion journal"))
	}
	defer file.Close()
	var journal ingestJournal
	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&journal); err != nil {
		return journal, result.New("input", i18n.T("Invalid ingestion journal"))
	}
	var trailing any
	if decoder.Decode(&trailing) != io.EOF {
		return journal, result.New("input", i18n.T("Invalid ingestion journal"))
	}
	return journal, nil
}

func validateIngestJournal(journal, expected ingestJournal) error {
	if journal.Version != 1 || journal.InstanceUUID != expected.InstanceUUID || journal.WorkspaceUUID != expected.WorkspaceUUID || journal.KnowledgeBase != expected.KnowledgeBase || journal.Parser != expected.Parser || len(journal.Files) != len(expected.Files) {
		return result.New("precondition", i18n.T("Ingestion journal target, parser or input group changed"))
	}
	inputs := make(map[string]ingestJournalFile, len(expected.Files))
	for _, row := range expected.Files {
		inputs[ingestPathKey(row.Path)] = row
	}
	for _, row := range journal.Files {
		key := ingestPathKey(row.Path)
		input, exists := inputs[key]
		if !exists || input.Size != row.Size || input.SHA256 != row.SHA256 {
			return result.New("precondition", i18n.T("Ingestion journal input paths, sizes or content hashes changed"))
		}
		delete(inputs, key)
		valid := false
		switch row.Phase {
		case "planned", "upload_intent":
			valid = row.FileID == "" && row.DocumentUUID == "" && row.TaskID == ""
		case "uploaded":
			valid = row.FileID != "" && row.DocumentUUID == "" && row.TaskID == ""
		case "store_intent", "submitted":
			valid = row.FileID != ""
		case "completed", "failed":
			valid = row.FileID != "" && row.DocumentUUID != ""
		}
		if !valid {
			return result.New("input", i18n.T("Ingestion journal contains an inconsistent confirmation phase"))
		}
	}
	return nil
}

func (run *ingestJournalRun) save() error {
	if err := checkIngestJournalPath(run.path); err != nil {
		return err
	}
	data, err := json.MarshalIndent(run.journal, "", "  ")
	if err != nil {
		return result.New("input", i18n.T("Unable to encode ingestion journal"))
	}
	file, err := os.CreateTemp(filepath.Dir(run.path), ".lbctl-ingest-journal-*")
	if err != nil {
		return result.New("input", i18n.T("Unable to save ingestion journal"))
	}
	defer os.Remove(file.Name())
	defer file.Close()
	if err = file.Chmod(0o600); err == nil {
		_, err = file.Write(data)
	}
	if err == nil {
		err = file.Sync()
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(file.Name(), run.path)
	}
	if err == nil {
		directory, openErr := os.Open(filepath.Dir(run.path))
		if openErr == nil {
			err = directory.Sync()
			_ = directory.Close()
		} else {
			err = openErr
		}
		if runtime.GOOS == "windows" {
			err = nil
		}
	}
	if err != nil {
		return result.New("input", i18n.T("Unable to durably save ingestion journal"))
	}
	return nil
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
	client, target := s.writeClient(preflight)
	if err := requireCapability(preflight, "knowledge_base.get"); err != nil {
		return preflight, nil, err
	}
	if _, listed := preflight.Capabilities.Operations["file.document.upload"]; !listed && target.UserToken != "" {
		err = client.CheckDocumentUpload(ctx, target)
	} else {
		err = requireCapability(preflight, "file.document.upload")
	}
	if err != nil {
		return preflight, nil, err
	}
	if wait {
		if err := requireCapability(preflight, "task.get"); err != nil {
			return preflight, nil, err
		}
	}
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
