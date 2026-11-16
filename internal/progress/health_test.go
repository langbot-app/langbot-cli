package progress

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestHealthLogTailAndSlowHint(t *testing.T) {
	if got := coreLogPrefix.ReplaceAllString("[11-16 17:44:08.164] mgr.py (177) - [INFO] : Initializing database type: sqlite...", "$1 · "); got != "mgr.py · Initializing database type: sqlite..." {
		t.Fatal(got)
	}
	path := filepath.Join(t.TempDir(), "core.log")
	if err := os.WriteFile(path, []byte(strings.Repeat("old\n", 3000)+"\x1b[31mInitializing database\x1b[0m\n"), 0600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	display := New(&out, func(string) (string, bool) { return "", false }, false, false)
	ctx := WithContext(context.Background(), display)
	display.Step("Check service health")
	display.mu.Lock()
	display.start = time.Now().Add(-4 * time.Minute)
	display.mu.Unlock()
	HealthFile(ctx, path)
	HealthFile(ctx, path)
	display.mu.Lock()
	tail := display.tail
	display.mu.Unlock()
	display.Finish(false)
	if tail != "Initializing database" || strings.Count(out.String(), "lbctl restart") != 1 {
		t.Fatal(tail, out.String())
	}
}
