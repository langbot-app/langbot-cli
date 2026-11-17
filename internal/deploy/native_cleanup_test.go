package deploy

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

func TestWaitNativeExit(t *testing.T) {
	t.Run("waits until exited", func(t *testing.T) {
		calls := 0
		err := waitNativeExit(t.Context(), func() (bool, error) {
			calls++
			return calls == 3, nil
		})
		if err != nil || calls != 3 {
			t.Fatalf("calls=%d err=%v", calls, err)
		}
	})
	t.Run("bounded wait", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
		defer cancel()
		if err := waitNativeExit(ctx, func() (bool, error) { return false, nil }); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("err=%v", err)
		}
	})
	t.Run("query error", func(t *testing.T) {
		if err := waitNativeExit(t.Context(), func() (bool, error) { return false, os.ErrPermission }); !errors.Is(err, os.ErrPermission) {
			t.Fatalf("err=%v", err)
		}
	})
}

func TestRemoveNativeFiles(t *testing.T) {
	locked := &os.PathError{Op: "remove", Path: "venvs/aiohttp/parser.pyd", Err: os.ErrPermission}
	for _, test := range []struct {
		name      string
		failures  int
		retry     bool
		timeout   time.Duration
		wantCalls int
		wantErr   bool
	}{
		{"success", 0, true, time.Second, 1, false},
		{"transient lock", 2, true, time.Second, 3, false},
		{"permanent error", 1, false, time.Second, 1, true},
		{"timeout", 10, true, 20 * time.Millisecond, 1, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			err := removeNativeFiles(t.Context(), "venvs", func(path string) error {
				if path != "venvs" {
					t.Fatalf("path=%s", path)
				}
				calls++
				if calls <= test.failures {
					return locked
				}
				return nil
			}, func(error) bool { return test.retry }, test.timeout)
			if calls != test.wantCalls || (err != nil) != test.wantErr {
				t.Fatalf("calls=%d err=%v", calls, err)
			}
			if test.wantErr && (!errors.Is(err, os.ErrPermission) || !strings.Contains(err.Error(), locked.Path)) {
				t.Fatalf("missing file error: %v", err)
			}
		})
	}
	t.Run("canceled before deletion", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		err := removeNativeFiles(ctx, "venvs", func(string) error { t.Fatal("unexpected deletion"); return nil }, func(error) bool { return true }, time.Second)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err=%v", err)
		}
	})
}
