package output

import (
	"bytes"
	"strings"
	"testing"

	"github.com/langbot-app/langbot-cli/internal/i18n"
)

func TestLocalizedListAndFieldPunctuation(t *testing.T) {
	for _, lang := range []string{i18n.English, i18n.Chinese} {
		t.Run(lang, func(t *testing.T) {
			defer i18n.Use(lang)()
			var out bytes.Buffer
			if err := renderResourceList(&out, "instances", []any{map[string]any{"name": "default", "dir": "/opt/langbot", "port": 5300}}); err != nil {
				t.Fatal(err)
			}
			heading := "Local instances:"
			if lang == i18n.Chinese {
				heading = "本机实例："
			}
			if !strings.HasPrefix(out.String(), heading+"\n") {
				t.Fatal(out.String())
			}
			out.Reset()
			if err := renderValue(&out, "http://localhost:5300", fieldLabel("name")); err != nil {
				t.Fatal(err)
			}
			separator := ": "
			if lang == i18n.Chinese {
				separator = "："
			}
			if out.String() != fieldLabel("name")+separator+"http://localhost:5300\n" {
				t.Fatal(out.String())
			}
		})
	}
}
