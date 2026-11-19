package app

import (
	"archive/zip"
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/langbot-app/langbot-cli/internal/i18n"
	"github.com/langbot-app/langbot-cli/internal/result"
)

// SkillInstallDirectory uses the existing preview/upload contract for local files.
func (s *Service) SkillInstallDirectory(ctx context.Context, directory string, sourcePaths []string, dryRun bool, options CheckOptions) (Result, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if strings.TrimSpace(directory) == "" {
		return Result{}, result.New("input", i18n.T("Skill directory must be a readable directory"))
	}
	directory, err := filepath.Abs(directory)
	if err != nil {
		return Result{}, result.New("input", i18n.T("Unable to read Skill directory"))
	}
	stat, err := os.Lstat(directory)
	if err != nil || !stat.IsDir() || stat.Mode()&os.ModeSymlink != 0 {
		return Result{}, result.New("input", i18n.T("Skill directory must be a readable directory"))
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		return Result{}, result.New("input", i18n.T("Unable to read Skill directory"))
	}
	defer root.Close()
	files := []string{}
	err = fs.WalkDir(root.FS(), ".", func(path string, entry fs.DirEntry, walkErr error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if walkErr != nil {
			return walkErr
		}
		if entry.Name() == ".git" {
			if entry.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() || strings.Contains(path, "\\") {
			return result.New("input", i18n.T("Skill directory contains a symbolic link or non-regular file"))
		}
		files = append(files, path)
		return nil
	})
	if err != nil {
		return Result{}, skillDirectoryError(ctx, err)
	}
	if len(files) == 0 {
		return Result{}, result.New("input", i18n.T("Skill directory has no files to package"))
	}
	// Scan before creating temporary files, even when the source contains TEMP.
	temporary, err := os.MkdirTemp("", "lbctl-skill-*")
	if err != nil {
		return Result{}, result.New("input", i18n.T("Unable to create a temporary Skill package"))
	}
	defer os.RemoveAll(temporary)
	name := strings.Trim(filepath.Base(directory), "/\\:")
	if name == "" {
		name = "skills"
	}
	filename := filepath.Join(temporary, name+".zip")
	if err := writeSkillDirectory(ctx, root, files, filename); err != nil {
		return Result{}, skillDirectoryError(ctx, err)
	}
	value, err := s.SkillInstallUpload(ctx, filename, sourcePaths, dryRun, options)
	if data, ok := value.Data.(map[string]any); ok {
		data["source_directory"] = directory
		data["file_count"] = len(files)
	}
	return value, err
}

type skillArchiveWriter struct {
	ctx     context.Context
	file    *os.File
	written int64
}

func (w *skillArchiveWriter) Write(data []byte) (int, error) {
	if err := w.ctx.Err(); err != nil {
		return 0, err
	}
	if int64(len(data)) > maxExtensionUploadBytes-w.written {
		return 0, result.New("input", i18n.T("Skill directory archive exceeds the 10MB upload limit"))
	}
	written, err := w.file.Write(data)
	w.written += int64(written)
	return written, err
}

func writeSkillDirectory(ctx context.Context, root *os.Root, paths []string, filename string) error {
	file, err := os.OpenFile(filename, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	defer file.Close()
	archive := zip.NewWriter(&skillArchiveWriter{ctx: ctx, file: file})
	defer archive.Close()
	for _, path := range paths {
		if err := ctx.Err(); err != nil {
			return err
		}
		input, err := root.Open(path)
		if err != nil {
			return err
		}
		info, statErr := input.Stat()
		if statErr != nil || !info.Mode().IsRegular() {
			input.Close()
			return result.New("input", i18n.T("Skill directory contains a symbolic link or non-regular file"))
		}
		header, err := zip.FileInfoHeader(info)
		if err != nil {
			input.Close()
			return err
		}
		header.Name, header.Method = path, zip.Deflate
		entry, err := archive.CreateHeader(header)
		if err == nil {
			_, err = io.Copy(entry, input)
		}
		closeErr := input.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
	}
	if err := archive.Close(); err != nil {
		return err
	}
	return file.Close()
}

func skillDirectoryError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return result.New("network", i18n.T("Skill directory packaging cancelled"))
	}
	var failure *result.Error
	if errors.As(err, &failure) {
		return failure
	}
	return result.New("input", i18n.T("Unable to package Skill directory"))
}
