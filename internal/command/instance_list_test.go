package command

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/langbot-app/langbot-cli/internal/deploy"
)

func TestLocalInstanceListAndStableJSON(t *testing.T) {
	locator := deploy.Locator{Path: filepath.Join(t.TempDir(), "locator.yaml")}
	for _, name := range []string{"first", "second"} {
		dir := writeDeploymentRecord(t, deploy.Record{Driver: deploy.DriverNative, Name: name, DeploymentID: name, Port: 15300, Endpoint: "http://127.0.0.1:15300", CoreVersion: "v4.11.8", ServiceManager: "process", Uninstalled: name == "second"})
		if err := locator.Write(dir); err != nil {
			t.Fatal(err)
		}
	}
	if err := locator.Write(filepath.Join(t.TempDir(), "missing")); err != nil {
		t.Fatal(err)
	}
	previous := ""
	for _, lang := range []string{"en", "zh-Hans"} {
		var out, diagnostic bytes.Buffer
		deps := Dependencies{Out: &out, Err: &diagnostic, LocalLocatorPath: locator.Path}
		if code := Execute(context.Background(), []string{"list", "--lang", lang, "-o", "json"}, deps); code != 0 {
			t.Fatalf("%d: %s", code, &out)
		}
		var envelope struct {
			Data struct {
				Instances []struct {
					Name, Dir, Status string
					Port              int
					Autostart         *bool
				}
			}
		}
		if err := json.Unmarshal(out.Bytes(), &envelope); err != nil {
			t.Fatal(err)
		}
		if len(envelope.Data.Instances) != 3 || envelope.Data.Instances[0].Status != "stopped" || envelope.Data.Instances[1].Status != "not_installed" || envelope.Data.Instances[2].Status != "unknown" {
			t.Fatal(out.String())
		}
		if previous != "" && previous != out.String() {
			t.Fatal("locale changed JSON")
		}
		previous = out.String()
		out.Reset()
		if code := Execute(context.Background(), []string{"list", "--lang", lang}, deps); code != 0 || !strings.Contains(out.String(), "first") || !strings.Contains(out.String(), "15300") {
			t.Fatalf("%d: %s", code, &out)
		}
	}
}

func TestUnknownCommandHelpDoesNotRepeatExecutable(t *testing.T) {
	for _, lang := range []string{"en", "zh-Hans"} {
		var out, diagnostic bytes.Buffer
		code := Execute(context.Background(), []string{"not-a-command", "--lang", lang}, Dependencies{Out: &out, Err: &diagnostic})
		if code == 0 || strings.Contains(out.String(), "lbctl lbctl") || !strings.Contains(out.String(), "lbctl --help") {
			t.Fatalf("%d: %s", code, &out)
		}
	}
}
