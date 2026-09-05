package config

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gofrs/flock"
)

func TestStoreUpdateAcrossProcesses(t *testing.T) {
	if os.Getenv("LBCTL_CONFIG_HELPER") == "1" {
		path := os.Getenv("LBCTL_CONFIG_PATH")
		index, _ := strconv.Atoi(os.Getenv("LBCTL_CONFIG_INDEX"))
		err := (Store{Path: path}).Update(context.Background(), func(file *File) error {
			if file.Contexts == nil {
				file.Contexts = make(map[string]Context)
			}
			// 让锁竞争更容易发生，验证的是跨进程读改写是否串行。
			time.Sleep(5 * time.Millisecond)
			file.Contexts[fmt.Sprintf("ctx-%d", index)] = Context{Endpoint: fmt.Sprintf("http://127.0.0.1:%d", 5300+index)}
			return nil
		})
		if err != nil {
			os.Exit(1)
		}
		os.Exit(0)
	}

	path := filepath.Join(t.TempDir(), "config.yaml")
	const processCount = 12
	commands := make([]*exec.Cmd, 0, processCount)
	for i := 0; i < processCount; i++ {
		cmd := exec.Command(os.Args[0], "-test.run=TestStoreUpdateAcrossProcesses", "--")
		cmd.Env = append(os.Environ(),
			"LBCTL_CONFIG_HELPER=1",
			"LBCTL_CONFIG_PATH="+path,
			"LBCTL_CONFIG_INDEX="+strconv.Itoa(i),
		)
		cmd.Stdout = io.Discard
		cmd.Stderr = io.Discard
		commands = append(commands, cmd)
	}
	for _, cmd := range commands {
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
	}
	for _, cmd := range commands {
		if err := cmd.Wait(); err != nil {
			t.Fatalf("helper process failed: %v", err)
		}
	}

	file, err := (Store{Path: path}).Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(file.Contexts) != processCount {
		t.Fatalf("并发更新丢失：got %d contexts, want %d", len(file.Contexts), processCount)
	}
}

func TestStoreUpdateDoesNotOverwriteInvalidConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	original := "version: 1\ncontexts: [\n"
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	err := (Store{Path: path}).Update(context.Background(), func(*File) error { return nil })
	if err == nil {
		t.Fatal("invalid config should fail")
	}
	data, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(data) != original {
		t.Fatal("invalid config was overwritten")
	}
}

func TestStoreUpdateDoesNotWriteOversizedConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	store := Store{Path: path}
	if err := store.Update(context.Background(), func(file *File) error {
		file.Contexts["small"] = Context{Endpoint: "http://localhost:5300"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Update(context.Background(), func(file *File) error {
		file.Contexts["large"] = Context{
			Endpoint:              "http://localhost:5300",
			ExpectedWorkspaceUUID: strings.Repeat("x", maxConfigBytes),
		}
		return nil
	}); err == nil {
		t.Fatal("oversized config should fail")
	}
	current, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(current) != string(original) {
		t.Fatal("oversized config replaced the previous file")
	}
}

func TestStoreUpdateLockCancellationDoesNotWrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	lock := flock.New(path + ".lock")
	locked, err := lock.TryLock()
	if err != nil || !locked {
		t.Fatalf("failed to acquire test lock: locked=%v err=%v", locked, err)
	}
	defer func() { _ = lock.Unlock() }()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	err = (Store{Path: path}).Update(ctx, func(file *File) error {
		file.Contexts["must-not-write"] = Context{Endpoint: "http://localhost:5300"}
		return nil
	})
	if err == nil {
		t.Fatal("lock cancellation should fail")
	}
	if _, statErr := os.Stat(path); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("cancelled update wrote config: %v", statErr)
	}
}

func TestDefaultPathUsesConfigDirectory(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	path, err := DefaultPath()
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(home, ".config", "langbot", "config.yaml")
	if path != want {
		t.Fatalf("default path = %q, want %q", path, want)
	}
}

func TestStoreRejectsUnknownFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	data := "version: 1\ncontexts: {}\nunknown: true\n"
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := (Store{Path: path}).Load(); err == nil {
		t.Fatal("unknown fields should fail")
	}
}

func TestResolvePrecedenceAndEndpointIsolation(t *testing.T) {
	file := File{
		Version:        1,
		CurrentContext: "dev",
		Contexts: map[string]Context{
			"dev": {
				Endpoint:              "http://localhost:5300/api/",
				Credential:            &Credential{Type: "env", Name: "DEV_KEY"},
				ExpectedWorkspaceUUID: "workspace-dev",
			},
		},
	}
	values := map[string]string{
		"LANGBOT_CONTEXT":  "dev",
		"LANGBOT_ENDPOINT": "http://localhost:5300/api",
	}
	lookup := func(name string) (string, bool) { value, ok := values[name]; return value, ok }
	connection, err := Resolve(file, Options{}, lookup)
	if err != nil {
		t.Fatal(err)
	}
	if connection.Endpoint != "http://localhost:5300/api" || connection.ExpectedWorkspaceUUID != "workspace-dev" {
		t.Fatalf("unexpected connection: %+v", connection)
	}

	values["LANGBOT_ENDPOINT"] = "https://other.example/base/"
	connection, err = Resolve(file, Options{}, lookup)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := connection.WithCredential(Options{}, lookup, nil); err == nil {
		t.Fatal("endpoint override must require an explicit credential")
	}

	values["LANGBOT_API_KEY"] = "new-key"
	connection, err = Resolve(file, Options{}, lookup)
	if err != nil {
		t.Fatal(err)
	}
	if !connection.Temporary || connection.ExpectedWorkspaceUUID != "" || connection.CredentialSource != "env:LANGBOT_API_KEY" {
		t.Fatalf("endpoint override leaked context binding: %+v", connection)
	}
	resolved, err := connection.WithCredential(Options{}, lookup, nil)
	if err != nil || resolved.APIKey != "new-key" {
		t.Fatalf("credential resolution failed: err=%v key=%q", err, resolved.APIKey)
	}
}

func TestResolveAPIKeyStdinWinsOverEnvironment(t *testing.T) {
	file := File{Version: 1, CurrentContext: "dev", Contexts: map[string]Context{
		"dev": {Endpoint: "http://localhost:5300", Credential: &Credential{Type: "env", Name: "DEV_KEY"}},
	}}
	lookup := func(name string) (string, bool) {
		if name == "LANGBOT_API_KEY" {
			return "environment-key", true
		}
		if name == "DEV_KEY" {
			return "context-key", true
		}
		return "", false
	}
	options := Options{APIKeyStdin: true}
	connection, err := Resolve(file, options, lookup)
	if err != nil {
		t.Fatal(err)
	}
	if connection.CredentialSource != "stdin" {
		t.Fatalf("stdin did not win: %+v", connection)
	}
	resolved, err := connection.WithCredential(options, lookup, strings.NewReader("stdin-key\n"))
	if err != nil || resolved.APIKey != "stdin-key" {
		t.Fatalf("stdin credential failed: err=%v key=%q", err, resolved.APIKey)
	}
}

func TestResolveOnlyChecksCredentialSource(t *testing.T) {
	file := File{Version: 1, CurrentContext: "dev", Contexts: map[string]Context{
		"dev": {Endpoint: "http://localhost:5300", Credential: &Credential{Type: "env", Name: "CUSTOM_KEY"}},
	}}
	var requested []string
	lookup := func(name string) (string, bool) {
		requested = append(requested, name)
		if name == "CUSTOM_KEY" {
			return "secret-value", true
		}
		return "", false
	}
	connection, err := Resolve(file, Options{}, lookup)
	if err != nil {
		t.Fatal(err)
	}
	if connection.CredentialSource != "env:CUSTOM_KEY" {
		t.Fatalf("unexpected source: %q", connection.CredentialSource)
	}
	for _, name := range requested {
		if name == "CUSTOM_KEY" {
			t.Fatal("Resolve read the custom credential value")
		}
	}
}

func TestResolveShowsEmptyExplicitKeyButCredentialReadFails(t *testing.T) {
	file := File{Version: 1, CurrentContext: "dev", Contexts: map[string]Context{
		"dev": {Endpoint: "http://localhost:5300"},
	}}
	lookup := func(name string) (string, bool) {
		if name == "LANGBOT_API_KEY" {
			return "", true
		}
		return "", false
	}
	connection, err := Resolve(file, Options{}, lookup)
	if err != nil {
		t.Fatal(err)
	}
	if connection.CredentialSource != "env:LANGBOT_API_KEY" {
		t.Fatalf("empty explicit key source was not preserved: %q", connection.CredentialSource)
	}
	if _, err := connection.WithCredential(Options{}, lookup, nil); err == nil {
		t.Fatal("empty explicit key should fail when credentials are loaded")
	}
}

func TestResolveRejectsExplicitEmptyTimeout(t *testing.T) {
	file := File{Version: 1, CurrentContext: "dev", Contexts: map[string]Context{
		"dev": {Endpoint: "http://localhost:5300"},
	}}
	_, err := Resolve(file, Options{TimeoutSet: true}, func(string) (string, bool) { return "", false })
	if err == nil {
		t.Fatal("explicit empty timeout should fail")
	}
}

func TestConnectionDoesNotMarshalSecret(t *testing.T) {
	connection := Connection{Endpoint: "http://localhost:5300", APIKey: "do-not-print"}
	data, err := json.Marshal(connection)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "do-not-print") {
		t.Fatal("API key was serialized")
	}
}

func TestUpdateSerializesThreadsWithinOneProcess(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	store := Store{Path: path}
	var group sync.WaitGroup
	for i := 0; i < 8; i++ {
		group.Add(1)
		go func(index int) {
			defer group.Done()
			_ = store.Update(context.Background(), func(file *File) error {
				file.Contexts[fmt.Sprintf("thread-%d", index)] = Context{Endpoint: "http://localhost:5300"}
				return nil
			})
		}(i)
	}
	group.Wait()
	file, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(file.Contexts) != 8 {
		t.Fatalf("thread update lost data: got %d", len(file.Contexts))
	}
}

func TestCredentialInputErrorsDoNotExposeValue(t *testing.T) {
	file := File{Version: 1, CurrentContext: "dev", Contexts: map[string]Context{
		"dev": {Endpoint: "http://localhost:5300", Credential: &Credential{Type: "env", Name: "DEV_KEY"}},
	}}
	connection, err := Resolve(file, Options{}, func(string) (string, bool) { return "", false })
	if err != nil {
		t.Fatal(err)
	}
	_, err = connection.WithCredential(Options{}, func(string) (string, bool) { return "", false }, nil)
	if err == nil {
		t.Fatal("missing credential should fail")
	}
	if errors.Is(err, os.ErrNotExist) {
		t.Fatal("raw filesystem error leaked")
	}
}
