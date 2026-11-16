package progress

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/langbot-app/langbot-cli/internal/i18n"
)

func HumanDisplay(ctx context.Context) bool {
	display, ok := ctx.Value(contextKey{}).(*Display)
	return ok && !display.machine
}

func HealthFile(ctx context.Context, path string) {
	if !HumanDisplay(ctx) {
		return
	}
	Health(ctx, lastFileLine(path))
}

func lastFileLine(path string) string {
	file, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return ""
	}
	start := max(int64(0), info.Size()-8192)
	if _, err := file.Seek(start, io.SeekStart); err != nil {
		return ""
	}
	data, _ := io.ReadAll(io.LimitReader(file, 8192))
	lines := strings.Split(strings.TrimSpace(clean(string(data))), "\n")
	return lines[len(lines)-1]
}

func Health(ctx context.Context, lastLine string) {
	display, ok := ctx.Value(contextKey{}).(*Display)
	if !ok || display.machine {
		return
	}
	display.mu.Lock()
	defer display.mu.Unlock()
	if lastLine != "" {
		lines := strings.Split(strings.TrimSpace(clean(lastLine)), "\n")
		display.tail = lines[len(lines)-1]
	}
	if !display.healthHint && !display.start.IsZero() && time.Since(display.start) >= 3*time.Minute {
		display.clear()
		fmt.Fprintln(display.out, i18n.Human("Health check is taking over 3 minutes. Inspect Core with lbctl logs, or retry with lbctl restart."))
		display.healthHint = true
	}
}
