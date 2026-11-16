package command

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/langbot-app/langbot-cli/internal/telemetry"
	"github.com/spf13/cobra"
)

func TestLifecycleFailureTelemetry(t *testing.T) {
	for _, name := range []string{"install", "upgrade", "uninstall", "start", "stop", "restart", "update", "self-update", "autostart status", "autostart enable", "autostart disable"} {
		t.Run(name, func(t *testing.T) {
			var mutex sync.Mutex
			var events []telemetry.Event
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				var event telemetry.Event
				if err := json.NewDecoder(request.Body).Decode(&event); err != nil {
					t.Error(err)
				}
				mutex.Lock()
				events = append(events, event)
				mutex.Unlock()
			}))
			defer server.Close()
			reporter := telemetry.New(telemetry.Options{Home: t.TempDir(), Version: "test", LookupEnv: func(key string) (string, bool) {
				return server.URL, key == "LBCTL_TELEMETRY_ENDPOINT"
			}})
			code := executeChinese(t.Context(), append(strings.Fields(name), "--not-a-valid-flag"), Dependencies{Telemetry: reporter, Out: io.Discard, Err: io.Discard})
			reporter.Close()
			mutex.Lock()
			defer mutex.Unlock()
			if code == 0 || len(events) != 2 {
				t.Fatalf("code=%d events=%+v", code, events)
			}
			outcomes := map[string]int{}
			for _, event := range events {
				outcomes[event.Outcome]++
				if event.Outcome == "failed" && (event.Step == "" || event.ErrorCode != "input") {
					t.Fatal(event)
				}
			}
			if outcomes["started"] != 1 || outcomes["failed"] != 1 {
				t.Fatal(outcomes)
			}
		})
	}
}

func TestAutostartTelemetryCommandSelection(t *testing.T) {
	root := &cobra.Command{Use: "lbctl"}
	group := &cobra.Command{Use: "autostart"}
	root.AddCommand(group)
	for _, action := range []string{"status", "enable", "disable"} {
		group.AddCommand(&cobra.Command{Use: action})
	}
	root.AddCommand(&cobra.Command{Use: "status"})
	for _, action := range []string{"status", "enable", "disable"} {
		selected, _, err := root.Find([]string{"autostart", action, "--invalid-flag"})
		if err != nil {
			t.Fatal(err)
		}
		if got := lifecycleTelemetryCommand(root, selected); got != "autostart."+action {
			t.Fatalf("%s: %s", action, got)
		}
	}
	selected, _, _ := root.Find([]string{"status"})
	if lifecycleTelemetryCommand(root, selected) != "" || lifecycleTelemetryCommand(root, group) != "" {
		t.Fatal("unrelated command or group was reported")
	}
}

func TestInstallOccupiedPortReportsFailure(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	var mutex sync.Mutex
	var events []telemetry.Event
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var event telemetry.Event
		if err := json.NewDecoder(request.Body).Decode(&event); err != nil {
			t.Error(err)
		}
		mutex.Lock()
		events = append(events, event)
		mutex.Unlock()
	}))
	defer server.Close()
	home := t.TempDir()
	t.Setenv("LANGBOT_HOME", home)
	reporter := telemetry.New(telemetry.Options{Home: home, Version: "test", LookupEnv: func(key string) (string, bool) {
		if key == "LBCTL_TELEMETRY_ENDPOINT" {
			return server.URL, true
		}
		return "", false
	}})
	args := []string{"install", "--driver", "native", "--yes", "--region", "global", "--version", "v4.10.0", "--port", strconv.Itoa(listener.Addr().(*net.TCPAddr).Port)}
	code := executeChinese(t.Context(), args, Dependencies{Telemetry: reporter, Out: io.Discard, Err: io.Discard, LocalDir: filepath.Join(home, "instance"), LocalLocatorPath: filepath.Join(home, "locator.json")})
	reporter.Close()
	mutex.Lock()
	defer mutex.Unlock()
	if code != 6 || len(events) != 2 {
		t.Fatalf("code=%d events=%+v", code, events)
	}
	for _, event := range events {
		if event.Outcome == "failed" && (event.Step != "check_ports" || event.Driver != "native" || event.Error == "") {
			t.Fatalf("failure: %+v", event)
		}
	}
}
