package output

import (
	"bytes"
	"strings"
	"testing"

	"github.com/langbot-app/langbot-cli/internal/i18n"
)

func TestChineseStatusUsesFullwidthSeparators(t *testing.T) {
	defer i18n.Use(i18n.Chinese)()
	var out bytes.Buffer
	err := renderActiveEnvironment(&out, map[string]any{
		"name": "default", "type": "community", "connection": map[string]any{"endpoint": "http://localhost:5300", "reachable": true},
		"runtime": map[string]any{"dir": "/opt/langbot"},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, label := range []string{"当前环境", "地址", "连接", "认证", "目录"} {
		if strings.Contains(out.String(), label+":") || !strings.Contains(out.String(), label+"：") {
			t.Fatal(out.String())
		}
	}
}

func TestVersionSummary(t *testing.T) {
	defer i18n.Use(i18n.English)()
	for _, test := range []struct{ current, latest, want string }{
		{"v0.5.3", "v0.5.3", "v0.5.3 (up to date)"},
		{"v0.5.2", "v0.5.3", "v0.5.2 · Update available: v0.5.3 (lbctl update)"},
		{"v0.5.3-rc", "v0.5.2", "v0.5.3-rc"},
		{"dev", "v0.5.2", "dev"},
		{"v4.11.8", "v4.11.8", "v4.11.8 (up to date)"},
	} {
		if got := versionSummary(test.current, test.latest, "lbctl update"); got != test.want || strings.Contains(got, "→") {
			t.Fatalf("got %q want %q", got, test.want)
		}
	}
}
