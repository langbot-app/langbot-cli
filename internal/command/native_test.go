package command

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestNativeDefaultDryRunIsMachineReadableAndDoesNotInstall(t *testing.T) {
	root := t.TempDir()
	t.Setenv("LANGBOT_HOME", root)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	listener.Close()
	var output, diagnostic bytes.Buffer
	code := executeChinese(context.Background(), []string{"install", "--version", "v4.11.8", "--region", "global", "--port", strconv.Itoa(port), "--dry-run", "--output", "json"}, Dependencies{Out: &output, Err: &diagnostic})
	var envelope struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(output.Bytes(), &envelope); err != nil || code != 0 {
		t.Fatalf("%s %v", output.String(), err)
	}
	for _, key := range []string{"url", "driver", "instance_dir", "version", "name", "region", "service_manager"} {
		if envelope.Data[key] == nil {
			t.Errorf("missing %s", key)
		}
	}
	if envelope.Data["driver"] != "native" || envelope.Data["name"] != "default" {
		t.Fatal(envelope.Data)
	}
	if _, err := os.Stat(filepath.Join(root, "instances")); !os.IsNotExist(err) {
		t.Fatal("dry-run wrote deployment")
	}
}

func TestNativeRejectsInvalidExtraAndOccupiedPortBeforeInstall(t *testing.T) {
	root := t.TempDir()
	t.Setenv("LANGBOT_HOME", root)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	for _, args := range [][]string{{"--extra", "unknown"}, {"--port", strconv.Itoa(listener.Addr().(*net.TCPAddr).Port)}} {
		var output bytes.Buffer
		code := executeChinese(context.Background(), append([]string{"install", "--driver", "native", "--region", "global", "--version", "v4.11.8", "--yes", "--output", "json"}, args...), Dependencies{Out: &output})
		if code == 0 {
			t.Fatalf("accepted invalid install %v: %s", args, output.String())
		}
		if _, err := os.Stat(filepath.Join(root, "instances")); !os.IsNotExist(err) {
			t.Fatal("invalid install wrote deployment")
		}
	}
}

func TestPassiveUpdateDoesNotReadEnvironmentForMachineOutput(t *testing.T) {
	var buffer bytes.Buffer
	passiveUpdateNotice(Dependencies{Err: &buffer, LookupEnv: func(string) (string, bool) { panic("non-terminal lookup") }}, &globalFlags{output: "json"})
	if buffer.Len() != 0 {
		t.Fatal(buffer.String())
	}
}

func TestInstallSelectionRejectsInvalidDriverAndPort(t *testing.T) {
	deps := normalizeDependencies(Dependencies{In: strings.NewReader(""), Err: &bytes.Buffer{}})
	if _, _, err := installSelection(context.Background(), deps, "invalid", 5300, true); err == nil {
		t.Fatal("invalid driver accepted")
	}
	if _, _, err := installSelection(context.Background(), deps, "native", 65536, true); err == nil {
		t.Fatal("invalid port accepted")
	}
}
