package command

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"unicode"
)

func TestLocalizedCommandAndStableMachineOutput(t *testing.T) {
	for _, args := range [][]string{{"version"}, {"--help"}, {"bot", "update", "--help"}, {"context", "use"}, {"schema"}, {"context", "use", "--output", "json"}, {"help", "unknown", "--output", "json"}, {"--context", "--help", "status", "--output", "json"}} {
		configPath := filepath.Join(t.TempDir(), "config.yaml")
		results := map[string]string{}
		for _, lang := range []string{"en", "zh-Hans"} {
			var out, diagnostic bytes.Buffer
			Execute(context.Background(), append([]string{"--lang", lang}, args...), Dependencies{Out: &out, Err: &diagnostic, LookupEnv: func(string) (string, bool) { return "", false }, DefaultConfigPath: func() (string, error) { return configPath, nil }, Version: "test"})
			results[lang] = out.String()
			machine := args[0] == "schema" || args[len(args)-1] == "json"
			if lang == "en" && !machine && strings.ContainsFunc(out.String(), func(char rune) bool { return unicode.Is(unicode.Han, char) }) {
				t.Fatalf("English contains Chinese: %s", &out)
			}
		}
		if (args[0] == "schema" || args[len(args)-1] == "json") && results["en"] != results["zh-Hans"] {
			t.Fatalf("machine output varies with locale: %v", args)
		}
	}
}

func TestHelpUsesLocaleWithJSONFlag(t *testing.T) {
	for lang, heading := range map[string]string{"en": "Usage:", "zh-Hans": "用法："} {
		var out, diagnostic bytes.Buffer
		code := Execute(context.Background(), []string{"--lang", lang, "--output", "json", "bot", "update", "--help"}, Dependencies{Out: &out, Err: &diagnostic})
		if code != 0 || !strings.Contains(out.String(), heading) || json.Valid(out.Bytes()) {
			t.Fatalf("%s: %d %s", lang, code, &out)
		}
	}
}

func TestNoticeDoesNotPolluteJSON(t *testing.T) {
	home := t.TempDir()
	for attempt := 0; attempt < 2; attempt++ {
		var out, diagnostic bytes.Buffer
		code := Execute(context.Background(), []string{"--lang", "en", "version", "-o", "json"}, Dependencies{NoticeHome: home, Out: &out, Err: &diagnostic, Version: "test", LookupEnv: func(string) (string, bool) { return "", false }})
		if code != 0 || !json.Valid(out.Bytes()) {
			t.Fatalf("%d: %s", code, &out)
		}
		if strings.Contains(diagnostic.String(), "https://langbot.app/terms") != (attempt == 0) {
			t.Fatal(diagnostic.String())
		}
	}
}
