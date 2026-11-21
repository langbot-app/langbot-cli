package deploy

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/langbot-app/langbot-cli/internal/i18n"
)

func waitNativeExit(ctx context.Context, exited func() (bool, error)) error {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf(i18n.T("waiting for LangBot processes to exit: %w"), err)
		}
		done, err := exited()
		if err != nil || done {
			return err
		}
		select {
		case <-ctx.Done():
		case <-ticker.C:
		}
	}
}

func RemoveNativeFiles(ctx context.Context, path string) error {
	return removeNativeFiles(ctx, path, removeAllWritable, nativeRemovalRetryable, 15*time.Second)
}

// removeAllWritable removes path like os.RemoveAll. The Plugin Runtime seals
// unpacked plugin artifacts read-only (directories 0555, files 0444), which
// makes unlink fail with EACCES on Unix and read-only attributes block deletion
// on Windows. On a permission error, restore owner write access inside the tree
// once and retry.
func removeAllWritable(path string) error {
	err := os.RemoveAll(path)
	if err == nil || !errors.Is(err, fs.ErrPermission) {
		return err
	}
	if !makeTreeOwnerWritable(path) {
		return err
	}
	return os.RemoveAll(path)
}

// makeTreeOwnerWritable adds owner write access to directories and regular
// files below root without following symbolic links. It reports whether any
// mode was changed; individual failures are left for RemoveAll to report.
func makeTreeOwnerWritable(root string) bool {
	changed := false
	_ = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil || entry.Type()&fs.ModeSymlink != 0 {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return nil
		}
		mode := info.Mode().Perm()
		want := mode | 0o200
		if entry.IsDir() {
			want = mode | 0o700
		} else if !info.Mode().IsRegular() {
			return nil
		}
		if want != mode && os.Chmod(path, want) == nil {
			changed = true
		}
		return nil
	})
	return changed
}

func removeNativeFiles(ctx context.Context, path string, remove func(string) error, retryable func(error) bool, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	delay := 100 * time.Millisecond
	var lastErr error
	for {
		if err := ctx.Err(); err != nil {
			if lastErr != nil {
				return fmt.Errorf(i18n.T("cannot remove LangBot files; close LangBot and retry: %w"), lastErr)
			}
			return err
		}
		err := remove(path)
		if err == nil || !retryable(err) {
			return err
		}
		lastErr = err
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return fmt.Errorf(i18n.T("cannot remove LangBot files; close LangBot and retry: %w"), err)
		case <-timer.C:
		}
		if delay < time.Second {
			delay = min(delay*2, time.Second)
		}
	}
}
