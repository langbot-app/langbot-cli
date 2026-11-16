package app

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/langbot-app/langbot-cli/internal/deploy"
	"github.com/langbot-app/langbot-cli/internal/i18n"
)

func (s *Service) humanUptime(ctx context.Context, resolved environmentResolution) string {
	info := resolved.Environment.Runtime
	if info.Status == "stopped" {
		return "0s"
	}
	if !resolved.LocalMatch || (info.Status != "running" && info.Status != "degraded") {
		return i18n.Human("Not reported")
	}
	var started time.Time
	if info.Driver == deploy.DriverNative {
		if stat, err := os.Stat(filepath.Join(info.Dir, "native.pid")); err == nil {
			started = stat.ModTime()
		}
	} else if info.Details != nil {
		ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		for _, container := range info.Details.Containers {
			if container.Service != "langbot" || container.State != "running" {
				continue
			}
			result, err := s.localRunner().Docker(ctx, "inspect", "--format", "{{.State.StartedAt}}", container.Name)
			if err == nil {
				started, _ = time.Parse(time.RFC3339Nano, strings.TrimSpace(result.Stdout))
			}
			break
		}
	}
	if started.IsZero() {
		return i18n.Human("Not reported")
	}
	return max(time.Duration(0), time.Since(started).Truncate(time.Second)).String()
}
