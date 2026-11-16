package app

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/langbot-app/langbot-cli/internal/deploy"
)

func TestNativeHumanUptime(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "native.pid")
	if err := os.WriteFile(path, []byte("1"), 0600); err != nil {
		t.Fatal(err)
	}
	started := time.Now().Add(-time.Minute)
	if err := os.Chtimes(path, started, started); err != nil {
		t.Fatal(err)
	}
	resolved := environmentResolution{LocalMatch: true, Environment: ActiveEnvironment{Runtime: EnvironmentRuntime{Driver: deploy.DriverNative, Status: "running", Dir: dir}}}
	service := New(Dependencies{})
	value, err := time.ParseDuration(service.humanUptime(context.Background(), resolved))
	if err != nil || value < time.Minute || value > 65*time.Second {
		t.Fatalf("%s: %v", value, err)
	}
	resolved.Environment.Runtime.Status = "stopped"
	if service.humanUptime(context.Background(), resolved) != "0s" {
		t.Fatal("stopped uptime")
	}
}
