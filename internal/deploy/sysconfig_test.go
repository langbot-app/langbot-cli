package deploy

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/langbot-app/langbot-cli/internal/redact"
)

func TestSystemConfigMutation(t *testing.T) {
	original := "# retained\nbox:\n  enabled: true\n  backend: local # selected\napi:\n  port: 5300\n  global_api_key: synthetic\n"
	config, err := ParseSystemConfig([]byte(original))
	if err != nil {
		t.Fatal(err)
	}
	if config.Value("api.global_api_key", false) != redact.Mask || config.Value("api.global_api_key", true) != "synthetic" {
		t.Fatal("incorrect secret projection")
	}
	for key, value := range map[string]string{"api.port": "65536", "box.enabled": "no", "box.backend": "unknown", "box.runtime.endpoint": "not a URL"} {
		if config.Set(key, value) == nil {
			t.Fatalf("accepted invalid %s", key)
		}
	}
	if err := config.Set("box.backend", "docker"); err != nil {
		t.Fatal(err)
	}
	encoded, err := config.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), "docker # selected") || !strings.HasPrefix(string(encoded), "# retained\nbox:") {
		t.Fatal("lost YAML comments or ordering")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(original), 0600); err != nil {
		t.Fatal(err)
	}
	if err := WriteSystemConfig(context.Background(), dir, Record{DataDir: dir, Driver: DriverNative}, nil, encoded); err != nil {
		t.Fatal(err)
	}
	backup, err := os.ReadFile(path + ".bak")
	if err != nil || string(backup) != original {
		t.Fatal("missing original backup")
	}
	for _, file := range []string{path, path + ".bak"} {
		info, err := os.Stat(file)
		if err != nil || runtime.GOOS != "windows" && info.Mode().Perm() != 0600 {
			t.Fatalf("incorrect mode: %v", info)
		}
	}
	if err := config.Unset("box.backend"); err != nil || config.Node("box.backend") != nil {
		t.Fatal("unset failed")
	}
}
