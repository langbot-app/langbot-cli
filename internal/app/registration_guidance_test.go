package app

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/langbot-app/langbot-cli/internal/deploy"
	"github.com/langbot-app/langbot-cli/internal/i18n"
)

func TestRegistrationGuidanceOmitsEmptyIdentifiers(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "original instance")
	locator := deploy.Locator{Path: filepath.Join(root, "locator.yaml")}
	record := deploy.Record{Driver: deploy.DriverNative, DeploymentID: "original", Name: "original", CoreVersion: "v4.11.8", Endpoint: "http://127.0.0.1:15300", Port: 15300, ServiceManager: "process"}
	if err := (deploy.Store{Dir: dir}).Write(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	if err := locator.Write(dir); err != nil {
		t.Fatal(err)
	}
	service := New(Dependencies{LocalLocatorPath: locator.Path})
	for _, lang := range []string{i18n.English, i18n.Chinese} {
		restore := i18n.Use(lang)
		err := service.checkDeploymentRegistration(filepath.Join(root, "other"), record)
		restore()
		if err == nil {
			t.Fatal("conflict not detected")
		}
		text := err.Error()
		for _, unwanted := range []string{"Compose project", "instance UUID", "Instance UUID"} {
			if strings.Contains(text, unwanted) {
				t.Fatal(text)
			}
		}
		for _, expected := range []string{dir, "lbctl install", "lbctl uninstall --dir", "--yes"} {
			if !strings.Contains(text, expected) {
				t.Fatal(text)
			}
		}
	}
}
