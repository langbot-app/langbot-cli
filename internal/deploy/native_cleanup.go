package deploy

import (
	"context"
	"fmt"
	"os"
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
	return removeNativeFiles(ctx, path, os.RemoveAll, nativeRemovalRetryable, 15*time.Second)
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
