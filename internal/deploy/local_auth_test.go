package deploy

import (
	"context"
	"encoding/base64"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

func TestAuthConfigReaderUsesUTF8(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		python, err = exec.LookPath("python")
	}
	if err != nil {
		t.Skip("Python unavailable")
	}
	path := filepath.Join(t.TempDir(), "config.yaml")
	content := "api: {}\n# 本地配置\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	command := exec.Command(python, "-c", readAuthConfig, path)
	command.Env = append(os.Environ(), "PYTHONIOENCODING=ascii", "PYTHONUTF8=0")
	output, err := command.Output()
	if err != nil || string(output) != content {
		t.Fatalf("UTF-8 config transfer failed: %v", err)
	}
}

func TestConfigureAPIKey(t *testing.T) {
	input := []byte("# keep comment\napi:\n  webhook_prefix: https://example.org\nother: [one, two]\n")
	updated, key, changed, err := configureAPIKey(input, false, "http://localhost:5300")
	if err != nil || !changed || !strings.HasPrefix(key, "lbg_") {
		t.Fatalf("key generation failed: %v", err)
	}
	decoded, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(key, "lbg_"))
	if err != nil || len(decoded) != 32 {
		t.Fatal("key must have 256 random bits")
	}
	var before, after map[string]any
	_ = yaml.Unmarshal(input, &before)
	_ = yaml.Unmarshal(updated, &after)
	delete(after["api"].(map[string]any), "global_api_key")
	want, _ := yaml.Marshal(before)
	got, _ := yaml.Marshal(after)
	if string(want) != string(got) || !strings.Contains(string(updated), "# keep comment") {
		t.Fatal("changed unrelated configuration")
	}
	reused, same, changed, err := configureAPIKey(updated, false, "ignored")
	if err != nil || changed || same != key || string(reused) != string(updated) {
		t.Fatal("existing key was not preserved byte-for-byte")
	}
	_, second, _, _ := configureAPIKey(input, false, "ignored")
	if second == key {
		t.Fatal("instances share keys")
	}
	_, custom, changed, err := configureAPIKey([]byte("api:\n  global_api_key: custom-no-prefix\n"), false, "")
	if err != nil || changed || custom != "custom-no-prefix" {
		t.Fatal("custom key was replaced")
	}
}

func TestEnsureLocalAuth(t *testing.T) {
	dir, record := nativeFixture(t)
	path := filepath.Join(record.DataDir, "config.yaml")
	if err := os.WriteFile(path, []byte("api: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	changed, err := EnsureLocalAuth(context.Background(), dir, record, nil)
	if err != nil || !changed {
		t.Fatalf("ensure: %v", err)
	}
	key, err := os.ReadFile(LocalKeyPath(dir))
	if err != nil || len(key) != 47 {
		t.Fatal("missing private credential")
	}
	for _, file := range []string{path, LocalKeyPath(dir)} {
		info, err := os.Stat(file)
		if err != nil || runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
			t.Fatal("credential permissions are not private")
		}
	}
	if changed, err := EnsureLocalAuth(context.Background(), dir, record, nil); err != nil || changed {
		t.Fatal("ensure is not idempotent")
	}
	for _, version := range []string{"v4.10.2", "v4.10.3", "v4.10.10", "v3.4.0", "unknown"} {
		record.CoreVersion = version
		if changed, err := EnsureLocalAuth(context.Background(), "missing", record, nil); err != nil || changed {
			t.Fatalf("old version %s not skipped", version)
		}
	}
	for _, version := range []string{"v4.10.11", "v4.11.7", "v5.0.0"} {
		if !SupportsLocalAuth(version) {
			t.Fatalf("supported version %s skipped", version)
		}
	}
}

func TestLocalAuthParseErrorDoesNotExposeConfig(t *testing.T) {
	dir, record := nativeFixture(t)
	const secret = "do-not-expose-this-key"
	if err := os.WriteFile(filepath.Join(record.DataDir, "config.yaml"), []byte("api: ["+secret), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := EnsureLocalAuth(context.Background(), dir, record, nil); err == nil || strings.Contains(err.Error(), secret) {
		t.Fatal("unsafe parse error")
	}
}
