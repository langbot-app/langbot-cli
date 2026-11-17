package command

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/langbot-app/langbot-cli/internal/config"
)

func TestShowKeyRequiresExplicitConfirmation(t *testing.T) {
	root := t.TempDir()
	secret := "never-show-this-without-confirmation"
	keyPath := filepath.Join(root, ".api-key")
	if err := os.WriteFile(keyPath, []byte(secret), 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "config.yaml")
	if err := (config.Store{Path: path}).Update(context.Background(), func(file *config.File) error {
		file.CurrentContext = "local-test"
		file.Contexts["local-test"] = config.Context{Endpoint: "http://localhost:5300", Credential: &config.Credential{Type: "local_deployment", Name: keyPath}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		args   []string
		reveal bool
	}{
		{args: []string{"auth", "show-key"}},
		{args: []string{"auth", "show-key", "-o", "json"}},
		{args: []string{"context", "show", "-o", "json"}},
		{args: []string{"context", "show", "-o", "yaml"}},
		{args: []string{"auth", "show-key", "--yes"}, reveal: true},
	} {
		var out, diagnostic bytes.Buffer
		code := Execute(context.Background(), test.args, Dependencies{Out: &out, Err: &diagnostic, In: strings.NewReader(""), DefaultConfigPath: func() (string, error) { return path, nil }, LookupEnv: func(string) (string, bool) { return "", false }, Transport: panicRoundTripper{}})
		if test.reveal {
			if code != 0 || strings.TrimSpace(out.String()) != secret {
				t.Fatal("explicit key reveal failed")
			}
		} else if strings.Contains(out.String()+diagnostic.String(), secret) {
			t.Fatal("key leaked without confirmation")
		}
	}
}
