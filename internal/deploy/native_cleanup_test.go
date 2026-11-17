package deploy

import (
	"context"
	"errors"
	"testing"
)

func TestNativeRemovalRetriesOnlyTransientFailures(t *testing.T) {
	locked := errors.New("locked")
	for _, test := range []struct {
		name           string
		failures, want int
		retry          bool
	}{
		{"released", 1, 2, true}, {"permanent", 20, 1, false}, {"bounded", 20, 10, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			err := removeNativeFiles(t.Context(), "venvs", func(string) error {
				calls++
				if calls <= test.failures {
					return locked
				}
				return nil
			}, func(error) bool { return test.retry })
			if calls != test.want || (err == nil) != (test.failures < test.want) {
				t.Fatalf("calls=%d err=%v", calls, err)
			}
		})
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := removeNativeFiles(ctx, "venvs", func(string) error { t.Fatal("remove after cancellation"); return nil }, func(error) bool { return true }); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
