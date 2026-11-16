package command

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/langbot-app/langbot-cli/internal/deploy"
)

func TestAutostartNamedInstanceAndConnectionFlags(t *testing.T) {
	root := t.TempDir()
	t.Setenv("LANGBOT_HOME", root)
	t.Setenv("LBCTL_NO_UPDATE_CHECK", "1")
	dir, err := deploy.InstanceDir("named")
	if err != nil {
		t.Fatal(err)
	}
	record := deploy.Record{Driver: deploy.DriverNative, DeploymentID: "named", Name: "named", ServiceManager: "process", CoreVersion: "v4.11.7", DataDir: filepath.Join(dir, "data"), Port: 5300}
	if err := (deploy.Store{Dir: dir}).Write(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	for _, action := range []string{"status", "disable"} {
		var output bytes.Buffer
		code := executeChinese(context.Background(), []string{"autostart", action, "--name", "named", "--output", "json"}, Dependencies{Out: &output})
		var envelope struct {
			OK   bool `json:"ok"`
			Data struct {
				Autostart bool   `json:"autostart"`
				Mechanism string `json:"mechanism"`
				Dir       string `json:"instance_dir"`
			} `json:"data"`
		}
		if err := json.Unmarshal(output.Bytes(), &envelope); err != nil || code != 0 || !envelope.OK || envelope.Data.Autostart || envelope.Data.Dir != dir || envelope.Data.Mechanism != "process" {
			t.Fatalf("%d %s: %v", code, output.String(), err)
		}
	}
	var output bytes.Buffer
	if code := executeChinese(context.Background(), []string{"autostart", "enable", "--endpoint", "http://remote.example"}, Dependencies{Out: &output}); code != 2 {
		t.Fatalf("connection flags accepted: %d %s", code, output.String())
	}
}
