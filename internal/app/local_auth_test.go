package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/langbot-app/langbot-cli/internal/config"
	"github.com/langbot-app/langbot-cli/internal/deploy"
)

func TestLocalAuthPreservesContextAndIsolatesInstances(t *testing.T) {
	root := t.TempDir()
	configPath := filepath.Join(root, "cli.yaml")
	store := config.Store{Path: configPath}
	if err := store.Update(context.Background(), func(file *config.File) error {
		file.CurrentContext = "cloud"
		file.Contexts["cloud"] = config.Context{Endpoint: "https://cloud.example", Credential: &config.Credential{Type: "env", Name: "CLOUD_KEY"}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	service := New(Dependencies{DefaultConfigPath: func() (string, error) { return configPath, nil }, LookupEnv: func(string) (string, bool) { return "", false },
		Transport: taskRoundTripFunc(func(request *http.Request) (*http.Response, error) {
			return taskResponse(fmt.Sprintf(`{"code":0,"data":{"instance_uuid":"instance-local","workspace_uuid":%q,"api_key_id":"global","permissions":["resource.view","resource.manage"],"schema_version":1,"operations":{"bot.create":{"supported":true},"bot.get":{"supported":true}}}}`, "workspace-"+request.URL.Hostname())), nil
		}),
	})
	keys := []string{}
	for _, name := range []string{"first", "second"} {
		dir := filepath.Join(root, name)
		record := deploy.Record{DeploymentID: "local-" + name, Driver: deploy.DriverNative, CoreVersion: "v4.11.7", DataDir: filepath.Join(dir, "data"), Endpoint: "http://" + name + ".localhost:5300"}
		if err := os.MkdirAll(record.DataDir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(record.DataDir, "config.yaml"), []byte("api: {}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		changed, err := service.configureLocalAuth(context.Background(), dir, record, false)
		if err != nil || !changed {
			t.Fatal("authentication not configured")
		}
		key, err := service.ShowAPIKey(context.Background(), CheckOptions{Context: record.DeploymentID, ContextSet: true})
		if err != nil {
			t.Fatal(err)
		}
		keys = append(keys, key)
		if err := service.bindLocalAuth(context.Background(), record); err != nil {
			t.Fatal(err)
		}
		if _, err := service.WritePreflight(context.Background(), "bot.create", CheckOptions{Context: record.DeploymentID, ContextSet: true}); err != nil {
			t.Fatalf("automatic context is not usable for writes: %v", err)
		}
		shown, err := service.ContextShow(record.DeploymentID)
		encoded, _ := json.Marshal(shown)
		if err != nil || strings.Contains(string(encoded), key) || !strings.Contains(string(encoded), "local_deployment") || !strings.Contains(string(encoded), "available") {
			t.Fatal("context does not safely describe local credential")
		}
		if err := store.Update(context.Background(), func(file *config.File) error {
			selected := file.Contexts[record.DeploymentID]
			selected.Credential = &config.Credential{Type: "env", Name: "CUSTOM_KEY"}
			file.Contexts[record.DeploymentID] = selected
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := service.configureLocalAuth(context.Background(), dir, record, false); err != nil {
			t.Fatal(err)
		}
		file, _ := store.Load()
		if file.Contexts[record.DeploymentID].Credential.Type != "env" || file.CurrentContext != "cloud" {
			t.Fatal("automatic configuration replaced explicit selection")
		}
	}
	if keys[0] == keys[1] {
		t.Fatal("instances share credentials")
	}
}
