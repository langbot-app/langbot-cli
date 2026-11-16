package output

import (
	"strings"
	"testing"

	"github.com/langbot-app/langbot-cli/internal/i18n"
)

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
