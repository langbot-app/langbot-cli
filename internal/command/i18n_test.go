package command

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"unicode"

	"github.com/langbot-app/langbot-cli/internal/app"
)

func TestNoBrandPreservesCommandOutput(t *testing.T) {
	for _, setting := range []string{"", "0", "1"} {
		for _, command := range []string{"install", "upgrade"} {
			var out, diagnostic bytes.Buffer
			deps := Dependencies{Out: &out, Err: &diagnostic, DefaultConfigPath: func() (string, error) { return filepath.Join(t.TempDir(), "config.yaml"), nil }, LookupEnv: func(key string) (string, bool) {
				return setting, key == "LBCTL_NO_BRAND"
			}}
			flags := &globalFlags{commandPath: command}
			err := emitCall(deps, flags, func() (app.Result, error) {
				return app.Result{Data: map[string]any{"action": "install", "changed": true, "status": map[string]any{"status": "running"}, "version": "v4.11.8", "endpoint": "http://127.0.0.1:5300"}}, nil
			})
			if err != nil || strings.Contains(diagnostic.String(), "LangBot") != (setting != "1") || !strings.Contains(out.String(), "http://127.0.0.1:5300") {
				t.Fatalf("%s/%s: %v %s %s", command, setting, err, &out, &diagnostic)
			}
		}
	}
}

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

func TestRootOverviewAndMachineOutput(t *testing.T) {
	for _, lang := range []string{"en", "zh-Hans"} {
		for _, machine := range []bool{false, true} {
			var out, diagnostic bytes.Buffer
			args := []string{"--lang", lang}
			if machine {
				args = append(args, "-o", "json")
			}
			code := Execute(context.Background(), args, Dependencies{Out: &out, Err: &diagnostic})
			if code != 0 || json.Valid(out.Bytes()) != machine {
				t.Fatalf("%d: %s", code, &out)
			}
			if !machine && (!strings.Contains(out.String(), "LangBot") || !strings.Contains(out.String(), "lbctl --help") || strings.Contains(out.String(), "--api-key-stdin")) {
				t.Fatal(out.String())
			}
		}
	}
}

func TestHelpProbeDoesNotConsumeTermsNotice(t *testing.T) {
	home := t.TempDir()
	env := func(string) (string, bool) { return "", false }
	for _, args := range [][]string{{"install", "--help"}, {"help", "install"}, {"-h"}} {
		var out, diagnostic bytes.Buffer
		Execute(context.Background(), args, Dependencies{NoticeHome: home, Out: &out, Err: &diagnostic, Version: "test", LookupEnv: env})
		if strings.Contains(diagnostic.String()+out.String(), "https://langbot.app/terms") {
			t.Fatalf("%v showed the notice", args)
		}
	}
	var out, diagnostic bytes.Buffer
	Execute(context.Background(), []string{"--lang", "en", "version"}, Dependencies{NoticeHome: home, Out: &out, Err: &diagnostic, Version: "test", LookupEnv: env})
	if !strings.Contains(diagnostic.String(), "https://langbot.app/terms") {
		t.Fatalf("notice was consumed by a help probe: %s", diagnostic.String())
	}
}
