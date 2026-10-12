package deploy

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAdoptedUpgradePreservesComposeBytesAndSupportsNextUpgrade(t *testing.T) {
	root := t.TempDir()
	composeDir, recordDir := filepath.Join(root, "existing"), filepath.Join(root, "records")
	if err := os.MkdirAll(composeDir, 0o700); err != nil {
		t.Fatal(err)
	}
	content := "# existing deployment\r\nservices:\r\n  langbot:\r\n    image: 'rockchin/langbot:latest' # core\r\n    ports: ['15300:5300']\r\n    environment: {KEEP: unchanged}\r\n    volumes: ['./data:/app/data']\r\n  langbot_plugin_runtime: {image: \"rockchin/langbot:latest\", volumes: ['./data/plugins:/app/data/plugins']}\r\n  langbot_box:\r\n    image: rockchin/langbot:latest\r\n    profiles: [all]\r\n  unrelated:\r\n    image: rockchin/langbot:latest\r\n"
	file := filepath.Join(composeDir, "docker-compose.yaml")
	if err := os.WriteFile(file, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	record := Record{DeploymentID: "adopted", Driver: DriverDockerCompose, CoreVersion: "v4.10.9", Profile: "all", ComposeFile: file, DataDir: filepath.Join(composeDir, "data"), Asset: AssetInfo{Name: "docker-compose.yaml", Digest: fileDigest([]byte(content))}}
	if err := (Store{Dir: recordDir}).Write(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	updated, digest, err := ComposeForUpgrade(record, recordDir, "v4.10.10")
	if err != nil {
		t.Fatal(err)
	}
	expected := strings.Replace(content, "rockchin/langbot:latest", "rockchin/langbot:v4.10.10", 3)
	if string(updated) != expected {
		t.Fatalf("unrelated Compose bytes changed:\n%s", updated)
	}
	if err := os.WriteFile(file, updated, 0o600); err != nil {
		t.Fatal(err)
	}
	record.CoreVersion, record.Asset.Digest = "v4.10.10", digest
	if _, _, err := ComposeForUpgrade(record, recordDir, "v4.10.11"); err != nil {
		t.Fatalf("next upgrade rejected: %v", err)
	}
	if err := os.WriteFile(file, append(updated, []byte("# changed\n")...), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ComposeForUpgrade(record, recordDir, "v4.10.11"); err == nil {
		t.Fatal("changed Compose accepted")
	}
}

func TestAdoptedUpgradeRejectsAmbiguousImages(t *testing.T) {
	for _, image := range []string{"${LANGBOT_IMAGE}", "other/langbot:v4.10.9", "*shared", "&shared rockchin/langbot:latest"} {
		content := "services:\n  langbot:\n    image: " + image + "\n  langbot_plugin_runtime:\n    image: rockchin/langbot:latest\n"
		if _, err := replaceComposeImages([]byte(content), "rockchin/langbot:v4.10.10"); err == nil {
			t.Fatalf("accepted image %q", image)
		}
	}
}

func TestAdoptedUpgradeRequiresCompleteMountBackup(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "data")
	makeConfig := func(source, volumeType string) string {
		data, err := json.Marshal(map[string]any{"services": map[string]any{
			"langbot":                map[string]any{"volumes": []map[string]any{{"type": volumeType, "source": source, "target": "/app/data"}}},
			"langbot_plugin_runtime": map[string]any{"volumes": []map[string]any{{"type": "bind", "source": filepath.Join(dataDir, "plugins"), "target": "/app/data/plugins"}}},
		}})
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	record := Record{DataDir: dataDir, Profile: "basic"}
	if err := ValidateAdoptedUpgradeConfig(record, makeConfig(dataDir, "bind")); err != nil {
		t.Fatal(err)
	}
	for _, config := range []string{makeConfig(filepath.Join(t.TempDir(), "external"), "bind"), makeConfig(dataDir, "volume"), `{ "services": {} }`} {
		if err := ValidateAdoptedUpgradeConfig(record, config); err == nil {
			t.Fatal("unbacked mount accepted")
		}
	}
}

func TestAdoptedBackupUsesSeparateRecordDirectoryAndOptionalEnv(t *testing.T) {
	for _, withEnv := range []bool{false, true} {
		t.Run(map[bool]string{false: "no-env", true: "implicit-env"}[withEnv], func(t *testing.T) {
			record, originalDir := upgradeFixture(t)
			recordDir := filepath.Join(t.TempDir(), "records")
			if err := (Store{Dir: recordDir}).Write(context.Background(), record); err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(filepath.Join(originalDir, recordFileName)); err != nil {
				t.Fatal(err)
			}
			record.EnvFile = ""
			if !withEnv {
				if err := os.Remove(filepath.Join(originalDir, ".env")); err != nil {
					t.Fatal(err)
				}
			}
			backup, err := CreateUpgradeBackup(context.Background(), record, UpgradeState{SourceVersion: record.CoreVersion}, recordDir)
			if err != nil {
				t.Fatal(err)
			}
			if err := VerifyUpgradeBackup(backup); err != nil {
				t.Fatal(err)
			}
			if !regularFile(filepath.Join(backup, recordFileName)) || regularFile(filepath.Join(backup, ".env")) != withEnv {
				t.Fatal("record or optional env backup mismatch")
			}
		})
	}
}
