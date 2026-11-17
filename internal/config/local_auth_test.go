package config

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

func TestInvalidCredentialReferenceDescribesBothSources(t *testing.T) {
	store := Store{Path: filepath.Join(t.TempDir(), "config.yaml")}
	err := store.Update(context.Background(), func(file *File) error {
		file.Contexts["invalid"] = Context{Endpoint: "http://localhost:5300", Credential: &Credential{Type: "local_deployment", Name: "relative"}}
		return nil
	})
	if err == nil || !strings.Contains(err.Error(), "env") || !strings.Contains(err.Error(), "local_deployment") {
		t.Fatalf("credential error does not describe supported sources: %v", err)
	}
}

func TestLocalCredentialPriorityAndIsolation(t *testing.T) {
	root := t.TempDir()
	file := emptyFile()
	file.CurrentContext = "first"
	for _, name := range []string{"first", "second"} {
		path := filepath.Join(root, name)
		if err := os.WriteFile(path, []byte("private-"+name), 0o600); err != nil {
			t.Fatal(err)
		}
		file.Contexts[name] = Context{Endpoint: "http://" + name + ".localhost:5300", Credential: &Credential{Type: "local_deployment", Name: path}}
	}
	for _, test := range []struct{ name, env, stdin, endpoint, want string }{
		{name: "first", want: "private-first"},
		{name: "second", want: "private-second"},
		{name: "first", env: "explicit-env", want: "explicit-env"},
		{name: "first", env: "explicit-env", stdin: "explicit-stdin", want: "explicit-stdin"},
		{name: "first", endpoint: "https://different.example"},
	} {
		lookup := func(name string) (string, bool) { return test.env, name == "LANGBOT_API_KEY" && test.env != "" }
		opts := Options{Context: test.name, ContextSet: true, APIKeyStdin: test.stdin != "", Endpoint: test.endpoint, EndpointSet: test.endpoint != ""}
		conn, err := Resolve(file, opts, lookup)
		if err != nil {
			t.Fatal(err)
		}
		conn, err = conn.WithCredential(opts, lookup, strings.NewReader(test.stdin))
		if test.want == "" {
			if err == nil || conn.APIKey != "" {
				t.Fatal("credential followed a changed endpoint")
			}
			continue
		}
		if err != nil || conn.APIKey != test.want {
			t.Fatal("wrong credential precedence")
		}
		for _, marshal := range []func(any) ([]byte, error){json.Marshal, yaml.Marshal} {
			data, err := marshal(conn)
			if err != nil || strings.Contains(string(data), test.want) {
				t.Fatal("connection leaks key")
			}
		}
	}
	store := Store{Path: filepath.Join(root, "config.yaml")}
	if err := store.Update(context.Background(), func(saved *File) error { *saved = file; return nil }); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(store.Path)
	if strings.Contains(string(data), "private-first") || strings.Contains(string(data), "private-second") {
		t.Fatal("configuration contains secrets")
	}
}
