package app

import (
	"context"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/langbot-app/langbot-cli/internal/deploy"
	"github.com/langbot-app/langbot-cli/internal/result"
)

func TestInstallMultipleProjectsWithIndependentPortSelections(t *testing.T) {
	root := t.TempDir()
	runner := &lifecycleTestRunner{state: "stopped"}
	transport := lifecycleTransport{runner: runner}
	locator := deploy.Locator{Path: filepath.Join(root, "locator.yaml")}
	service := New(Dependencies{
		LocalLocatorPath: locator.Path, LocalRunner: runner, Transport: transport,
		LocalHTTP:         &http.Client{Transport: transport},
		DefaultConfigPath: func() (string, error) { return filepath.Join(root, "config.yaml"), nil },
	})
	used := map[int]bool{}
	freePort := func() int {
		for {
			port := freeLifecyclePort(t)
			if !used[port] {
				used[port] = true
				return port
			}
		}
	}
	debug, adapter := freePort(), freePort()
	for _, options := range []InstallOptions{
		{Dir: filepath.Join(root, "first"), Version: "v4.10.10", Project: "first", Port: freePort(), PluginDebugPort: new(int), AdapterPorts: []string{"none"}},
		{Dir: filepath.Join(root, "second"), Version: "v4.10.10", Project: "second", Port: freePort(), PluginDebugPort: &debug, AdapterPorts: []string{fmt.Sprintf("%d:2283", adapter)}},
	} {
		value, err := service.Install(context.Background(), options)
		if err != nil {
			t.Fatalf("install %s: %v data=%+v", options.Project, err, value.Data)
		}
		record, err := (deploy.Store{Dir: options.Dir}).Read()
		if err != nil || record.ComposeProject != options.Project || record.InstallPorts == nil || record.InstallPorts.PluginDebugPort != *options.PluginDebugPort {
			t.Fatalf("incorrect saved installation: %+v error=%v", record, err)
		}
		if err := deploy.ValidateOwnedLayout(record, options.Dir); err != nil {
			t.Fatalf("custom installation must remain upgradeable: %v", err)
		}
	}
	dirs, err := locator.Directories()
	if err != nil || len(dirs) != 2 {
		t.Fatalf("multiple installations were not registered: %v %v", dirs, err)
	}
	for _, call := range runner.calls {
		if strings.Contains(call, "compose.project=langbot-lbctl") {
			t.Fatalf("installation checked another instance's default project: %s", call)
		}
	}
	before := len(runner.calls)
	_, err = service.Install(context.Background(), InstallOptions{
		Dir: filepath.Join(root, "duplicate"), Version: "v4.10.10", Project: "second", Port: freePort(),
		PluginDebugPort: new(int), AdapterPorts: []string{"none"}, DryRun: true,
	})
	if err == nil || result.AsError(err).Kind != "precondition" {
		t.Fatalf("dry-run must reject an already registered Compose project: %v", err)
	}
	for _, call := range runner.calls[before:] {
		if strings.HasPrefix(call, "docker pull ") || strings.HasPrefix(call, "compose up ") {
			t.Fatalf("conflicting dry-run mutated Docker: %s", call)
		}
	}
}
