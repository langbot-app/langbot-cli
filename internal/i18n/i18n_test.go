package i18n

import (
	"bytes"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"unicode"
)

func TestLanguagePriority(t *testing.T) {
	for _, test := range []struct {
		explicit     string
		env          map[string]string
		system, want string
	}{
		{"zh-Hans", map[string]string{"LBCTL_LANG": "en"}, "en", Chinese},
		{"", map[string]string{"LBCTL_LANG": "zh_CN.UTF-8", "LANGBOT_LANG": "en"}, "en", Chinese},
		{"", map[string]string{"LANGBOT_LANG": "zh", "LC_ALL": "C"}, "en", Chinese},
		{"", map[string]string{"LC_ALL": "C", "LC_MESSAGES": "zh_CN"}, "zh", Chinese},
		{"", map[string]string{"LC_MESSAGES": "zh_CN.UTF-8", "LANG": "en_US"}, "en", Chinese},
		{"", map[string]string{"LANG": "zh-Hans-CN"}, "en", Chinese},
		{"", map[string]string{"LANG": "POSIX"}, "zh", Chinese},
		{"", map[string]string{"LANG": "C.UTF-8"}, "zh-CN", Chinese},
		{"", map[string]string{"LC_ALL": "C.UTF-8", "LANG": "zh_CN.UTF-8"}, "en", Chinese},
		{"", map[string]string{"LANG": "C"}, "", English},
		{"", nil, "zh-CN", Chinese}, {"", nil, "en-US", English}, {"", nil, "", English},
		{"fr", map[string]string{"LANG": "zh"}, "zh", English},
	} {
		calls := 0
		got := Detect(test.explicit, func(key string) (string, bool) { value, ok := test.env[key]; return value, ok }, func() string { calls++; return test.system })
		if got != test.want {
			t.Fatalf("%+v: %s", test, got)
		}
		hasLocale := meaningfulLocale(test.explicit)
		for _, value := range test.env {
			hasLocale = hasLocale || meaningfulLocale(value)
		}
		if hasLocale && calls != 0 {
			t.Fatal("unnecessary system language lookup")
		}
	}
}

func TestCatalogFallbackAndMachineMode(t *testing.T) {
	defer Use(English)()
	if T("No data") != "No data" {
		t.Fatal("English translation")
	}
	Configure(Chinese, false)
	if T("No data") != "无数据" || T("untranslated message") != "untranslated message" {
		t.Fatal("Chinese or fallback translation")
	}
	Configure(English, true)
	if T("No data") != Source("No data") || T("deployment changed") != "deployment changed" {
		t.Fatal("machine output changed")
	}
}

func TestCatalogFormatArguments(t *testing.T) {
	format := regexp.MustCompile(`%[-+# 0-9.\[\]]*[a-zA-Z%]`)
	for key, value := range chinese {
		if strings.Join(format.FindAllString(key, -1), "|") != strings.Join(format.FindAllString(value, -1), "|") {
			t.Errorf("format arguments differ: %q / %q", key, value)
		}
	}
}

func TestFirstRunNotice(t *testing.T) {
	for _, lang := range []string{English, Chinese} {
		home := t.TempDir()
		var out bytes.Buffer
		if err := FirstRun(home, &out, lang); err != nil {
			t.Fatal(err)
		}
		first := out.String()
		for _, expected := range []string{"https://langbot.app/terms", "LANGBOT_TELEMETRY=0", "DO_NOT_TRACK=1"} {
			if !strings.Contains(first, expected) {
				t.Fatal(first)
			}
		}
		if err := FirstRun(home, &out, lang); err != nil || out.String() != first {
			t.Fatalf("notice repeated: %v", err)
		}
	}
}

func TestNoCJKLiteralsOutsideCatalog(t *testing.T) {
	err := filepath.Walk("../..", func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() || strings.Contains(filepath.ToSlash(path), "/i18n/") || strings.HasSuffix(path, "_test.go") || !strings.HasSuffix(path, ".go") {
			return nil
		}
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			return err
		}
		ast.Inspect(file, func(node ast.Node) bool {
			literal, ok := node.(*ast.BasicLit)
			if !ok || literal.Kind != token.STRING {
				return true
			}
			value, _ := strconv.Unquote(literal.Value)
			if strings.ContainsFunc(value, func(char rune) bool { return unicode.Is(unicode.Han, char) }) {
				t.Errorf("move user-visible text to i18n: %s: %s", path, value)
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
