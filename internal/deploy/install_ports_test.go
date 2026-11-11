package deploy

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

func freeInstallTestPort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	return port
}

func TestInstallPortSelectionGeneratesOnlyRequestedPublications(t *testing.T) {
	for _, test := range []struct {
		name     string
		network  InstallNetworkOptions
		debug    []string
		core     []string
		debugURL string
	}{
		{"defaults", InstallNetworkOptions{}, []string{"5401:5401"}, []string{"15301:5300", "2280-2285:2280-2285"}, ""},
		{"custom", InstallNetworkOptions{Project: "regression", PluginDebugPort: new(15401), AdapterPorts: []string{"12280-12285:2280-2285"}}, []string{"15401:5401"}, []string{"15301:5300", "12280-12285:2280-2285"}, "ws://localhost:15401/plugin/debug/ws"},
		{"headless", InstallNetworkOptions{PluginDebugPort: new(int), AdapterPorts: []string{"none"}}, nil, []string{"15301:5300"}, ""},
		{"partial", InstallNetworkOptions{PluginDebugPort: new(int), AdapterPorts: []string{"12280:2280", "12283:2283"}}, nil, []string{"15301:5300", "12280:2280", "12283:2283"}, ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			assets, err := NewInstallAssets(filepath.Join(t.TempDir(), "deployment"), "v4.10.10", "all", 15301, test.network)
			if err != nil {
				t.Fatal(err)
			}
			var document struct {
				Services map[string]struct {
					Ports       []string          `yaml:"ports"`
					Environment map[string]string `yaml:"environment"`
					Command     []string          `yaml:"command"`
				} `yaml:"services"`
			}
			if err := yaml.Unmarshal(assets.Compose, &document); err != nil {
				t.Fatalf("generated Compose is invalid: %v\n%s", err, assets.Compose)
			}
			if !reflect.DeepEqual(document.Services["langbot_plugin_runtime"].Ports, test.debug) || !reflect.DeepEqual(document.Services["langbot"].Ports, test.core) {
				t.Fatalf("unexpected publications: %+v", document.Services)
			}
			if got := document.Services["langbot"].Environment["PLUGIN__DISPLAY_PLUGIN_DEBUG_URL"]; got != test.debugURL {
				t.Fatalf("debug display URL = %q, want %q", got, test.debugURL)
			}
			if strings.Contains(strings.Join(document.Services["langbot_plugin_runtime"].Command, " "), "--ws-debug-port") {
				t.Fatal("host remapping must not change the internal Runtime listener")
			}
		})
	}
}

func TestInstallPortSelectionRejectsConflictsAndInvalidMappings(t *testing.T) {
	for _, test := range []struct {
		name    string
		http    int
		network InstallNetworkOptions
	}{
		{"invalid project", 15301, InstallNetworkOptions{Project: "../other"}},
		{"negative debug", 15301, InstallNetworkOptions{PluginDebugPort: new(-1)}},
		{"oversized debug", 15301, InstallNetworkOptions{PluginDebugPort: new(65536)}},
		{"http debug conflict", 15301, InstallNetworkOptions{PluginDebugPort: new(15301)}},
		{"http adapter conflict", 2280, InstallNetworkOptions{}},
		{"debug adapter conflict", 15301, InstallNetworkOptions{PluginDebugPort: new(12280), AdapterPorts: []string{"12280:2280"}}},
		{"unequal ranges", 15301, InstallNetworkOptions{AdapterPorts: []string{"12280-12281:2280-2285"}}},
		{"reversed range", 15301, InstallNetworkOptions{AdapterPorts: []string{"12285-12280:2280-2285"}}},
		{"wrong target", 15301, InstallNetworkOptions{AdapterPorts: []string{"12280:5400"}}},
		{"zero host", 15301, InstallNetworkOptions{AdapterPorts: []string{"0:2280"}}},
		{"duplicate host", 15301, InstallNetworkOptions{AdapterPorts: []string{"12280:2280", "12280:2281"}}},
		{"duplicate target", 15301, InstallNetworkOptions{AdapterPorts: []string{"12280:2280", "12281:2280"}}},
		{"mixed none", 15301, InstallNetworkOptions{AdapterPorts: []string{"none", "12280:2280"}}},
		{"missing mapping", 15301, InstallNetworkOptions{AdapterPorts: []string{"12280"}}},
		{"unsupported protocol", 15301, InstallNetworkOptions{AdapterPorts: []string{"12280:2280/udp"}}},
		{"unsupported address", 15301, InstallNetworkOptions{AdapterPorts: []string{"127.0.0.1:12280:2280"}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := NewInstallAssets(t.TempDir(), "v4.10.10", "basic", test.http, test.network); err == nil {
				t.Fatal("invalid installation mapping was accepted")
			}
		})
	}
	if _, err := NewInstallAssets(t.TempDir(), "v4.10.10", "basic", 5401, InstallNetworkOptions{PluginDebugPort: new(int), AdapterPorts: []string{"none"}}); err != nil {
		t.Fatalf("disabled publications must release their former host ports: %v", err)
	}
}

func TestInstallPreflightIgnoresDisabledPortsAndChecksSelectedProject(t *testing.T) {
	// Reproduce #26 without stopping an existing service when it owns 5401.
	if listener, err := net.Listen("tcp", "127.0.0.1:5401"); err == nil {
		defer listener.Close()
	}
	if available, _ := portAvailable(5401); available {
		t.Fatal("could not establish occupied port 5401")
	}
	runner := &fakeRunner{docker: map[string]CommandResult{
		"--version": {Stdout: "Docker version 29"}, "info --format {{.ServerVersion}}": {Stdout: "29"}, "compose version --short": {Stdout: "5.5.1"},
		"ps -a --filter label=com.docker.compose.project=langbot-lbctl --format {{.ID}}": {Stdout: "unrelated-default-project"},
	}}
	assets, err := NewInstallAssets(filepath.Join(t.TempDir(), "new"), "v4.10.10", "all", freeInstallTestPort(t), InstallNetworkOptions{
		Project: "regression", PluginDebugPort: new(int), AdapterPorts: []string{"none"},
	})
	if err != nil {
		t.Fatal(err)
	}
	checks, err := PreflightInstall(context.Background(), assets, runner)
	if err != nil {
		t.Fatalf("headless isolated install must pass: %v %+v", err, checks)
	}
	for _, check := range checks {
		if check.Name == "port" && check.Detail != assets.Port {
			t.Fatalf("preflight checked an unpublished port: %+v", check)
		}
		if check.Name == "compose_project" && check.Detail != "regression" {
			t.Fatalf("preflight checked a different project: %+v", check)
		}
	}
	assets.Ports.PluginDebugPort = 5401
	checks, err = PreflightInstall(context.Background(), assets, runner)
	if err == nil {
		t.Fatal("publishing occupied debug port 5401 must fail")
	}
	for _, check := range checks {
		if check.Name == "port" && check.Detail == 5401 && check.Status == "error" {
			return
		}
	}
	t.Fatalf("missing occupied-port diagnostic: %+v", checks)
}

func TestInstallPreflightChecksCustomDebugAndAdapterPorts(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	occupied := listener.Addr().(*net.TCPAddr).Port
	runner := &fakeRunner{docker: map[string]CommandResult{
		"--version": {Stdout: "Docker version 29"}, "info --format {{.ServerVersion}}": {Stdout: "29"}, "compose version --short": {Stdout: "5.5.1"},
	}}
	for _, network := range []InstallNetworkOptions{
		{PluginDebugPort: &occupied, AdapterPorts: []string{"none"}},
		{PluginDebugPort: new(int), AdapterPorts: []string{fmt.Sprintf("%d:2283", occupied)}},
	} {
		assets, err := NewInstallAssets(filepath.Join(t.TempDir(), "new"), "v4.10.10", "basic", freeInstallTestPort(t), network)
		if err != nil {
			t.Fatal(err)
		}
		checks, err := PreflightInstall(context.Background(), assets, runner)
		if err == nil {
			t.Fatal("selected occupied host port was not rejected")
		}
		found := false
		for _, check := range checks {
			found = found || (check.Name == "port" && check.Detail == occupied && check.Status == "error")
		}
		if !found {
			t.Fatalf("missing selected-port diagnostic: %+v", checks)
		}
	}
}

func TestOwnedUpgradePreservesInstallPortsAndSupportsLegacyRecords(t *testing.T) {
	for _, network := range []InstallNetworkOptions{
		{},
		{Project: "second", PluginDebugPort: new(15401), AdapterPorts: []string{"12280-12285:2280-2285"}},
		{Project: "headless", PluginDebugPort: new(int), AdapterPorts: []string{"none"}},
	} {
		dir := filepath.Join(t.TempDir(), "deployment")
		assets, err := NewInstallAssets(dir, "v4.10.9", "basic", 15301, network)
		if err != nil {
			t.Fatal(err)
		}
		record := assets.Record("local-ports", "sha256:test")
		if network.Project == "" {
			record.InstallPorts = nil // Records written by the previous installer.
		}
		if err := (Store{Dir: dir}).Write(context.Background(), record); err != nil {
			t.Fatal(err)
		}
		if err := assets.Write(); err != nil {
			t.Fatal(err)
		}
		persisted, err := (Store{Dir: dir}).Read()
		if err != nil || !reflect.DeepEqual(persisted.InstallPorts, record.InstallPorts) {
			t.Fatalf("publication settings were not persisted: %+v %v", persisted.InstallPorts, err)
		}
		updated, _, err := ComposeForUpgrade(persisted, dir, "v4.10.10")
		if err != nil {
			t.Fatal(err)
		}
		if want := strings.ReplaceAll(string(assets.Compose), "v4.10.9", "v4.10.10"); string(updated) != want {
			t.Fatalf("upgrade modified more than images:\n%s", updated)
		}
		if err := WriteUpgradeCompose(persisted, updated); err != nil {
			t.Fatal(err)
		}
		persisted.CoreVersion, persisted.Asset.Name = "v4.10.10", "rockchin/langbot:v4.10.10"
		if err := ValidateOwnedLayout(persisted, dir); err != nil {
			t.Fatalf("upgraded layout rejected: %v", err)
		}
		if err := os.WriteFile(persisted.ComposeFile, append(updated, []byte("# manual edit\n")...), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := ValidateOwnedLayout(persisted, dir); err == nil {
			t.Fatal("custom publication support must not allow unconfirmed Compose edits")
		}
	}
}
