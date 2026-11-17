package deploy

import (
	"context"
	"os"
	"time"
)

func waitNativeProcesses(ctx context.Context, processes []int) error {
	for _, pid := range processes {
		for nativePIDAlive(pid) {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(100 * time.Millisecond):
			}
		}
	}
	return nil
}

func RemoveNativeFiles(ctx context.Context, path string) error {
	return removeNativeFiles(ctx, path, os.RemoveAll, retryNativeRemoval)
}

func removeNativeFiles(ctx context.Context, path string, remove func(string) error, retry func(error) bool) error {
	for attempt := 0; ; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		err := remove(path)
		if err == nil || !retry(err) || attempt == 9 {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(300 * time.Millisecond):
		}
	}
}
